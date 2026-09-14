package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// servedRequirementTransitionResult is a stored result in the diagnosed shape:
// the count predicted satisfied at planning, stated unavailable as
// `fact_pruned` at assembly, with a model status of partial.
func servedRequirementTransitionResult() contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_byid_transition01"
	result.Status = contextfabric.InvestigationPartial
	result.Completeness.Outcomes = []contractsv1.ContextFabricPlanRequirementOutcomeRow{
		{
			Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
			Requirement: "count/member/team",
			Obligation:  "count",
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
		},
		{
			Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
			Requirement:   "count/member/team",
			Obligation:    "count",
			Outcome:       contractsv1.ContextFabricRequirementUnavailable,
			Impact:        contractsv1.ContextFabricAnswerImpactDimension,
			CauseCoverage: contractsv1.ContextFabricCoverageDetailFactPruned,
			CauseObserved: true,
		},
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// TestByIDRoute_ServesTheStoredRequirementOutcomeRowsUnchanged pins the by-id
// half of reconciliation: a stored document is read back with the SAME outcome
// rows it was saved with, and the completeness authority measured on that read
// derives degraded from them against the model's partial.
func TestByIDRoute_ServesTheStoredRequirementOutcomeRowsUnchanged(t *testing.T) {
	stored := servedRequirementTransitionResult()
	if err := stored.Validate(); err != nil {
		t.Fatalf("the stored fixture does not validate: %v", err)
	}
	app, token, logs := newCompletenessAuthorityTestApp(t, legacyResultStore{result: stored}, false)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var served contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
		t.Fatalf("served body did not decode: %v", err)
	}
	if !reflect.DeepEqual(served.Completeness.Outcomes, stored.Completeness.Outcomes) {
		t.Fatalf("the by-id read changed the outcome rows:\n  served: %+v\n  stored: %+v", served.Completeness.Outcomes, stored.Completeness.Outcomes)
	}
	for _, fragment := range []string{`"model_status":"partial"`, `"server_state":"degraded"`, `"disagreed":true`} {
		if !strings.Contains(logs.String(), fragment) {
			t.Fatalf("the by-id authority measurement lacks %s: %s", fragment, logs.String())
		}
	}
}
