package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestAppCorrelatesRequestObservationWithCallerRequestID(t *testing.T) {
	// Given
	sink := &snapshotSink{}
	hooks := observability.NewHooks(sink, nil)
	app, token := newHostedTestApp(t, nil, &hooks, []string{auth.ScopeContextRead}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	requestID := "req_0123456789abcdef0123456789abcdef"
	request.Header.Set("X-Request-ID", requestID)

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	snapshot := sink.only(t)
	if snapshot.Kind != observability.KindRequest || snapshot.Operation != observability.OperationCapabilities || snapshot.HTTPStatusClass != observability.HTTPStatus2xx || snapshot.Outcome != observability.OutcomeSuccess || snapshot.Denial != observability.DenialNone || snapshot.RequestID != observability.RequestID(requestID) {
		t.Fatalf("unexpected observation: %#v", snapshot)
	}
}

func TestAppObservesRecoveredPanic(t *testing.T) {
	// Given
	sink := &snapshotSink{}
	hooks := observability.NewHooks(sink, nil)
	buffer := &bytes.Buffer{}
	secret := "evidence-and-transcript-sentinel"
	app, token := newHostedTestApp(t, panicCapabilitiesProvider{value: secret}, &hooks, []string{auth.ScopeContextRead}, nil, nil)
	app.logger = testLogger(buffer)
	requestID := "req_0123456789abcdef0123456789abcdef"
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Request-ID", requestID)

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	snapshot := sink.only(t)
	if snapshot.Kind != observability.KindRequest || snapshot.Outcome != observability.OutcomeFailure || snapshot.RequestID != observability.RequestID(requestID) {
		t.Fatalf("unexpected observation: %#v", snapshot)
	}
	if strings.Contains(buffer.String(), secret) {
		t.Fatalf("panic log leaked raw data: %s", buffer.String())
	}
}

func TestInstrumentedHandlerClassifiesScopeRepositoryAndRateLimitDenials(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	credentials := memory.NewCredentialStore()
	audit := memory.NewAuditStore()
	allowedToken := issueScopedCredential(t, credentials, audit, now, []string{auth.ScopeEvidenceRead}, []string{"owner/repository"})
	noEvidenceScopeToken := issueScopedCredential(t, credentials, audit, now, []string{auth.ScopeContextRead}, []string{"owner/repository"})
	authenticator, err := auth.NewAuthenticator(credentials, audit, auth.AuthenticatorOptions{Now: func() time.Time { return now }, Limiter: auth.NoopLimiter{}, Logger: testLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, Policies: limits.PolicySet{Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	sink := &snapshotSink{}
	hooks := observability.NewHooks(sink, nil)
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities:  StaticCapabilitiesProvider{Value: contractsv1.Capabilities{SchemaVersion: contractsv1.CapabilitiesSchema}},
		Observability: &hooks,
		Limits:        manager,
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	terminal := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	protected := authenticator.RequireRepository(func(r *http.Request) string { return r.Header.Get("X-Repo") },
		app.ProtectedHandler(limits.RequestClassEvidence, terminal))
	handler := app.InstrumentedHandler(authenticator.Middleware(authenticator.RequireScope(auth.ScopeEvidenceRead, protected)))
	request := func(token, repository string) *http.Request {
		value := httptest.NewRequest(http.MethodGet, "/evidence", nil)
		value.Header.Set("Authorization", "Bearer "+token)
		value.Header.Set("X-Request-ID", "req_0123456789abcdef0123456789abcdef")
		value.Header.Set("X-Repo", repository)
		return value
	}

	// When
	scopeDenied := httptest.NewRecorder()
	handler.ServeHTTP(scopeDenied, request(noEvidenceScopeToken, "owner/repository"))
	repoDenied := httptest.NewRecorder()
	handler.ServeHTTP(repoDenied, request(allowedToken, "owner/denied"))
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, request(allowedToken, "owner/repository"))
	rateLimited := httptest.NewRecorder()
	handler.ServeHTTP(rateLimited, request(allowedToken, "owner/repository"))

	// Then
	if scopeDenied.Code != http.StatusForbidden || repoDenied.Code != http.StatusForbidden || allowed.Code != http.StatusNoContent || rateLimited.Code != http.StatusTooManyRequests {
		t.Fatalf("unexpected statuses: scope=%d repo=%d allowed=%d rate=%d", scopeDenied.Code, repoDenied.Code, allowed.Code, rateLimited.Code)
	}
	snapshots := sink.all(t)
	if len(snapshots) != 4 {
		t.Fatalf("snapshot count = %d", len(snapshots))
	}
	if snapshots[0].Denial != observability.DenialPermissionScope {
		t.Fatalf("scope denial = %#v", snapshots[0])
	}
	if snapshots[1].Denial != observability.DenialRepositoryScope {
		t.Fatalf("repository denial = %#v", snapshots[1])
	}
	if snapshots[2].Outcome != observability.OutcomeSuccess {
		t.Fatalf("allowed outcome = %#v", snapshots[2])
	}
	if snapshots[3].Denial != observability.DenialRateLimit {
		t.Fatalf("rate limit denial = %#v", snapshots[3])
	}
}

func issueScopedCredential(t *testing.T, credentials *memory.CredentialStore, audit *memory.AuditStore, now time.Time, scopes, repositories []string) string {
	t.Helper()
	service, err := auth.NewService(credentials, audit, auth.ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: "org_1", Name: "scoped credential", RepositoryScopes: repositories, Scopes: scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Token
}

type panicCapabilitiesProvider struct{ value string }

func (p panicCapabilitiesProvider) Capabilities(context.Context, *http.Request) (contractsv1.Capabilities, error) {
	panic(p.value)
}

type snapshotSink struct {
	mu        sync.Mutex
	snapshots []observability.SupportSnapshot
}

func (s *snapshotSink) Record(snapshot observability.SupportSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, snapshot)
}

func (s *snapshotSink) only(t *testing.T) observability.SupportSnapshot {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.snapshots) != 1 {
		t.Fatalf("snapshot count = %d", len(s.snapshots))
	}
	return s.snapshots[0]
}

func (s *snapshotSink) all(t *testing.T) []observability.SupportSnapshot {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]observability.SupportSnapshot(nil), s.snapshots...)
}
