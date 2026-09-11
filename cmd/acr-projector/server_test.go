package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
)

func TestReadinessHandlerHealthz(t *testing.T) {
	handler := readinessHandler("1.0.0", nil, nil)
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
	handler := readinessHandler("1.0.0", nil, nil)
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
	}, nil)
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

// TestReadinessHandlerLogsTransitions is r1 P3 finding 4's own pin:
// acr-projector's readiness handler must log the SAME class of transition
// telemetry acr-api's does -- an Info line the first time /readyz is
// observed and again whenever the status actually changes, naming the
// real check ("postgres", never an unevaluated zero value).
func TestReadinessHandlerLogsTransitions(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, nil))
	failing := false
	handler := readinessHandler("1.0.0", []api.ReadinessCheck{
		api.CheckFunc{CheckName: "postgres", Fn: func(context.Context) error {
			if failing {
				return errors.New("postgres unreachable")
			}
			return nil
		}},
	}, logger)

	poll := func() {
		t.Helper()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	}
	// aggregateLines matches ONLY the aggregate "readiness state changed"
	// line. r2 P3 finding: this filter previously also silently excluded
	// the per-check message, so the test never actually observed it --
	// "readiness state changed" is not a substring of "readiness check
	// state changed" (the word "check" breaks the contiguous match), so
	// the two message classes never collided here, but the test also never
	// asserted the per-check message fired at all. perCheckLines below
	// closes that gap.
	aggregateLines := func() []string {
		var lines []string
		for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
			if strings.Contains(line, `"msg":"readiness state changed"`) {
				lines = append(lines, line)
			}
		}
		return lines
	}
	perCheckLines := func() []string {
		var lines []string
		for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
			if strings.Contains(line, `"msg":"readiness check state changed"`) {
				lines = append(lines, line)
			}
		}
		return lines
	}

	poll()
	if lines := aggregateLines(); len(lines) != 1 || !strings.Contains(lines[0], `"postgres":"ready"`) {
		t.Fatalf("after first observation, aggregate transition lines = %v", lines)
	}
	if lines := perCheckLines(); len(lines) != 1 || !strings.Contains(lines[0], `"check":"postgres"`) || !strings.Contains(lines[0], `"status":"ready"`) {
		t.Fatalf("after first observation, per-check transition lines = %v, want exactly 1 naming postgres", lines)
	}
	poll()
	if lines := aggregateLines(); len(lines) != 1 {
		t.Fatalf("after a repeated ready poll, aggregate transition lines = %d, want 1 (no duplicate): %v", len(lines), lines)
	}
	if lines := perCheckLines(); len(lines) != 1 {
		t.Fatalf("after a repeated ready poll, per-check transition lines = %d, want 1 (no duplicate): %v", len(lines), lines)
	}

	failing = true
	poll()
	lines := aggregateLines()
	if len(lines) != 2 || !strings.Contains(lines[1], `"previous_status":"ready"`) || !strings.Contains(lines[1], `"status":"not_ready"`) {
		t.Fatalf("after failure, aggregate transition lines = %v", lines)
	}
	checkLines := perCheckLines()
	if len(checkLines) != 2 || !strings.Contains(checkLines[1], `"check":"postgres"`) || !strings.Contains(checkLines[1], `"previous_status":"ready"`) || !strings.Contains(checkLines[1], `"status":"not_ready"`) {
		t.Fatalf("after failure, per-check transition lines = %v, want a named postgres flip", checkLines)
	}
}
