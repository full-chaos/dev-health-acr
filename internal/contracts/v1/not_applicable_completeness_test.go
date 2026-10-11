package v1

import "testing"

func notApplicableRow(stage ContextFabricOutcomeStage, requirement, obligation string, outcome ContextFabricPlanRequirementOutcome) ContextFabricPlanRequirementOutcomeRow {
	row := ContextFabricPlanRequirementOutcomeRow{Stage: stage, Requirement: requirement, Obligation: obligation, Outcome: outcome, Impact: ContextFabricAnswerImpactNone}
	return row
}

// A read requirement that does not apply to the member kind is not an
// unevaluated read: it must lose nothing, so an otherwise complete set stays
// complete, while an applicable planning-only read still makes it partial.
func TestANotApplicableReadSeedDoesNotMakeACompleteSetPartial(t *testing.T) {
	t.Parallel()
	served := []ContextFabricPlanRequirementOutcomeRow{
		notApplicableRow(ContextFabricOutcomeStagePlanning, "state/member/incident", "state", ContextFabricRequirementSatisfied),
		notApplicableRow(ContextFabricOutcomeStageAssembledResult, "state/member/incident", "state", ContextFabricRequirementSatisfied),
	}
	notApplicable := notApplicableRow(ContextFabricOutcomeStagePlanning, "health/member/incident", "health", ContextFabricRequirementNotApplicable)
	if got := DeriveContextFabricAnswerCompletenessState(append(append([]ContextFabricPlanRequirementOutcomeRow{}, served...), notApplicable)); got != ContextFabricAnswerCompletenessComplete {
		t.Fatalf("state = %q with a not_applicable read seed, want complete", got)
	}
	// Control: the same row as a satisfied planning-only read is still unevaluated.
	unevaluated := notApplicableRow(ContextFabricOutcomeStagePlanning, "health/member/incident", "health", ContextFabricRequirementSatisfied)
	if got := DeriveContextFabricAnswerCompletenessState(append(append([]ContextFabricPlanRequirementOutcomeRow{}, served...), unevaluated)); got != ContextFabricAnswerCompletenessPartial {
		t.Fatalf("state = %q with a satisfied planning-only read, want partial", got)
	}
}
