package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12242 (team-lead ruling, option 1): a served-status floor can put the
// status BELOW completeness. Completeness counts outcome rows only (vol 3
// D25), and these floors read a coverage detail or a limitation that no
// outcome row states, so every requirement can be met (completeness
// `complete`) while the status is `partial`. The pair is pinned here through
// the real by-id handler, for both floors that produce it.

// storedSatisfiedAnswer is a stored `complete` answer whose one requirement
// is satisfied: completeness `complete`.
func storedSatisfiedAnswer(t *testing.T, resultID string) contextfabric.InvestigationResult {
	t.Helper()
	stored := validContextFabricInvestigationResult()
	stored.ResultID = resultID
	stored.Status = contractsv1.ContextFabricInvestigationComplete
	stored.Completeness.Outcomes = []contextfabric.RequirementOutcomeRow{{
		Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: "state/subject/project", Obligation: "state",
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone,
	}, {
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "state/subject/project", Obligation: "state",
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone, Served: 1, Declared: 1,
	}}
	return stored
}

// servedByID saves stored and reads it back through the by-id route.
func servedByID(t *testing.T, stored contextfabric.InvestigationResult) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	stored.Completeness = contextfabric.ComputeAnswerCompleteness(stored)
	if stored.Status != contractsv1.ContextFabricInvestigationComplete || stored.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("fixture: stored status %q, completeness %q, want both complete", stored.Status, stored.Completeness.State)
	}
	store := memoryinvestigation.NewStore()
	if err := store.Save(context.Background(), seedPrincipal(callerOrgID), stored, contextfabric.SourceWatermarkSnapshot{}, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	app, token := newContextFabricTestAppWithResults(t, nil, store)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("served result invalid: %v", err)
	}
	return got
}

func requirePartialBelowComplete(t *testing.T, got contractsv1.ContextFabricInvestigationResult) {
	t.Helper()
	if got.Status != contractsv1.ContextFabricInvestigationPartial {
		t.Fatalf("by-id status = %q, want the floor's partial", got.Status)
	}
	if got.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("by-id completeness.state = %q, want complete: every outcome row is satisfied, and completeness counts outcome rows only", got.Completeness.State)
	}
	if got.Completeness.TerminalStatus != got.Status {
		t.Fatalf("completeness.terminal_status = %q, want the served status %q", got.Completeness.TerminalStatus, got.Status)
	}
}

// The unverified project narrowing floor: partial, completeness complete, and
// the read_population_unverified detail kept.
func TestByIDAnUnverifiedProjectNarrowingServesPartialBesideCompleteCompleteness(t *testing.T) {
	stored := storedSatisfiedAnswer(t, "result_route_narrowing_floor")
	detail := contractsv1.ContextFabricCoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph",
		Code: contractsv1.ContextFabricCoverageDetailReadPopulationUnverified, Degrading: true,
		Raw: contextfabric.ProjectNarrowingUnverifiedRaw,
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	stored.Coverage.Details = []contractsv1.ContextFabricCoverageDetail{detail}
	stored.Coverage.DegradedReasons = []string{contextfabric.ProjectNarrowingUnverifiedRaw}
	stored.Coverage.Partial = true
	got := servedByID(t, stored)
	requirePartialBelowComplete(t, got)
	if len(got.Coverage.Details) != 1 || got.Coverage.Details[0].Code != contractsv1.ContextFabricCoverageDetailReadPopulationUnverified ||
		got.Coverage.Details[0].Raw != contextfabric.ProjectNarrowingUnverifiedRaw || !got.Coverage.Details[0].Degrading {
		t.Fatalf("coverage details = %+v, want the degrading read_population_unverified detail kept", got.Coverage.Details)
	}
}

// The member-status pool cut floor gives the same pair: its limitation is
// read by no outcome row either.
func TestByIDAMemberStatusPoolCutServesPartialBesideCompleteCompleteness(t *testing.T) {
	stored := storedSatisfiedAnswer(t, "result_route_pool_cut_floor")
	limitation := contractsv1.ContextFabricMemberStatusPoolCutLimitation("project", 50, 120)
	if !contractsv1.IsContextFabricMemberStatusPoolCutLimitation(limitation) {
		t.Fatalf("fixture: %q is not a pool-cut limitation", limitation)
	}
	stored.Limitations = append(stored.Limitations, limitation)
	requirePartialBelowComplete(t, servedByID(t, stored))
}
