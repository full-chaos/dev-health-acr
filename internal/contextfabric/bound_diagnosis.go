package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// diagnoseSynthesisDraftBound re-derives, for reporting purposes only,
// which model-facing length/count bound (if any) caused d to fail
// ValidateAgainst -- checked in the same order ValidateAgainst itself
// visits: claimed facts, then drivers, then the remaining_work/
// readiness_gaps/conflicts findings. It never decides accept/reject; it
// only explains a rejection ValidateAgainst already made (CHAOS-3784). See
// contractsv1's context_fabric_bound_diagnosis.go doc comment.
//
// Top-level synthesis collection caps (drivers.max_count,
// remaining_work.max_count, and siblings in
// contractsv1.ContextFabricModelFacingBounds) are deliberately NOT
// diagnosed here: ValidateAgainst itself never checks them -- only
// ContextFabricInvestigationResult.Validate() does, later and against the
// already-composed InvestigationResult, classified ErrInvalidResult (see
// engine.go), not ErrSynthesisRejected. A synthesis draft that violates one
// of those top-level caps therefore never reaches this function with a
// non-nil ValidateAgainst error in the first place; distinguishing THAT
// class of rejection is a separate, pre-existing gap (a violation there is
// misattributed to ACR as a 500, not to the model) outside CHAOS-3784's
// narrow scope.
func diagnoseSynthesisDraftBound(d SynthesisDraft) (bound string, ok bool) {
	for _, claim := range d.ClaimedFacts {
		if bound, ok := contractsv1.DiagnoseContextFabricClaimedFactBound(claim); ok {
			return bound, true
		}
	}
	for _, driver := range d.Drivers {
		if bound, ok := contractsv1.DiagnoseContextFabricDriverJudgmentBound(driver); ok {
			return bound, true
		}
	}
	for _, findings := range [][]Finding{d.RemainingWork, d.ReadinessGaps, d.Conflicts} {
		for _, finding := range findings {
			if bound, ok := contractsv1.DiagnoseContextFabricFindingBound(finding); ok {
				return bound, true
			}
		}
	}
	return "", false
}
