package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestAuthenticatorReturnsRetryableErrorForCredentialStoreFailure(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	store := &failingCredentialStore{CredentialStore: memory.NewCredentialStore(), err: errors.New("database unavailable")}
	authenticator := newTestAuthenticator(t, store, memory.NewAuditStore(), now, NoopLimiter{})
	handler := authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("failed credential lookup reached handler")
	}))
	raw := make([]byte, tokenSecretBytes)
	for index := range raw {
		raw[index] = 42
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+TokenPrefix+base64.RawURLEncoding.EncodeToString(raw))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertContractError(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Error.Retryable {
		t.Fatal("credential store outage must be retryable")
	}
}

func TestAuthenticatorDoesNotLogCredentialStoreFailureDetails(t *testing.T) {
	secret := "postgres://operator:credential-secret@example"
	store := &failingCredentialStore{CredentialStore: memory.NewCredentialStore(), err: errors.New(secret)}
	buffer := &bytes.Buffer{}
	authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{
		Now: func() time.Time { return time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC) }, Limiter: NoopLimiter{}, Logger: slog.New(slog.NewJSONHandler(buffer, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, tokenSecretBytes)
	for index := range raw {
		raw[index] = 42
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+TokenPrefix+base64.RawURLEncoding.EncodeToString(raw))
	response := httptest.NewRecorder()
	authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("failed lookup reached handler") })).ServeHTTP(response, request)
	assertContractError(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
	if strings.Contains(buffer.String(), secret) {
		t.Fatalf("log leaked credential store failure: %s", buffer.String())
	}
}

func TestAuthenticatorTreatsExactExpiryAsExpired(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	credentialStore := memory.NewCredentialStore()
	auditStore := memory.NewAuditStore()
	issued := issueForMiddleware(t, credentialStore, auditStore, now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, &now)
	authenticator := newTestAuthenticator(t, credentialStore, auditStore, now, NoopLimiter{})
	handler := authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("expired token reached handler") }))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+issued.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertContractError(t, response, http.StatusUnauthorized, "invalid_token")
}

func TestAuthenticatorRateLimitsBeforeCredentialLookup(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	store := &countingCredentialStore{CredentialStore: memory.NewCredentialStore()}
	authenticator := newTestAuthenticator(t, store, memory.NewAuditStore(), now, NewMemoryLimiter(time.Minute, 1, 5))
	handler := authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "192.0.2.30:1234"
		return r
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request())
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected first status: %d", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request())
	assertContractError(t, second, http.StatusTooManyRequests, "rate_limited")
	if second.Header().Get("Retry-After") != "60" {
		t.Fatalf("retry-after = %q", second.Header().Get("Retry-After"))
	}
	if store.lookups != 0 {
		t.Fatalf("malformed and rate-limited attempts should not hit credential storage: %d", store.lookups)
	}
}
