package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// factReadWindowStated reports whether the canonical window a fact read
// carries (factReadQuestion) was supplied by the caller: stated in the
// question or the request (question_stated), or confirmed from an option this
// investigation offered (clarification_confirmed). An inferred_default window
// and no window both give false.
//
// factReadQuestion writes the window onto the read whatever its provenance,
// and the rollup providers read it either way. The investment provider reads
// all history when no period was stated, so it bounds its work units only by
// a stated window (CHAOS-12142). Whether an unstated read should take the
// platform default window instead is an open question on that ticket.
func factReadWindowStated(effective *contractsv1.ContextFabricEffectiveEvidenceWindow) bool {
	if effective == nil {
		return false
	}
	switch effective.Provenance {
	case WindowQuestionStated, WindowClarificationConfirmed:
		return true
	default:
		return false
	}
}
