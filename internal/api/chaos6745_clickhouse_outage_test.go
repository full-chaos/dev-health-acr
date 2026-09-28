package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestClickHouseOutage_DoesNotBlockAuthOrReadyzOnlyDataRoutes is CHAOS-6745's
// end-to-end acceptance case, in the exact shape the ticket asks for:
// ClickHouse down -> OAuth (auth surface) routes serve, /readyz stays ready,
// and only the ClickHouse-backed data route returns the typed, retryable
// "store_unavailable" 503. Before this fix, a failing clickhouse check
// flipped ReadinessChecks (and so /readyz) to not_ready, which pulls the
// WHOLE pod from every Service's endpoints -- an unauthenticated caller
// never even reaches the auth surface to observe the difference; that
// class of regression is what this test would have caught.
func TestClickHouseOutage_DoesNotBlockAuthOrReadyzOnlyDataRoutes(t *testing.T) {
	// device_authorization requires WebAssertions configured (unrelated to
	// this ticket -- see deviceRuntimeHandler); wired here the same way
	// TestDeviceRoutes_HTTPFlowApprovesAndRedeemsOnlyOnce does, so this
	// probes the actual named auth surface (device authorization) rather
	// than a route picked only because it needs less setup.
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := newHostedTestAppWithWebAssertions(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil, verifier)
	// ClickHouse is down for every call from here on; postgres/entitlement
	// (the /readyz gate) stay healthy.
	app.dataStoreChecks = []ReadinessCheck{
		CheckFunc{CheckName: "clickhouse", Fn: func(context.Context) error { return errors.New("clickhouse: connection refused") }},
	}

	t.Run("readyz stays ready", func(t *testing.T) {
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("/readyz status = %d body=%s, want 200 (clickhouse must not gate pod readiness)", response.Code, response.Body.String())
		}
	})

	t.Run("auth surface still serves", func(t *testing.T) {
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}))
		if response.Code != http.StatusOK {
			t.Fatalf("device_authorization status = %d body=%s, want 200 (auth has no ClickHouse dependency)", response.Code, response.Body.String())
		}
	})

	t.Run("clickhouse-backed data route degrades with a typed error", func(t *testing.T) {
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, contextPacketRequest(t, app, token, hostedContextRequest()))
		assertErrorResponse(t, response, http.StatusServiceUnavailable, "store_unavailable")
	})
}
