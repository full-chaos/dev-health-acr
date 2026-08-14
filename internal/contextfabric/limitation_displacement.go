package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// withRetrievalDegradation returns limitations carrying the
// retrieval-degradation disclosure, DISPLACING the last model-authored
// entry when the list is already at the contract's cap (CHAOS-3746).
//
// A plain append was the original defect. When the model returned a full
// list, the append produced one entry too many and
// ContextFabricInvestigationResult.Validate rejected the whole result --
// ErrInvalidResult, no answer at all, because a disclosure could not fit.
// That is a real path rather than a curiosity: a degraded retrieval is
// exactly the run most likely to produce a long limitation list, since the
// same missing mechanism gives the model more gaps to note.
//
// Displacement, not refusal, and not silence. The three candidate
// behaviours at the cap are: drop the disclosure (a degraded answer reads
// as a clean one -- the worst outcome, and invisible), fail the
// investigation (the current defect), or displace one model caveat. Only
// the third keeps the answer AND the statement of what it is worth.
//
// The disclosure is service-authored and says how much the whole answer can
// be trusted; a model caveat is one of many, and the last one is the least
// prominent. So the LAST model-authored entry gives way, earlier ones keep
// their order, and the disclosure lands at the end where a reader meets it
// after the caveats it qualifies.
//
// The cap is read from the contract rather than restated, so this cannot
// drift from the bound it exists to respect -- the same relation
// fact_registry.go's coverage clamps now hold.
func withRetrievalDegradation(limitations []string) []string {
	// Either spelling already present: nothing to add, and nothing may be
	// displaced to make room for a duplicate. A reused answer reaches here
	// carrying its stored wording, which must survive verbatim.
	if hasRetrievalDegradedLimitation(limitations) {
		return limitations
	}
	if len(limitations) < contractsv1.ContextFabricLimitationsMaxCount {
		return append(limitations, retrievalDegradedLimitation)
	}
	// At (or somehow past) the cap. Keep the first cap-1 entries in their
	// original order and put the disclosure last.
	kept := append([]string(nil), limitations[:contractsv1.ContextFabricLimitationsMaxCount-1]...)
	return append(kept, retrievalDegradedLimitation)
}

// displacedLimitationCount reports how many model-authored limitations
// withRetrievalDegradation dropped, so the projection can DECLARE the loss
// rather than let a shortened list read as a complete one.
//
// It takes the before and after lists rather than recomputing the rule: a
// second copy of the arithmetic is a second thing that can be wrong, and
// this counter's whole job is to be right about what the other function
// actually did.
func displacedLimitationCount(before, after []string) int {
	if len(before) <= len(after) {
		return 0
	}
	return len(before) - len(after)
}
