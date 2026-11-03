package v1

import "testing"

// CHAOS-12822 (the expand step for CHAOS-12821): a read requirement whose every declared
// kind came back `not_applicable` from its provider is published as `not_applicable`, and
// the reason it names is the provider's report. That pairing is legal only as an OBSERVED
// cause, only on an assembled-result row (a planning row runs before any provider speaks),
// and only with no overrun or narrowing basis. Both the write bounds and the stored-read
// bounds accept it, so a build that has this step can read a row the emitter writes.
func TestNotApplicableRowMayNameTheObservedProviderReportOnAnAssembledResult(t *testing.T) {
	t.Parallel()
	base := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStageAssembledResult, Requirement: "health/subject/team", Obligation: "health",
		Outcome: ContextFabricRequirementNotApplicable, Impact: ContextFabricAnswerImpactNone,
		CauseCoverage: ContextFabricCoverageDetailFactProviderReported, CauseObserved: true,
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*ContextFabricPlanRequirementOutcomeRow)
		wantErr bool
	}{
		{"provider report, observed, assembled result", func(*ContextFabricPlanRequirementOutcomeRow) {}, false},
		{"provider report not observed", func(r *ContextFabricPlanRequirementOutcomeRow) { r.CauseObserved = false }, true},
		{"provider report on a planning row", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Stage = ContextFabricOutcomeStagePlanning
		}, true},
		// Projection and reuse run after assembly and perform no read, so neither can
		// carry a provider's report either (Codex r1, P3: only planning was pinned).
		{"provider report on a projection row", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Stage = ContextFabricOutcomeStageProjection
		}, true},
		{"provider report on a reuse row", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Stage = ContextFabricOutcomeStageReuse
		}, true},
		{"provider report with an overrun", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.CauseOverrun = ContextFabricBudgetOverrunItems
		}, true},
		{"provider report with a narrowing basis", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.CauseNarrowing = ContextFabricNarrowingBasisCanonicalIDLexical
		}, true},
		{"a read failure is not a reason for not_applicable", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.CauseCoverage = ContextFabricCoverageDetailFactReadFailed
		}, true},
		{"a satisfied row still names no provider report", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementSatisfied
		}, true},
		{"not_applicable with a loss impact", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Impact = ContextFabricAnswerImpactDimension
		}, true},
	} {
		for _, bounds := range []struct {
			name   string
			bounds contextFabricBounds
		}{{"write", contextFabricWriteBounds}, {"stored read", contextFabricLegacyBounds}} {
			row := base
			tc.mutate(&row)
			err := validatePlanRequirementOutcomeRow(row, bounds.bounds)
			if (err != nil) != tc.wantErr {
				t.Errorf("%s (%s bounds): err = %v, wantErr %v", tc.name, bounds.name, err, tc.wantErr)
			}
		}
	}
}

// A not_applicable row naming the provider's report loses nothing: it contributes nothing
// to the derived state, exactly like the no-declaring-producer reason.
func TestAProviderReportedNotApplicableRowKeepsACompleteSetComplete(t *testing.T) {
	t.Parallel()
	rows := []ContextFabricPlanRequirementOutcomeRow{
		{Stage: ContextFabricOutcomeStageAssembledResult, Requirement: "state/subject/team", Obligation: "state",
			Outcome: ContextFabricRequirementSatisfied, Impact: ContextFabricAnswerImpactNone, Served: 2, Declared: 2},
		{Stage: ContextFabricOutcomeStageAssembledResult, Requirement: "health/subject/team", Obligation: "health",
			Outcome: ContextFabricRequirementNotApplicable, Impact: ContextFabricAnswerImpactNone,
			CauseCoverage: ContextFabricCoverageDetailFactProviderReported, CauseObserved: true},
	}
	for index, row := range rows {
		if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
			t.Fatalf("rows[%d] is not a valid row: %v", index, err)
		}
	}
	if got := DeriveContextFabricAnswerCompletenessStateBeforeReadEvaluation(rows); got != ContextFabricAnswerCompletenessComplete {
		t.Fatalf("derived state = %q, want complete", got)
	}
}
