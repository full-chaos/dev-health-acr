package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestWebAssertion_contextPacketsAuthorizeOnlyResolvedRepositoryScopes(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	if app.limits == nil {
		t.Fatal("test app has no request controls")
	}
	request := webContextRequest(t, now, private, hostedContextRequest(), "request_1")

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	audit := app.runtime.Audit.(*memory.AuditStore).Events()
	for _, event := range audit {
		if event.Action == "context_packet_generated" && (event.ActorType != string(auth.WebAssertionAuthenticationMethod) || event.ActorID != "user_123") {
			t.Fatalf("web audit actor = %#v", event)
		}
	}
}

func TestWebAssertion_contextPacketsRejectForeignRepositoryAndReplay(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	foreign := hostedContextRequest()
	foreign.Repository.Slug = "other-org/other-repo"
	foreignRequest := webContextRequest(t, now, private, foreign, "foreign_1")
	direct := webContextRequest(t, now, private, foreign, "foreign_direct")
	if _, err := verifier.Verify(direct); err != nil {
		t.Fatalf("direct foreign assertion validation = %v", err)
	}
	replayRequest := webContextRequest(t, now, private, hostedContextRequest(), "replay_1")
	duplicateReplayRequest := webContextRequest(t, now, private, hostedContextRequest(), "replay_1")

	// When
	foreignResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(foreignResponse, foreignRequest)
	firstReplay := httptest.NewRecorder()
	app.Handler().ServeHTTP(firstReplay, replayRequest)
	secondReplay := httptest.NewRecorder()
	app.Handler().ServeHTTP(secondReplay, duplicateReplayRequest)

	// Then
	assertErrorResponse(t, foreignResponse, http.StatusForbidden, "repo_forbidden")
	if firstReplay.Code != http.StatusOK {
		t.Fatalf("first replay status = %d body=%s", firstReplay.Code, firstReplay.Body.String())
	}
	assertErrorResponse(t, secondReplay, http.StatusTooManyRequests, "rate_limited")
}

func TestWebAssertion_cannotAuthenticateEpisodeWrite(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	body := []byte(`{}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/episodes", bytes.NewReader(body))
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set(auth.WebAssertionHeader, signAPIAssertion(t, private, request, body, "write_1"))

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
}

func webContextRequest(t *testing.T, now time.Time, private ed25519.PrivateKey, requestBody any, jti string) *http.Request {
	t.Helper()
	body, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WebAssertionHeader, signAPIAssertionAt(t, private, request, body, jti, now))
	return request
}

func signAPIAssertion(t *testing.T, private ed25519.PrivateKey, request *http.Request, body []byte, jti string) string {
	t.Helper()
	return signAPIAssertionAt(t, private, request, body, jti, time.Now().UTC())
}

func signAPIAssertionAt(t *testing.T, private ed25519.PrivateKey, request *http.Request, body []byte, jti string, now time.Time) string {
	t.Helper()
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": "org_1",
		"repository_scopes": []string{hostedTestRepository}, "permissions": []string{auth.ScopeContextRead, auth.ScopeEvidenceRead},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": jti,
		"method": request.Method, "path": request.URL.EscapedPath(), "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
}

func writeAPIJWKS(t *testing.T, public ed25519.PublicKey) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "kid": "current", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(public)}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "web-assertions.jwks.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
