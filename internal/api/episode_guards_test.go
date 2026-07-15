package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestCreateEpisodeEnforcesScopeEntitlementAndRepository(t *testing.T) {
	tests := []struct {
		name         string
		scopes       []string
		entitlements EntitlementProvider
		repository   string
		status       int
	}{
		{name: "scope", scopes: []string{auth.ScopeContextRead}, repository: hostedTestRepository, status: http.StatusForbidden},
		{name: "entitlement", scopes: []string{auth.ScopeEpisodeWrite}, entitlements: EntitlementFunc(func(context.Context, string, string) (bool, error) { return false, nil }), repository: hostedTestRepository, status: http.StatusForbidden},
		{name: "repository", scopes: []string{auth.ScopeEpisodeWrite}, repository: "example-org/other", status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			creator := &fakeEpisodeCreator{episode: storedEpisode()}
			app, token := hostedEpisodeTestApp(t, creator, test.scopes, test.entitlements)
			create := episodeCreate()
			create.Repository.Slug = test.repository

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, create, "idempotency_01"))

			// Then
			if response.Code != test.status || creator.create.ClientEpisodeID != "" {
				t.Fatalf("status=%d creator=%#v", response.Code, creator.create)
			}
		})
	}
}

func TestCreateEpisodeEnforcesBodyAndRateLimits(t *testing.T) {
	// Given
	creator := &fakeEpisodeCreator{episode: storedEpisode()}
	app, token := hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
	app.config.MaxRequestBodyBytes = 1

	// When
	overseized := httptest.NewRecorder()
	app.Handler().ServeHTTP(overseized, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	// Then
	if overseized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d", overseized.Code)
	}

	// Given
	app, token = hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) }, Policies: limits.PolicySet{Episode: limits.EpisodePolicy{Window: time.Minute, PerOrgLimit: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	app.limits = manager

	// When
	first, second := httptest.NewRecorder(), httptest.NewRecorder()
	app.Handler().ServeHTTP(first, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))
	app.Handler().ServeHTTP(second, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	// Then
	if first.Code != http.StatusCreated || second.Code != http.StatusTooManyRequests {
		t.Fatalf("statuses = %d, %d", first.Code, second.Code)
	}
}

func TestCreateEpisodeRecordsUsageAuditAndOperation(t *testing.T) {
	// Given
	sink := &snapshotSink{}
	hooks := observability.NewHooks(sink, nil)
	creator := &fakeEpisodeCreator{episode: storedEpisode()}
	app, token := hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
	app.observability = hooks
	audit := &episodeAuditStore{}
	app.runtime.Audit = audit
	telemetry, err := auth.NewUsageTelemetry(app.runtime.Credentials, audit, auth.UsageTelemetryOptions{QueueCapacity: 1, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	app.usageTelemetry = telemetry
	t.Cleanup(func() { _ = telemetry.Close() })
	request := authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01")
	request.Header.Set("X-Request-ID", "req_0123456789abcdef0123456789abcdef")

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	flushContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := telemetry.Flush(flushContext); err != nil {
		t.Fatal(err)
	}

	// Then
	if response.Code != http.StatusCreated || response.Header().Get("X-Request-ID") != request.Header.Get("X-Request-ID") || !audit.recorded {
		t.Fatalf("status=%d request_id=%q audit=%t", response.Code, response.Header().Get("X-Request-ID"), audit.recorded)
	}
	if observation := sink.only(t); observation.Operation != observability.OperationEpisode || observation.HTTPStatusClass != observability.HTTPStatus2xx {
		t.Fatalf("observation = %#v", observation)
	}
}

type episodeAuditStore struct{ recorded bool }

func (s *episodeAuditStore) Record(_ context.Context, event storage.AuditEvent) error {
	s.recorded = event.Action == "episode_recorded" && event.Metadata["request_bytes"] != nil && event.Metadata["response_bytes"] != nil
	return nil
}
