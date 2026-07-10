package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestAuthenticatorAllowsAuthorizedReadAndTracksUsage(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	credentialStore := memory.NewCredentialStore()
	auditStore := memory.NewAuditStore()
	issued := issueForMiddleware(t, credentialStore, auditStore, now, []string{ScopeContextRead, ScopeEvidenceRead}, []string{"full-chaos/dev-health-acr"}, nil)
	authenticator := newTestAuthenticator(t, credentialStore, auditStore, now, NewMemoryLimiter(time.Minute, 10, 5))

	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.OrgID != "org_1" || principal.CredentialID != issued.Credential.CredentialID {
			t.Fatalf("unexpected principal: %#v %v", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := authenticator.Middleware(authenticator.RequireScope(ScopeContextRead,
		authenticator.RequireRepository(func(r *http.Request) string { return r.Header.Get("X-Repo") }, terminal)))

	request := httptest.NewRequest(http.MethodPost, "/context", nil)
	request.RemoteAddr = "192.0.2.10:4242"
	request.Header.Set("Authorization", "Bearer "+issued.Token)
	request.Header.Set("X-Repo", "FULL-CHAOS/dev-health-acr")
	request.Header.Set("X-Request-ID", "req_auth")
	request.Header.Set("User-Agent", "acr-mcp/test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	record, ok := credentialStore.RecordForTest(issued.Credential.CredentialID)
	if !ok || record.Metadata.LastUsedAt == nil || !record.Metadata.LastUsedAt.Equal(now) || record.LastUsedIP != "192.0.2.10" {
		t.Fatalf("last-used metadata not updated: %#v", record)
	}
	if record.LastUsedUserAgent != "acr-mcp/test" {
		t.Fatalf("user agent not recorded: %q", record.LastUsedUserAgent)
	}
	if !hasAuditAction(auditStore.Events(), "credential_used") {
		t.Fatal("successful use was not audited")
	}
}

func TestAuthenticatorDeniesIndependentScopeAndRepository(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	credentialStore := memory.NewCredentialStore()
	auditStore := memory.NewAuditStore()
	issued := issueForMiddleware(t, credentialStore, auditStore, now, []string{ScopeEpisodeWrite}, []string{"owner/allowed"}, nil)
	authenticator := newTestAuthenticator(t, credentialStore, auditStore, now, NoopLimiter{})

	tests := []struct {
		name   string
		scope  string
		repo   string
		status int
		code   string
	}{
		{name: "write does not imply read", scope: ScopeContextRead, repo: "owner/allowed", status: http.StatusForbidden, code: "insufficient_scope"},
		{name: "cross repository", scope: ScopeEpisodeWrite, repo: "owner/denied", status: http.StatusForbidden, code: "repo_forbidden"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := authenticator.Middleware(authenticator.RequireScope(test.scope,
				authenticator.RequireRepository(func(r *http.Request) string { return r.Header.Get("X-Repo") }, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					t.Fatal("denied request reached terminal handler")
				}))))
			request := httptest.NewRequest(http.MethodPost, "/resource", nil)
			request.Header.Set("Authorization", "Bearer "+issued.Token)
			request.Header.Set("X-Repo", test.repo)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertContractError(t, response, test.status, test.code)
		})
	}
	if !hasAuditAction(auditStore.Events(), "scope_denied") || !hasAuditAction(auditStore.Events(), "repository_denied") {
		t.Fatalf("denials were not audited: %#v", auditStore.Events())
	}
}

func TestAuthenticatorRejectsUnknownRevokedAndExpiredTokensWithoutLeakingReason(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		configure func(*time.Time) *time.Time
		revoke    bool
		unknown   bool
	}{
		{name: "unknown", unknown: true},
		{name: "expired", configure: func(now *time.Time) *time.Time { value := now.Add(-time.Second); return &value }},
		{name: "revoked", revoke: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentialStore := memory.NewCredentialStore()
			auditStore := memory.NewAuditStore()
			var expires *time.Time
			if test.configure != nil {
				expires = test.configure(&now)
			}
			issued := issueForMiddleware(t, credentialStore, auditStore, now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, expires)
			if test.revoke {
				if _, err := credentialStore.Revoke(context.Background(), "org_1", issued.Credential.CredentialID, now.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			if test.unknown {
				bytes := make([]byte, tokenSecretBytes)
				for index := range bytes {
					bytes[index] = 99
				}
				issued.Token = TokenPrefix + base64.RawURLEncoding.EncodeToString(bytes)
			}
			authenticator := newTestAuthenticator(t, credentialStore, auditStore, now, NoopLimiter{})
			handler := authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid token reached handler") }))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", "Bearer "+issued.Token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertContractError(t, response, http.StatusUnauthorized, "invalid_token")
			if strings.Contains(response.Body.String(), issued.Credential.CredentialID) ||
				(test.name != "unknown" && strings.Contains(response.Body.String(), test.name)) {
				t.Fatalf("authentication response leaked reason or id: %s", response.Body.String())
			}
		})
	}
}

func TestAuthenticatorReturnsRetryableErrorForCredentialStoreFailure(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	store := &failingCredentialStore{CredentialStore: memory.NewCredentialStore(), err: errors.New("database unavailable")}
	authenticator := newTestAuthenticator(t, store, memory.NewAuditStore(), now, NoopLimiter{})
	handler := authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("failed credential lookup reached handler")
	}))
	bytes := make([]byte, tokenSecretBytes)
	for index := range bytes {
		bytes[index] = 42
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+TokenPrefix+base64.RawURLEncoding.EncodeToString(bytes))
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

func TestAuthenticatorTreatsExactExpiryAsExpired(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	credentialStore := memory.NewCredentialStore()
	auditStore := memory.NewAuditStore()
	issued := issueForMiddleware(t, credentialStore, auditStore, now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, &now)
	authenticator := newTestAuthenticator(t, credentialStore, auditStore, now, NoopLimiter{})
	handler := authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("expired token reached handler")
	}))
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
	if store.lookups != 0 {
		t.Fatalf("malformed and rate-limited attempts should not hit credential storage: %d", store.lookups)
	}
}

func issueForMiddleware(t *testing.T, store storage.CredentialStore, audit storage.AuditStore, now time.Time, scopes, repositories []string, expires *time.Time) IssuedCredential {
	t.Helper()
	service, err := NewService(store, audit, ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Create(context.Background(), CreateCredentialRequest{
		OrgID: "org_1", Name: "test", RepositoryScopes: repositories, Scopes: scopes, ExpiresAt: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func newTestAuthenticator(t *testing.T, store storage.CredentialStore, audit storage.AuditStore, now time.Time, limiter AttemptLimiter) *Authenticator {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	authenticator, err := NewAuthenticator(store, audit, AuthenticatorOptions{Now: func() time.Time { return now }, Limiter: limiter, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

func assertContractError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != contractsv1.ErrorSchema || envelope.Error.Code != code || envelope.Error.HTTPStatus != status {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}

func hasAuditAction(events []storage.AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}

type countingCredentialStore struct {
	storage.CredentialStore
	lookups int
}

func (s *countingCredentialStore) FindByTokenHash(ctx context.Context, tokenHash string) (contractsv1.ClientCredential, error) {
	s.lookups++
	return s.CredentialStore.FindByTokenHash(ctx, tokenHash)
}

type failingCredentialStore struct {
	storage.CredentialStore
	err error
}

func (s *failingCredentialStore) FindByTokenHash(context.Context, string) (contractsv1.ClientCredential, error) {
	return contractsv1.ClientCredential{}, s.err
}
