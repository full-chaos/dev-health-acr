package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type auditOutageStore struct{ denialAttempts atomic.Int64 }

func (s *auditOutageStore) Record(ctx context.Context, event storage.AuditEvent) error {
	if event.Status == "denied" {
		s.denialAttempts.Add(1)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestCredentialTelemetryHTTPDriver_success_and_denial_during_audit_outage(t *testing.T) {
	// Given
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	outage := &auditOutageStore{}
	telemetry, err := auth.NewUsageTelemetry(app.runtime.Credentials, outage, auth.UsageTelemetryOptions{
		QueueCapacity: 4, FlushInterval: time.Hour, DeliveryTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = telemetry.Close() })
	authenticator, err := auth.NewAuthenticator(app.runtime.Credentials, outage, auth.AuthenticatorOptions{
		Now: app.now, Limiter: auth.NoopLimiter{}, DetachedTimeout: 20 * time.Millisecond,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), UsageTelemetry: telemetry,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.runtime.Audit = outage
	app.authenticator = authenticator
	app.usageTelemetry = telemetry
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	// When
	success, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/agent-context/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	success.Header.Set("Authorization", "Bearer "+token)
	success.Header.Set("X-ACR-Client-Version", "0.1.0")
	successResponse, err := server.Client().Do(success)
	if err != nil {
		t.Fatal(err)
	}
	successResponse.Body.Close()
	denied, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/agent-context/evidence/evidence_12345678", nil)
	if err != nil {
		t.Fatal(err)
	}
	denied.Header.Set("Authorization", "Bearer "+token)
	denied.Header.Set("X-ACR-Client-Version", "0.1.0")
	deniedResponse, err := server.Client().Do(denied)
	if err != nil {
		t.Fatal(err)
	}
	deniedResponse.Body.Close()

	// Then
	if successResponse.StatusCode != http.StatusOK || deniedResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("HTTP statuses = %d, %d; want %d, %d", successResponse.StatusCode, deniedResponse.StatusCode, http.StatusOK, http.StatusForbidden)
	}
	if attempts := outage.denialAttempts.Load(); attempts != 1 {
		t.Fatalf("denial audit delivery attempts = %d, want 1", attempts)
	}
}
