package auth

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
	"slices"
	"testing"
	"time"
)

func TestWebAssertionVerifier_authenticatesBoundReadRequest(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := writeTestJWKS(t, "current", public)
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: path, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema_version":"context_packet_request.v1"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	token := signTestWebAssertion(t, private, "current", webAssertionClaims(now, request, body))
	request.Header.Set(WebAssertionHeader, token)

	// When
	principal, err := verifier.Verify(request)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if principal.AuthenticationMethod != WebAssertionAuthenticationMethod || principal.Subject != "user_123" || principal.CredentialID != "" {
		t.Fatalf("principal identity = %#v", principal)
	}
	if principal.OrgID != "org_123" || len(principal.RepositoryScopes) != 1 || principal.RepositoryScopes[0] != "example-org/widget-service" {
		t.Fatalf("principal scopes = %#v", principal)
	}
}

func TestWebAssertionVerifier_preservesTrustedReadPermissions(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	request.Header.Set(WebAssertionHeader, signTestWebAssertion(t, private, "current", webAssertionClaims(now, request, body)))

	// When
	principal, err := verifier.Verify(request)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if got, want := principal.Permissions, []string{ScopeContextRead, ScopeEvidenceRead}; !slices.Equal(got, want) {
		t.Fatalf("permissions = %v, want %v", got, want)
	}
}

func TestWebAssertionVerifier_authenticatesCredentialIssueOnlyForBoundApprovalRequest(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema_version":"device_approval_request.v1","user_code":"ABCDEFGH","repository_scopes":["*"]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader(body))
	claims := webAssertionClaims(now, request, body)
	claims["repository_scopes"] = []string{"*"}
	claims["permissions"] = []string{"credential:issue"}
	token := signTestWebAssertion(t, private, "current", claims)
	request.Header.Set(WebAssertionHeader, token)

	// When
	principal, err := verifier.Verify(request)

	// Then
	if err != nil {
		t.Fatalf("bound credential issue assertion was rejected: %v", err)
	}
	if got, want := principal.Permissions, []string{"credential:issue"}; !slices.Equal(got, want) {
		t.Fatalf("permissions = %v, want %v", got, want)
	}
	if got, want := principal.RepositoryScopes, []string{"*"}; !slices.Equal(got, want) {
		t.Fatalf("repository scopes = %v, want %v", got, want)
	}

	for _, changedRequest := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader([]byte(`{"device_code":"changed"}`))),
		httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval/other", bytes.NewReader(body)),
	} {
		changedRequest.Header.Set(WebAssertionHeader, token)
		if _, err := verifier.Verify(changedRequest); err == nil {
			t.Fatal("credential issue assertion was accepted for a changed request")
		}
	}
}

func TestWebAssertionVerifier_bindsFreshCredentialIssueAssertionToPreviewBody(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema_version":"device_approval_preview_request.v1","user_code":"ABCDEFGH"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader(body))
	claims := webAssertionClaims(now, request, body)
	claims["permissions"] = []string{WebAssertionPermissionCredentialIssue}
	token := signTestWebAssertion(t, private, "current", claims)
	request.Header.Set(WebAssertionHeader, token)

	// When / Then
	_, err = verifier.Verify(request)
	if err != nil {
		t.Fatalf("fresh preview assertion was rejected: %v", err)
	}
	changed := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader([]byte(`{"schema_version":"device_approval_preview_request.v1","user_code":"BCDEFGHJ"}`)))
	changedClaims := webAssertionClaims(now, request, body)
	changedClaims["permissions"] = []string{WebAssertionPermissionCredentialIssue}
	changedClaims["jti"] = "assertion_456"
	changed.Header.Set(WebAssertionHeader, signTestWebAssertion(t, private, "current", changedClaims))
	if _, err := verifier.Verify(changed); err == nil {
		t.Fatal("credential issue assertion was accepted for a changed preview body")
	}
}

func TestKnownScopes_rejectsCredentialIssueWebAssertionPermission(t *testing.T) {
	// Given
	permissions := []string{WebAssertionPermissionCredentialIssue}

	// When
	_, err := normalizeScopes(permissions)

	// Then
	if err == nil {
		t.Fatal("credential issue web assertion permission was accepted as a credential scope")
	}
}

func TestWebAssertionVerifier_rejectsInvalidClaims(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	base := webAssertionClaims(now, request, body)

	tests := []struct {
		name   string
		mutate func(map[string]any, map[string]any)
	}{
		{name: "issuer", mutate: func(claims, _ map[string]any) { claims["iss"] = "https://other.example.test" }},
		{name: "audience", mutate: func(claims, _ map[string]any) { claims["aud"] = "other" }},
		{name: "algorithm", mutate: func(_ map[string]any, header map[string]any) { header["alg"] = "HS256" }},
		{name: "unknown key", mutate: func(_ map[string]any, header map[string]any) { header["kid"] = "removed" }},
		{name: "expired", mutate: func(claims, _ map[string]any) { claims["exp"] = now.Add(-6 * time.Second).Unix() }},
		{name: "future", mutate: func(claims, _ map[string]any) { claims["nbf"] = now.Add(6 * time.Second).Unix() }},
		{name: "overlong", mutate: func(claims, _ map[string]any) { claims["exp"] = now.Add(31 * time.Second).Unix() }},
		{name: "method", mutate: func(claims, _ map[string]any) { claims["method"] = http.MethodGet }},
		{name: "path", mutate: func(claims, _ map[string]any) { claims["path"] = "/other" }},
		{name: "body", mutate: func(claims, _ map[string]any) { claims["body_sha256"] = "00" }},
		{name: "global wildcard read", mutate: func(claims, _ map[string]any) { claims["repository_scopes"] = []string{"*"} }},
		{name: "owner wildcard repository", mutate: func(claims, _ map[string]any) { claims["repository_scopes"] = []string{"example-org/*"} }},
		{name: "mixed global wildcard repository", mutate: func(claims, _ map[string]any) {
			claims["repository_scopes"] = []string{"*", "example-org/widget-service"}
		}},
		{name: "write permission", mutate: func(claims, _ map[string]any) { claims["permissions"] = []string{ScopeEpisodeWrite} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := cloneWebClaims(t, base)
			header := map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "current"}
			test.mutate(claims, header)
			request := request.Clone(request.Context())
			request.Body = ioNopCloser{bytes.NewReader(body)}
			request.Header.Set(WebAssertionHeader, signTestWebAssertionWithHeader(t, private, header, claims))

			// When
			_, err := verifier.Verify(request)

			// Then
			if err == nil {
				t.Fatal("invalid assertion was accepted")
			}
		})
	}
}

func TestWebAssertionVerifier_observesReplay(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/evidence/opaque-reference", bytes.NewReader(body))
	token := signTestWebAssertion(t, private, "current", webAssertionClaims(now, request, body))
	request.Header.Set(WebAssertionHeader, token)

	// When
	_, firstErr := verifier.Verify(request)
	_, secondErr := verifier.Verify(request)

	// Then
	if firstErr != nil || !IsWebAssertionReplay(secondErr) {
		t.Fatalf("verification errors = (%v, %v)", firstErr, secondErr)
	}
}

func webAssertionClaims(now time.Time, request *http.Request, body []byte) map[string]any {
	digest := sha256.Sum256(body)
	return map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": "org_123",
		"repository_scopes": []string{"example-org/widget-service"}, "permissions": []string{ScopeContextRead, ScopeEvidenceRead},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": "assertion_123",
		"method": request.Method, "path": request.URL.EscapedPath(), "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
}

func signTestWebAssertion(t *testing.T, key ed25519.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	return signTestWebAssertionWithHeader(t, key, map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": kid}, claims)
}

func signTestWebAssertionWithHeader(t *testing.T, key ed25519.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	encodedHeader, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	encodedClaims, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(encodedHeader) + "." + base64.RawURLEncoding.EncodeToString(encodedClaims)
	signature := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func writeTestJWKS(t *testing.T, kid string, public ed25519.PublicKey) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "kid": kid, "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(public)}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "web-assertions.jwks.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func cloneWebClaims(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type ioNopCloser struct{ *bytes.Reader }

func (ioNopCloser) Close() error { return nil }
