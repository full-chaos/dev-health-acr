package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestAuthenticator_webAssertionReadOnlyScopeAndRepositoryAuthorization(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	audit := memory.NewAuditStore()
	authenticator, err := NewAuthenticator(newMemoryCredentialStore(t), audit, AuthenticatorOptions{
		Now: func() time.Time { return now }, Limiter: NoopLimiter{}, WebAssertions: verifier,
	})
	if err != nil {
		t.Fatal(err)
	}
	read := authenticator.MiddlewareFor(true, authenticator.RequireScope(ScopeContextRead,
		authenticator.RequireRepository(func(*http.Request) string { return "example-org/widget-service" }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok || principal.AuthenticationMethod != storage.AuthenticationMethodWebAssertion || principal.CredentialID != "" {
				t.Fatalf("web principal = %#v, %t", principal, ok)
			}
			w.WriteHeader(http.StatusNoContent)
		}))))
	body := []byte(`{}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	request.Header.Set(WebAssertionHeader, signTestWebAssertion(t, private, "current", webAssertionClaims(now, request, body)))

	// When
	response := httptest.NewRecorder()
	read.ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusNoContent {
		t.Fatalf("read status = %d body=%s", response.Code, response.Body.String())
	}
	if !hasAuditAction(audit.Events(), "web_assertion_used") {
		t.Fatalf("web assertion use was not audited: %#v", audit.Events())
	}
}

func TestAuthenticator_webAssertionsCannotAuthenticateWriteRoutes(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(newMemoryCredentialStore(t), nil, AuthenticatorOptions{Now: func() time.Time { return now }, Limiter: NoopLimiter{}, WebAssertions: verifier})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/episodes", bytes.NewReader(body))
	claims := webAssertionClaims(now, request, body)
	claims["jti"] = "write_assertion"
	request.Header.Set(WebAssertionHeader, signTestWebAssertion(t, private, "current", claims))

	// When
	response := httptest.NewRecorder()
	authenticator.MiddlewareFor(false, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("write route authorized web assertion") })).ServeHTTP(response, request)

	// Then
	assertContractError(t, response, http.StatusUnauthorized, "invalid_token")
}
