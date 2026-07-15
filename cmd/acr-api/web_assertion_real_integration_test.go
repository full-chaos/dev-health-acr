package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestWebAssertion_realBinary(t *testing.T) {
	if os.Getenv("ACR_WEB_ASSERTION_INTEGRATION") != "1" {
		t.Skip("set ACR_WEB_ASSERTION_INTEGRATION=1 to run disposable hosted dependencies")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwksPath := writeRealBinaryJWKS(t, public)
	clickhouse := newClickHouseFixture(t, ctx)
	defer func() { _ = clickhouse.Stop(ctx) }()
	entitlement := newEntitlementFixture(t)
	defer entitlement.Stop()
	postgres := newPostgresFixture(t, ctx)
	defer func() { _ = postgres.Stop(ctx) }()
	environment := hostedProcessEnvironment(hostedEnvironmentInput{address: reserveAddress(t), postgres: postgres, clickhouse: clickhouse, entitlement: entitlement})
	environment["ACR_WEB_ASSERTION_ISSUER"] = "https://web.example.test"
	environment["ACR_WEB_ASSERTION_AUDIENCE"] = "acr-api"
	environment["ACR_WEB_ASSERTION_JWKS_FILE"] = jwksPath
	process := startAPIProcess(t, ctx, apiProcessRequest{binary: buildACRAPIBinary(t), environment: environment})
	defer func() { _ = process.Stop(ctx) }()
	baseURL := "http://" + environment["ACR_ADDR"]
	assertWebAssertionScenario(t, baseURL, private, jwksPath, os.Getenv("ACR_WEB_ASSERTION_SCENARIO"))
}

func assertWebAssertionScenario(t *testing.T, baseURL string, private ed25519.PrivateKey, jwksPath, scenario string) {
	t.Helper()
	if scenario == "" {
		scenario = "happy"
	}
	requestBody := hostedWebAssertionContextRequest()
	path := "/api/v1/agent-context/context-packets"
	wantStatus := http.StatusUnauthorized
	mutate := func(map[string]any, map[string]string) {}
	extraBearer := false
	switch scenario {
	case "happy":
		response := doWebAssertionRequest(t, baseURL, private, path, http.MethodPost, requestBody, mutate, false)
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			contents, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			t.Fatalf("happy context status = %d body=%s", response.StatusCode, contents)
		}
		var packet contractsv1.ContextPacket
		if err := json.NewDecoder(response.Body).Decode(&packet); err != nil {
			t.Fatal(err)
		}
		evidenceResponse := doWebAssertionRequest(t, baseURL, private, "/api/v1/agent-context/evidence/"+packet.Items[0].EvidenceRefIDs[0], http.MethodGet, nil, mutate, false)
		defer evidenceResponse.Body.Close()
		if evidenceResponse.StatusCode != http.StatusOK {
			t.Fatalf("happy evidence status = %d", evidenceResponse.StatusCode)
		}
		return
	case "wrong-issuer":
		mutate = func(claims map[string]any, _ map[string]string) { claims["iss"] = "https://other.example.test" }
	case "wrong-audience":
		mutate = func(claims map[string]any, _ map[string]string) { claims["aud"] = "other" }
	case "wrong-alg":
		mutate = func(_ map[string]any, header map[string]string) { header["alg"] = "HS256" }
	case "wrong-kid":
		mutate = func(_ map[string]any, header map[string]string) { header["kid"] = "removed" }
	case "expired":
		mutate = func(claims map[string]any, _ map[string]string) {
			claims["exp"] = time.Now().Add(-6 * time.Second).Unix()
		}
	case "future":
		mutate = func(claims map[string]any, _ map[string]string) {
			claims["nbf"] = time.Now().Add(6 * time.Second).Unix()
		}
	case "overlong":
		mutate = func(claims map[string]any, _ map[string]string) {
			claims["exp"] = time.Now().Add(31 * time.Second).Unix()
		}
	case "method":
		mutate = func(claims map[string]any, _ map[string]string) { claims["method"] = http.MethodGet }
	case "path":
		mutate = func(claims map[string]any, _ map[string]string) { claims["path"] = "/other" }
	case "body":
		mutate = func(claims map[string]any, _ map[string]string) { claims["body_sha256"] = "invalid" }
	case "removed-key":
		if err := os.WriteFile(jwksPath, []byte(`{"keys":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	case "foreign-scope":
		requestBody.Repository.Slug = "other-org/other-repo"
		wantStatus = http.StatusForbidden
	case "write-route":
		path, requestBody = "/api/v1/agent-context/episodes", contractsv1.ContextPacketRequest{}
	case "token-confusion":
		extraBearer = true
	default:
		t.Fatalf("unsupported ACR_WEB_ASSERTION_SCENARIO %q", scenario)
	}
	response := doWebAssertionRequest(t, baseURL, private, path, http.MethodPost, requestBody, mutate, extraBearer)
	defer response.Body.Close()
	assertSafeWebAssertionFailure(t, response, wantStatus)
}

func hostedWebAssertionContextRequest() contractsv1.ContextPacketRequest {
	return contractsv1.ContextPacketRequest{
		SchemaVersion: contractsv1.ContextPacketRequestSchema, RequestID: "web-assertion-request", Goal: "Investigate seeded CI failure",
		Repository: contractsv1.RepositoryRef{Slug: hostedIntegrationRepository}, Scope: contractsv1.RequestedScope{Branch: "main"},
		Options: contractsv1.PacketOptions{MaxItems: 10, MaxOutputTokens: 500, MaxSerializedBytes: 8192},
		Client:  contractsv1.ClientInfo{Name: "web", Version: "1.0.0", SidecarVersion: "0.1.0"},
	}
}

func doWebAssertionRequest(t *testing.T, baseURL string, private ed25519.PrivateKey, path, method string, requestBody any, mutate func(map[string]any, map[string]string), extraBearer bool) *http.Response {
	t.Helper()
	body := []byte{}
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			t.Fatal(err)
		}
		body = encoded
	}
	request, err := http.NewRequest(method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	token := signRealBinaryAssertion(t, private, request, body, mutate)
	request.Header.Set(auth.WebAssertionHeader, token)
	if extraBearer {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func signRealBinaryAssertion(t *testing.T, private ed25519.PrivateKey, request *http.Request, body []byte, mutate func(map[string]any, map[string]string)) string {
	t.Helper()
	now := time.Now().UTC()
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": hostedIntegrationOrg,
		"repository_scopes": []string{hostedIntegrationRepository}, "permissions": []string{auth.ScopeContextRead, auth.ScopeEvidenceRead},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": base64.RawURLEncoding.EncodeToString([]byte(now.Format(time.RFC3339Nano))),
		"method": request.Method, "path": request.URL.EscapedPath(), "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	header := map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"}
	mutate(claims, header)
	encodedHeader, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	encodedClaims, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(encodedHeader) + "." + base64.RawURLEncoding.EncodeToString(encodedClaims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
}

func assertSafeWebAssertionFailure(t *testing.T, response *http.Response, wantStatus int) {
	t.Helper()
	var envelope contractsv1.ErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus || envelope.Error.HTTPStatus != wantStatus || envelope.Error.Code == "" || strings.Contains(envelope.Error.Message, "assertion") {
		t.Fatalf("web assertion failure = status:%d envelope:%#v", response.StatusCode, envelope)
	}
}

func writeRealBinaryJWKS(t *testing.T, public ed25519.PublicKey) string {
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
