package v1

import "testing"

func TestNotApplicableRowMayNameOnlyTheObservedNoDeclaringProducerCause(t *testing.T) {
	t.Parallel()
	base := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStagePlanning, Requirement: "health/member/incident", Obligation: "health",
		Impact: ContextFabricAnswerImpactNone,
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*ContextFabricPlanRequirementOutcomeRow)
		wantErr bool
	}{
		{"not_applicable naming the cause, observed", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementNotApplicable
			r.CauseCoverage = ContextFabricCoverageDetailFactNoDeclaringProducer
			r.CauseObserved = true
		}, false},
		{"not_applicable naming no cause (older rows)", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementNotApplicable
		}, false},
		{"not_applicable cause not observed", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementNotApplicable
			r.CauseCoverage = ContextFabricCoverageDetailFactNoDeclaringProducer
		}, true},
		{"not_applicable other coverage cause", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementNotApplicable
			r.CauseCoverage = ContextFabricCoverageDetailFactPruned
			r.CauseObserved = true
		}, true},
		{"not_applicable with an overrun", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementNotApplicable
			r.CauseCoverage = ContextFabricCoverageDetailFactNoDeclaringProducer
			r.CauseOverrun = ContextFabricBudgetOverrunItems
			r.CauseObserved = true
		}, true},
		{"satisfied naming the cause", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementSatisfied
			r.CauseCoverage = ContextFabricCoverageDetailFactNoDeclaringProducer
			r.CauseObserved = true
		}, true},
	} {
		row := base
		tc.mutate(&row)
		err := ValidateContextFabricPlanRequirementOutcomeRow(row)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}
