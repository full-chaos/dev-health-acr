package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestLimitMiddlewareReturnsCorrelatedRateLimitDenial(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	credentials := memory.NewCredentialStore()
	audit := memory.NewAuditStore()
	issued := issueCredential(t, credentials, audit, now)
	authenticator, err := auth.NewAuthenticator(credentials, audit, auth.AuthenticatorOptions{
		Now:     func() time.Time { return now },
		Limiter: auth.NoopLimiter{},
		Logger:  slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := limits.NewManager(limits.Options{
		Now: func() time.Time { return now },
		Policies: limits.PolicySet{Context: limits.ContextPolicy{
			Window: time.Minute, PerOrgLimit: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := testApp(t)
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := CompleteUsage(r.Context(), limits.ResourceUsage{Items: 2, Tokens: 3, Bytes: 5}); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := app.requestIDMiddleware(authenticator.Middleware(LimitMiddleware(manager, limits.RequestClassContext, terminal)))
	request := func(requestID string) *http.Request {
		value := httptest.NewRequest(http.MethodPost, "/agent-context", nil)
		value.Header.Set("Authorization", "Bearer "+issued)
		value.Header.Set("X-Request-ID", requestID)
		return value
	}

	// When
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request("req_11111111111111111111111111111111"))
	denied := httptest.NewRecorder()
	requestID := "req_22222222222222222222222222222222"
	handler.ServeHTTP(denied, request(requestID))

	// Then
	if first.Code != http.StatusNoContent {
		t.Fatalf("first request status = %d", first.Code)
	}
	if denied.Code != http.StatusTooManyRequests {
		t.Fatalf("denied request status = %d body=%s", denied.Code, denied.Body.String())
	}
	if denied.Header().Get("X-Request-ID") != requestID {
		t.Fatalf("response request ID = %q", denied.Header().Get("X-Request-ID"))
	}
	if denied.Header().Get("Retry-After") != "60" {
		t.Fatalf("retry-after = %q", denied.Header().Get("Retry-After"))
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(denied.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.RequestID != requestID || envelope.Error.Code != "rate_limited" || !envelope.Error.Retryable {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}

func TestWriteRateLimitErrorRoundsRetryUp(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.WithValue(context.Background(), requestIDContextKey, "req_0123456789abcdef0123456789abcdef"))
	response := httptest.NewRecorder()

	writeRateLimitError(response, request, 1100*time.Millisecond)

	if response.Header().Get("Retry-After") != "2" {
		t.Fatalf("retry-after = %q", response.Header().Get("Retry-After"))
	}
}

func TestAppProtectedHandlerUsesInjectedLimitManager(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager, err := limits.NewManager(limits.Options{
		Now: func() time.Time { return now },
		Policies: limits.PolicySet{Evidence: limits.EvidencePolicy{
			Window: time.Minute, PerCredentialLimit: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Value: contractsv1.Capabilities{SchemaVersion: contractsv1.CapabilitiesSchema}},
		Limits:       manager,
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	credentials := memory.NewCredentialStore()
	audit := memory.NewAuditStore()
	token := issueCredential(t, credentials, audit, now)
	authenticator, err := auth.NewAuthenticator(credentials, audit, auth.AuthenticatorOptions{Now: func() time.Time { return now }, Limiter: auth.NoopLimiter{}, Logger: testLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatal(err)
	}
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := CompleteUsage(r.Context(), limits.ResourceUsage{Items: 2, Tokens: 3, Bytes: 5}); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := app.requestIDMiddleware(authenticator.Middleware(app.ProtectedHandler(limits.RequestClassEvidence, terminal)))
	request := func() *http.Request {
		value := httptest.NewRequest(http.MethodGet, "/evidence", nil)
		value.Header.Set("Authorization", "Bearer "+token)
		return value
	}

	// When
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request())
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, request())

	// Then
	if first.Code != http.StatusNoContent {
		t.Fatalf("first status = %d", first.Code)
	}
	if denied.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", denied.Code, denied.Body.String())
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(denied.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "rate_limited" {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	usage, err := manager.Usage(limits.Subject{OrgID: "org_1", CredentialID: "probe_credential"}, limits.RequestClassEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Org.Admitted != 1 || usage.Org.Completed != 1 || usage.Org.Denied != 1 || usage.Org.Items != 2 || usage.Org.Tokens != 3 || usage.Org.Bytes != 5 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
}

func TestAppAuthenticatedHandlerUsesInjectedAttemptLimiter(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	credentials := memory.NewCredentialStore()
	audit := memory.NewAuditStore()
	token := issueCredential(t, credentials, audit, now)
	manager, err := limits.NewManager(limits.Options{Policies: limits.PolicySet{Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Value: contractsv1.Capabilities{SchemaVersion: contractsv1.CapabilitiesSchema}},
		Limits:       manager,
		AuthAttempts: auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{Window: time.Minute, AttemptLimit: 1, FailureLimit: 1, MaxTrackedKeys: 2}),
		Now:          func() time.Time { return now },
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.AuthenticatedHandler(credentials, audit, limits.RequestClassEvidence, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := func() *http.Request {
		value := httptest.NewRequest(http.MethodGet, "/evidence", nil)
		value.Header.Set("Authorization", "Bearer "+token)
		value.Header.Set("X-Request-ID", "req_0123456789abcdef0123456789abcdef")
		return value
	}

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request())
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request())

	if first.Code != http.StatusNoContent || second.Code != http.StatusTooManyRequests {
		t.Fatalf("statuses = %d, %d", first.Code, second.Code)
	}
}

func issueCredential(t *testing.T, credentials *memory.CredentialStore, audit *memory.AuditStore, now time.Time) string {
	t.Helper()
	service, err := auth.NewService(credentials, audit, auth.ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: "org_1", Name: "limit middleware", RepositoryScopes: []string{"owner/repository"}, Scopes: []string{auth.ScopeContextRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Token
}
