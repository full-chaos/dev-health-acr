package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3811. A failed investigation used to record one line -- failure_class
// -- identical for a graph outage, a rejected fact request, and an ACR-side
// invariant breach. This pins the bounded stage + classification pair that
// replaced it, and pins that NOTHING derived from the error's own text is
// logged at any level.

func decodeFailureLog(t *testing.T, logs string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		entry := map[string]any{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["msg"] == "context fabric investigation failed" {
			return entry
		}
	}
	t.Fatalf("no investigation failure log line found in:\n%s", logs)
	return nil
}

func TestContextFabricInvestigationFailuresCarryStageAndClassification(t *testing.T) {
	staged := func(stage contextfabric.InvestigationStage, err error) error {
		// The route reads the stage through the exported StageError, the
		// same way Engine attaches it.
		return &contextfabric.StageError{Stage: stage, Err: err}
	}
	cases := []struct {
		name           string
		err            error
		wantStatus     int
		wantStage      string
		wantClassified string
		wantLevel      string
	}{
		{
			name: "graph unavailable during resolution", wantStatus: http.StatusServiceUnavailable,
			err:       staged(contextfabric.StageResolution, fmt.Errorf("resolve subjects: %w", contextfabric.ErrUnavailable)),
			wantStage: "resolution", wantClassified: "dependency_unavailable", wantLevel: "ERROR",
		},
		{
			name: "no investigation subjects at the fact read", wantStatus: http.StatusInternalServerError,
			err:       staged(contextfabric.StageFactRead, fmt.Errorf("read canonical facts: %w", contextfabric.ErrNoInvestigationSubjects)),
			wantStage: "fact_read", wantClassified: "no_investigation_subjects", wantLevel: "ERROR",
		},
		{
			name: "invalid result at validation", wantStatus: http.StatusInternalServerError,
			err:       staged(contextfabric.StageValidation, fmt.Errorf("%w: paths", contextfabric.ErrInvalidResult)),
			wantStage: "validation", wantClassified: "invalid_result", wantLevel: "ERROR",
		},
		{
			name: "model output invalid at synthesis", wantStatus: http.StatusBadGateway,
			err:       staged(contextfabric.StageSynthesis, fmt.Errorf("synthesize investigation: %w", contextfabric.ErrModelOutput)),
			wantStage: "synthesis", wantClassified: "model_output_invalid", wantLevel: "ERROR",
		},
		{
			name: "an unanswerable time bound is a caller problem", wantStatus: http.StatusBadRequest,
			err:       fmt.Errorf("%w: as-of time is in the future", contextfabric.ErrInvalidTimeBound),
			wantStage: "unknown", wantClassified: "invalid_time_bound", wantLevel: "WARN",
		},
		{
			name: "an unstaged, unclassified error is named as such", wantStatus: http.StatusInternalServerError,
			err:       errors.New("something unexpected broke"),
			wantStage: "unknown", wantClassified: "unclassified", wantLevel: "ERROR",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, testCase.err
			}))
			response := httptest.NewRecorder()

			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			entry := decodeFailureLog(t, logs.String())
			if got := entry["failure_stage"]; got != testCase.wantStage {
				t.Fatalf("failure_stage = %v, want %q", got, testCase.wantStage)
			}
			if got := entry["failure_classification"]; got != testCase.wantClassified {
				t.Fatalf("failure_classification = %v, want %q", got, testCase.wantClassified)
			}
			if got := entry["level"]; got != testCase.wantLevel {
				t.Fatalf("level = %v, want %q", got, testCase.wantLevel)
			}
			if got := entry["failure_class"]; got != "context_fabric_investigation" {
				t.Fatalf("failure_class = %v, want the pre-existing value to be preserved", got)
			}
		})
	}
}

// The standing rule: a bounded classification is a guarantee only if it holds
// at every level. A failure whose cause carries a raw dependency message must
// log the classification and NOTHING of that message -- no debug hatch.
func TestContextFabricInvestigationFailureLogsNeverCarryRawCauseText(t *testing.T) {
	const secret = "dial tcp 10.1.2.3:16379: connection refused by falkordb-primary"
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, &contextfabric.StageError{
			Stage: contextfabric.StageGraph,
			Err:   fmt.Errorf("discover graph context: %s: %w", secret, contextfabric.ErrUnavailable),
		}
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if strings.Contains(logs.String(), "10.1.2.3") || strings.Contains(logs.String(), "falkordb") {
		t.Fatalf("failure log carries raw cause text:\n%s", logs.String())
	}
	if strings.Contains(response.Body.String(), "10.1.2.3") || strings.Contains(response.Body.String(), "falkordb") {
		t.Fatalf("response body carries raw cause text: %s", response.Body.String())
	}
	if got := decodeFailureLog(t, logs.String())["failure_stage"]; got != "graph" {
		t.Fatalf("failure_stage = %v, want \"graph\"", got)
	}
}
