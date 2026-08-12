package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
)

func TestReadinessHandlerHealthz(t *testing.T) {
	handler := readinessHandler("1.0.0", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ok" || body.Service != "acr-projector" {
		t.Fatalf("body = %+v", body)
	}
}

func TestReadinessHandlerReadyzWithoutChecksReportsDisabledButReady(t *testing.T) {
	handler := readinessHandler("1.0.0", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body readinessResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ready" || body.Enabled {
		t.Fatalf("body = %+v", body)
	}
}

func TestReadinessHandlerReadyzReportsFailingCheck(t *testing.T) {
	handler := readinessHandler("1.0.0", []api.ReadinessCheck{
		api.CheckFunc{CheckName: "postgres", Fn: func(context.Context) error { return errors.New("dependency unavailable") }},
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body readinessResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "not_ready" || !body.Enabled || len(body.Checks) != 1 || body.Checks[0].Status != "not_ready" {
		t.Fatalf("body = %+v", body)
	}
}
