package v1

import (
	"strings"
	"testing"
)

func joinResultWithCensusRow(t *testing.T, planned ContextFabricPlanRequirement) ContextFabricInvestigationResult {
	t.Helper()
	r := resultWithRequirementJoin(t, planned, planned.Requirement)
	row := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStageAssembledResult, Requirement: planned.Requirement,
		Obligation: planned.Obligation, Outcome: ContextFabricRequirementNarrowed,
		Impact: ContextFabricAnswerImpactScope, CauseCoverage: censusQualifiedToken,
		CauseObserved: true, Served: 5, Declared: 5,
	}
	r.Completeness.Outcomes = []ContextFabricPlanRequirementOutcomeRow{row}
	r.Completeness.State = DeriveContextFabricAnswerCompletenessState(r.Completeness.Outcomes)
	return r
}

func TestJoinAdmitsACensusCountRowForAComputedCountRequirement(t *testing.T) {
	t.Parallel()
	planned := validComputedRequirement()
	planned.Requirement, planned.Role, planned.Subject = "count/member/team", "member", ContextFabricSubjectTeam
	if err := joinResultWithCensusRow(t, planned).Validate(); err != nil {
		t.Fatalf("a computed count requirement with a census-qualified row is refused: %v", err)
	}
}

func TestJoinRefusesACountRowForAReadKindRequirement(t *testing.T) {
	t.Parallel()
	planned := validReadRequirement()
	planned.Requirement, planned.Obligation, planned.Role, planned.Subject = "count/member/team", "count", "member", ContextFabricSubjectTeam
	err := joinResultWithCensusRow(t, planned).Validate()
	if err == nil || !strings.Contains(err.Error(), "computed obligation") {
		t.Fatalf("a count row against a read-kind requirement: err = %v, want the computed-obligation refusal", err)
	}
}

func TestJoinAdmitsAReadObligationRowForAReadRequirement(t *testing.T) {
	t.Parallel()
	planned := validReadRequirement()
	if err := resultWithRequirementJoin(t, planned, planned.Requirement).Validate(); err != nil {
		t.Fatalf("a read obligation row against a read requirement is refused: %v", err)
	}
}
