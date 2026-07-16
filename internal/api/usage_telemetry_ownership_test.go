package api

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestNewApp_borrows_injected_usage_telemetry_for_primary_authenticator(t *testing.T) {
	// Given
	store, err := memory.NewCredentialStore()
	if err != nil {
		t.Fatal(err)
	}
	telemetry, err := auth.NewUsageTelemetry(store, memory.NewAuditStore(), auth.UsageTelemetryOptions{FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = telemetry.Close() })

	// When
	app, _ := newHostedTestAppWithUsageTelemetry(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil, nil, telemetry)

	// Then
	if app.usageTelemetry != telemetry || app.authenticator == nil || app.authenticator.UsageTelemetry() != telemetry {
		t.Fatalf("primary usage telemetry = (%p, %p), want injected %p", app.usageTelemetry, app.authenticator.UsageTelemetry(), telemetry)
	}
}
