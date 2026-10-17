package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDevelopmentStub_protected_routes_fail_closed_without_runtime(t *testing.T) {
	// Given
	app := testApp(t)
	routes := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/agent-context/capabilities"},
		{method: http.MethodPost, path: "/api/v1/agent-context/context-packets"},
		{method: http.MethodGet, path: "/api/v1/agent-context/evidence/ev1_characterization"},
		{method: http.MethodPost, path: "/api/v1/agent-context/episodes"},
		{method: http.MethodPost, path: "/api/v1/context-fabric/investigations"},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))

			// Then
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestDevelopmentStub_readiness_reports_configuration_and_runtime_dependency(t *testing.T) {
	// Given
	app := testApp(t,
		CheckFunc{CheckName: "configuration", Fn: func(context.Context) error { return nil }},
		CheckFunc{CheckName: "runtime_dependencies", Fn: func(context.Context) error { return errors.New("not configured") }},
	)

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	// Then
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	var body readinessResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Checks) != 2 || body.Checks[0].Name != "configuration" || body.Checks[1].Name != "runtime_dependencies" {
		t.Fatalf("checks = %#v, want configuration and runtime_dependencies", body.Checks)
	}
}

func TestUnauthenticatedRuntimeHandler_failsClosedWithoutRuntimeAndLeavesHealthRoutesIndependent(t *testing.T) {
	// Given
	app := testApp(t)
	called := false
	handler := app.unauthenticatedRuntimeHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	// When
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/unregistered-device-route", nil))
	health := httptest.NewRecorder()
	app.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	// Then
	if response.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("runtime handler status = %d called = %t", response.Code, called)
	}
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", health.Code, http.StatusOK)
	}
}

func TestUnauthenticatedRuntimeHandler_runsNextWhenRuntimeIsConfigured(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	handler := app.unauthenticatedRuntimeHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	// When
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/unregistered-device-route", nil))

	// Then
	if response.Code != http.StatusNoContent {
		t.Fatalf("runtime handler status = %d, want %d", response.Code, http.StatusNoContent)
	}
}
