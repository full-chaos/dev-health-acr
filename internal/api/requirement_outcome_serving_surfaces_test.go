package api

// The by-id read is a SERVING SURFACE, and it owes what every other serving
// surface owes: a requirement whose assembled account differs from its
// derivation-time prediction is stated on the trace, and a document that claims
// a requirement satisfied with no served evidence of its kind and subject is
// refused rather than served.
//
// This route reads storage directly and never reaches finalizeServed, which is
// exactly why these are separate pins rather than a consequence of the engine's.

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// storedCountPlan is the published plan for a turn that planned a count over
// team members -- the derivation predicts it SERVED.
func storedCountPlan() *contractsv1.ContextFabricAnswerPlan {
	return &contractsv1.ContextFabricAnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilyScopedCohortStatus,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceModel,
		FamilyVersion: "family-v1",
		MemberKind:    contractsv1.ContextFabricSubjectTeam,
		Budget:        contractsv1.ContextFabricAnswerPlanBudget{MaxItems: 30, MaxSerializedBytes: 262144},
		Requirements: []contractsv1.ContextFabricPlanRequirement{{
			Requirement:   "count/member/team",
			Obligation:    contractsv1.ContextFabricAnswerObligationCount,
			Role:          "member",
			Subject:       contractsv1.ContextFabricSubjectTeam,
			Kind:          "computed",
			Step:          "membership_cardinality",
			StepExecution: "server_executed",
			InputClass:    "resolved_member_set",
			Scope:         "each_member",
			Quantifier:    "exact",
		}},
	}
}

// storedResultWithCountOutcome is a stored, valid result carrying the published
// count plan and the given assembled-stage row for it.
func storedResultWithCountOutcome(t *testing.T, assembled contractsv1.ContextFabricPlanRequirementOutcomeRow, claims []contractsv1.ContextFabricClaimedFact) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_byid_surfaces01"
	result.Status = contextfabric.InvestigationPartial
	result.AnswerPlan = storedCountPlan()
	if claims == nil {
		// A stored document carries an empty claim list, never a null one.
		claims = []contractsv1.ContextFabricClaimedFact{}
	}
	result.ClaimedFacts = claims
	result.Completeness.Outcomes = []contractsv1.ContextFabricPlanRequirementOutcomeRow{
		{
			Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
			Requirement: "count/member/team",
			Obligation:  contractsv1.ContextFabricAnswerObligationCount,
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
		},
		assembled,
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	if err := result.Validate(); err != nil {
		t.Fatalf("the stored fixture does not validate, so it does not model a persisted row: %v", err)
	}
	return result
}

// TestByIDRoute_StatesTheRequirementTransitionItServes: a stored document whose
// count was predicted served and ended unavailable is served by id, and the read
// says so on the trace. Without it, the same mismatch is observable on the
// investigation that produced it and invisible on every later read of it.
func TestByIDRoute_StatesTheRequirementTransitionItServes(t *testing.T) {
	stored := storedResultWithCountOutcome(t, contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement:   "count/member/team",
		Obligation:    contractsv1.ContextFabricAnswerObligationCount,
		Outcome:       contractsv1.ContextFabricRequirementUnavailable,
		Impact:        contractsv1.ContextFabricAnswerImpactDimension,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailFactPruned,
		CauseObserved: true,
	}, nil)
	app, token, logs := newCompletenessAuthorityTestApp(t, legacyResultStore{result: stored}, false)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(logs.String(), contextfabric.RequirementOutcomeTransitionLogMessage) {
		t.Fatalf("a by-id read served a requirement mismatch with no transition line: %s", logs.String())
	}
	for _, fragment := range []string{`"requirement":"count/member/team"`, `"predicted":"served"`, `"assembled_outcome":"unavailable"`, `"cause":"computed_population_absent"`} {
		if !strings.Contains(logs.String(), fragment) {
			t.Errorf("the by-id transition line lacks %s", fragment)
		}
	}
}

// TestByIDRoute_RefusesAStoredSatisfiedRequirementWithNoServedEvidence: the
// invariant is about the document, not about the path it took to a reader.
func TestByIDRoute_RefusesAStoredSatisfiedRequirementWithNoServedEvidence(t *testing.T) {
	stored := storedResultWithCountOutcome(t, contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: "count/member/team",
		Obligation:  contractsv1.ContextFabricAnswerObligationCount,
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
		Served:      3,
		Declared:    3,
	}, nil)
	app, token, _ := newCompletenessAuthorityTestApp(t, legacyResultStore{result: stored}, false)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code == 200 {
		t.Fatalf("a by-id read served a satisfied team count with no cardinality claim and no member set: %s", recorder.Body.String())
	}
	if recorder.Code != 500 {
		t.Fatalf("status = %d, want 500 -- a stored document that states an unsupported satisfied requirement is a server defect", recorder.Code)
	}
}

// The control: the same stored shape WITH the served evidence its row claims is
// served unchanged, so the refusal above is about the missing evidence and not
// about the fixture.
func TestByIDRoute_ServesASatisfiedRequirementThatItsEvidenceBacks(t *testing.T) {
	teamCount := int64(3)
	stored := storedResultWithCountOutcome(t, contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: "count/member/team",
		Obligation:  contractsv1.ContextFabricAnswerObligationCount,
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
		Served:      3,
		Declared:    3,
	}, []contractsv1.ContextFabricClaimedFact{{
		ClaimID: "server:cardinality:team",
		Kind:    contractsv1.ContextFabricFactCardinality,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: "org_1", Label: "org_1"},
		Field:   "team_count",
		Value:   contractsv1.ContextFabricScalarValue{Integer: &teamCount},
	}})
	app, token, _ := newCompletenessAuthorityTestApp(t, legacyResultStore{result: stored}, false)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200 -- this document carries the cardinality claim its row claims (body %s)", recorder.Code, recorder.Body.String())
	}
}
