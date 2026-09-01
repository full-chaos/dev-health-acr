package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// newContextFabricTestAppWithProductionLimitsAndLogs is
// newContextFabricTestAppWithProductionLimits (context_fabric_response_bound_test.go),
// except it keeps the captured log buffer instead of discarding it -- this
// ticket's proof needs to read the emitted line, not just the response.
func newContextFabricTestAppWithProductionLimitsAndLogs(t *testing.T, investigator contextfabric.Investigator) (*App, string, *bytes.Buffer) {
	t.Helper()
	app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, investigator, nil)
	app.config.MaxItems = productionMaxItems
	app.config.MaxOutputTokens = productionMaxOutputTokens
	app.config.MaxSerializedBytes = productionMaxSerializedBytes
	return app, token, logs
}

// decodeLogLine finds the first JSON log line whose "msg" equals want and
// returns it decoded, or fails the test -- the same shape as
// decodeFailureLog (context_fabric_failure_signal_test.go), generalized to
// any message.
func decodeLogLine(t *testing.T, logs string, want string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		entry := map[string]any{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["msg"] == want {
			return entry
		}
	}
	t.Fatalf("no %q log line found in:\n%s", want, logs)
	return nil
}

// TestContextFabricInvestigationRoutePassingAnswerLogsBudgetMeasurement is
// the (d) proof for CHAOS-4540: before this fix, the only place
// measured_items/measured_bytes/estimated_tokens were ever logged was a run
// that had already failed -- a passing answer's own margin could only ever
// be read as "it fits", never as a number. This pins that a 200 now emits
// "context fabric response measured" carrying the exact numbers the
// response was built from.
func TestContextFabricInvestigationRoutePassingAnswerLogsBudgetMeasurement(t *testing.T) {
	result := threeRollupProjectStatusResult("result_4540_pass")
	measuredBytes := marshaledSize(t, result)
	estimatedTokens := (measuredBytes + 3) / 4
	wantItems := contextFabricResultItemCounts(result).Total()

	app, token, logs := newContextFabricTestAppWithProductionLimitsAndLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	entry := decodeLogLine(t, logs.String(), "context fabric response measured")
	if got, want := entry["measured_bytes"], float64(measuredBytes); got != want {
		t.Fatalf("measured_bytes = %#v, want %v (body=%s)", got, want, response.Body.String())
	}
	if got, want := entry["measured_items"], float64(wantItems); got != want {
		t.Fatalf("measured_items = %#v, want %v", got, want)
	}
	if got, want := entry["estimated_tokens"], float64(estimatedTokens); got != want {
		t.Fatalf("estimated_tokens = %#v, want %v", got, want)
	}
	if got, want := entry["max_items"], float64(productionMaxItems); got != want {
		t.Fatalf("max_items = %#v, want %v", got, want)
	}
	if got, want := entry["max_serialized_bytes"], float64(productionMaxSerializedBytes); got != want {
		t.Fatalf("max_serialized_bytes = %#v, want %v", got, want)
	}
	if _, present := entry["request_id"]; !present {
		t.Fatal(`log entry missing "request_id" -- the measurement must correlate to the same request the response was served for`)
	}
}

// TestContextFabricInvestigationRouteMinimalAnswerStillLogsBudgetMeasurement
// is the "zero/quiet case" acceptance criterion: a minimal, far-under-budget
// answer must emit the SAME line with small (not omitted) numbers -- a
// quiet run must be exactly as visible as a busy one, matching
// RecordProjectedRowsCount/RecordFactScopeExpansion's own documented
// "quiet is not absent" discipline this ticket cites.
func TestContextFabricInvestigationRouteMinimalAnswerStillLogsBudgetMeasurement(t *testing.T) {
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_4540_minimal"

	app, token, logs := newContextFabricTestAppWithProductionLimitsAndLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != 200 {
		t.Fatalf("status = %d, want 200 -- this fixture must be well under every ceiling; body=%s", response.Code, response.Body.String())
	}
	entry := decodeLogLine(t, logs.String(), "context fabric response measured")
	measuredBytes, ok := entry["measured_bytes"].(float64)
	if !ok || measuredBytes <= 0 {
		t.Fatalf("measured_bytes = %#v, want a positive number -- a minimal answer still has a real measured size, not an omitted or zero one", entry["measured_bytes"])
	}
}

// TestContextFabricInvestigationRouteExceededPathStillLogsTheExceedLine is
// the non-regression half: the pre-existing exceed-path WARN
// ("context fabric response exceeded service limits") must be UNCHANGED --
// still fired, with its own field set intact -- when the new passing-path
// INFO line is added beside it. A 413 must NOT also emit the passing-path
// line, since the answer never reached the point that line describes.
func TestContextFabricInvestigationRouteExceededPathStillLogsTheExceedLine(t *testing.T) {
	result := twelveRollupResult("result_4540_over")
	app, token, logs := newContextFabricTestAppWithProductionLimitsAndLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != 413 {
		t.Fatalf("status = %d, want 413; body=%s", response.Code, response.Body.String())
	}
	_ = decodeLogLine(t, logs.String(), "context fabric response exceeded service limits")
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		entry := map[string]any{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["msg"] == "context fabric response measured" {
			t.Fatalf("a 413 response also logged the passing-path measurement line: %v -- the answer never reached the point that line describes", entry)
		}
	}
}
