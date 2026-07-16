package auth

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

type delayedUsageCredentialStore struct {
	storage.CredentialStore

	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *delayedUsageCredentialStore) TouchLastUsed(ctx context.Context, credentialID, ip, userAgent string, usedAt time.Time) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.CredentialStore.TouchLastUsed(ctx, credentialID, ip, userAgent, usedAt)
	case <-ctx.Done():
		return ctx.Err()
	}
}

type unavailableDenialAuditStore struct {
	storage.AuditStore

	calls atomic.Int64
}

func (s *unavailableDenialAuditStore) Record(ctx context.Context, event storage.AuditEvent) error {
	if event.Status == "denied" {
		s.calls.Add(1)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestAuthenticator_successfulRead_performs_no_synchronous_usage_or_audit_write(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	baseStore := newMemoryCredentialStore(t)
	audit := memory.NewAuditStore()
	issued := issueForMiddleware(t, baseStore, audit, now, []string{ScopeContextRead}, []string{"owner/repository"}, nil)
	store := &delayedUsageCredentialStore{CredentialStore: baseStore, started: make(chan struct{}), release: make(chan struct{})}
	telemetry, err := NewUsageTelemetry(store, audit, UsageTelemetryOptions{QueueCapacity: 1, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = telemetry.Close() })
	authenticator, err := NewAuthenticator(store, audit, AuthenticatorOptions{Now: func() time.Time { return now }, Limiter: NoopLimiter{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), UsageTelemetry: telemetry})
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Middleware(authenticator.RequireScope(ScopeContextRead, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+issued.Token)

	// When
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusNoContent {
		t.Fatalf("response status = %d", response.Code)
	}
	select {
	case <-store.started:
		t.Fatal("successful request synchronously wrote credential usage")
	default:
	}
	if len(audit.Events()) != 0 {
		t.Fatalf("successful request synchronously wrote audit events: %#v", audit.Events())
	}
	flushContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	flushResult := make(chan error, 1)
	go func() { flushResult <- telemetry.Flush(flushContext) }()
	select {
	case <-store.started:
	case <-flushContext.Done():
		t.Fatal("telemetry worker did not begin its asynchronous write")
	}
	close(store.release)
	if err := <-flushResult; err != nil {
		t.Fatal(err)
	}
	if len(audit.Events()) != 1 {
		t.Fatalf("asynchronous telemetry audit count = %d, want 1", len(audit.Events()))
	}
}

func TestAuthenticator_denial_attempts_one_bounded_audit_delivery_and_preserves_denial(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	baseStore := newMemoryCredentialStore(t)
	issued := issueForMiddleware(t, baseStore, memory.NewAuditStore(), now, []string{ScopeEvidenceRead}, []string{"owner/repository"}, nil)
	audit := &unavailableDenialAuditStore{}
	var logs bytes.Buffer
	telemetry, err := NewUsageTelemetry(baseStore, audit, UsageTelemetryOptions{QueueCapacity: 1, FlushInterval: time.Hour, DeliveryTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = telemetry.Close() })
	authenticator, err := NewAuthenticator(baseStore, audit, AuthenticatorOptions{
		Now: func() time.Time { return now }, Limiter: NoopLimiter{}, DetachedTimeout: 20 * time.Millisecond,
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)), UsageTelemetry: telemetry,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Middleware(authenticator.RequireScope(ScopeContextRead, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("denied request reached terminal handler")
	})))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+issued.Token)

	// When
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	// Then
	assertContractError(t, response, http.StatusForbidden, "insufficient_scope")
	if calls := audit.calls.Load(); calls != 1 {
		t.Fatalf("denial audit delivery attempts = %d, want 1", calls)
	}
	if stats := telemetry.Stats(); stats.Enqueued != 0 {
		t.Fatalf("denied request enqueued successful-use telemetry: %#v", stats)
	}
	if output := logs.String(); !strings.Contains(output, "denial_audit_delivery") || strings.Contains(output, "persisted") {
		t.Fatalf("denial audit observability = %q", output)
	}
}
