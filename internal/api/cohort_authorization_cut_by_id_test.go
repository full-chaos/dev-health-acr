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

// A result stored BEFORE the authorization cut was stated on the count row:
// a cohort of 4, the degrading graph_cohort_denied_by_authorization detail
// (30 denied), and a `satisfied` 4/4 count row. The by-id read is a serving
// point, so it must not serve completeness `complete` for that cut member set.
// The stored row is kept; the served copy appends the narrowed row.
func TestContextFabricInvestigationResultRouteRestatesAStoredAuthorizationCutCount(t *testing.T) {
	store := memoryinvestigation.NewStore()
	stored := validContextFabricInvestigationResult()
	stored.ResultID = "result_route_authzcut1"
	members := make([]contractsv1.ContextFabricCohortMember, 0, 4)
	for _, id := range []string{"team:A", "team:B", "team:C", "team:D"} {
		members = append(members, contractsv1.ContextFabricCohortMember{
			Subject:          contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: id, Label: id},
			Rank:             len(members) + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	stored.Cohort = &contractsv1.ContextFabricCohort{Kind: contractsv1.ContextFabricSubjectTeam, Rationale: "scope census match", Members: members, Complete: true}
	denied := 30
	detail := contractsv1.ContextFabricCoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph",
		Code: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, Degrading: true, Count: &denied,
		Raw: "cohort_denied_by_authorization:30",
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	stored.Coverage.Details = []contractsv1.ContextFabricCoverageDetail{detail}
	stored.Coverage.DegradedReasons = []string{"cohort_denied_by_authorization:30"}
	stored.Coverage.Partial = true
	stored.Status = contractsv1.ContextFabricInvestigationPartial
	storedRow := contextfabric.RequirementOutcomeRow{
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "count/member/team", Obligation: "count",
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone, Served: 4, Declared: 4,
	}
	stored.Completeness.Outcomes = []contextfabric.RequirementOutcomeRow{{
		Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: "count/member/team", Obligation: "count",
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone,
	}, storedRow}
	stored.Completeness = contextfabric.ComputeAnswerCompleteness(stored)
	if stored.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("fixture: stored state = %q, want the pre-fix `complete`", stored.Completeness.State)
	}
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
	if got.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("by-id completeness.state = %q over a member set authorization cut by %d", got.Completeness.State, denied)
	}
	var assembled []contextfabric.RequirementOutcomeRow
	for _, row := range got.Completeness.Outcomes {
		if row.Obligation == "count" && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			assembled = append(assembled, row)
		}
	}
	if len(assembled) != 2 || assembled[0].Outcome != storedRow.Outcome || assembled[0].Served != 4 || assembled[0].Declared != 4 {
		t.Fatalf("assembled count rows = %+v, want the stored row kept, then the restated cut", assembled)
	}
	if assembled[1].Outcome != contractsv1.ContextFabricRequirementNarrowed ||
		assembled[1].CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization ||
		assembled[1].Served != 4 || assembled[1].Declared != 4+denied {
		t.Fatalf("restated row = %+v, want narrowed 4/%d naming the authorization cut", assembled[1], 4+denied)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("served result invalid: %v", err)
	}
}
