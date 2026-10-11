package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A budget refusal names what it measured on the one failure line: its stage,
// its numbers, and every section holding at least 5% of the measured bytes.
// The line used to read failure_stage=unknown with nothing else, and the
// production refusal of a teams-wide question could not be diagnosed from it.
func TestBudgetRefusalLogNamesTheStageAndTheMeasuredSections(t *testing.T) {
	refusal := contextfabric.AnswerBudgetRefusal{
		Overrun: contractsv1.ContextFabricBudgetOverrunBytes, MeasuredItems: 22, MeasuredBytes: 100_000,
		MaxItems: 36, MaxSerializedBytes: 65_536, RetryAttempted: true, NothingLeftToCut: true,
		Family: contextfabric.QuestionFamilyDiscoveredCohortRanking,
		Stage:  contextfabric.StageSynthesis,
		Sections: []contractsv1.ContextFabricSectionBytes{
			{Name: "paths", Bytes: 40_000}, {Name: "claimed_facts", Bytes: 5_000}, {Name: "limitations", Bytes: 4_999}, {Name: "versions", Bytes: 600},
		},
	}
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, refusal
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 body=%s", response.Code, response.Body.String())
	}
	entry := decodeFailureLog(t, logs.String())
	if got := entry["failure_stage"]; got != "synthesis" {
		t.Fatalf("failure_stage = %v, want synthesis", got)
	}
	if got := entry["failure_classification"]; got != "budget_refusal" {
		t.Fatalf("failure_classification = %v, want budget_refusal", got)
	}
	if got := entry["budget_sections"]; got != "paths=40000,claimed_facts=5000" {
		t.Fatalf("budget_sections = %v, want exactly the sections at or above 5%% of the measured bytes", got)
	}
	for key, want := range map[string]any{
		"budget_overrun": "bytes", "budget_measured_bytes": float64(100_000), "budget_max_serialized_bytes": float64(65_536),
		"budget_measured_items": float64(22), "budget_max_items": float64(36),
		"budget_nothing_left_to_cut": true, "budget_retry_attempted": true, "budget_question_family": string(contextfabric.QuestionFamilyDiscoveredCohortRanking),
	} {
		if entry[key] != want {
			t.Fatalf("%s = %v, want %v", key, entry[key], want)
		}
	}
	if strings.Contains(logs.String(), "gather the answer") {
		t.Fatal("no answer text may reach the refusal line")
	}
}

// A refusal's own metadata is validated where it is logged: a stage outside the
// closed vocabulary is reported as unknown, an overrun outside items|bytes as
// unrecognized, and neither value's text reaches the line.
func TestBudgetRefusalLogRejectsMalformedRefusalMetadata(t *testing.T) {
	refusal := contextfabric.AnswerBudgetRefusal{
		Overrun: contractsv1.ContextFabricBudgetOverrun("not-an-axis\nforged=1"), MeasuredBytes: 70_000, MaxSerializedBytes: 65_536,
		Stage: contextfabric.InvestigationStage("not-a-stage\nforged=1"),
	}
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, refusal
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	entry := decodeFailureLog(t, logs.String())
	if got := entry["failure_stage"]; got != "unknown" {
		t.Fatalf("failure_stage = %v, want unknown for a stage outside the vocabulary", got)
	}
	if got := entry["budget_overrun"]; got != "unrecognized" {
		t.Fatalf("budget_overrun = %v, want unrecognized", got)
	}
	if strings.Contains(logs.String(), "forged") {
		t.Fatalf("a malformed value reached the log line: %s", logs.String())
	}
}
