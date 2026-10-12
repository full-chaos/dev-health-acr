package contextfabric

// The scope anchor kind a turn decides under, from the binding.
//
// The binder's kind precedence is a redeemed anchor receipt, then an
// admitted carried binding, then the model's stated kind. Before this switch
// only the binder applied it; retrieval, the count's population decision,
// discovery, the engine-committed capture and the persisted reading all read
// the model's kind, so a carried repository could be applied by the ledger,
// found by ownership and refused by the count in one turn.
//
// With EngineOptions.AnchorBindingCountEnabled on, the engine applies the
// binding's kind to the accepted reading once, right after admission and
// before any resolution, exactly the way it already applies a kind derived
// from a committed hint: the winning sample's ScopeAnchorKind is replaced and
// its kind source says where the kind came from. Every stage then reads one
// kind. A kind the frame refuses as its anchor (not a scoped frame, or the
// member kind) is not applied. Switched off, nothing changes.
//
// THE PERSISTED READING KEEPS THE MODEL'S KIND. The bound kind is this turn's
// decision, re-made from this turn's admission; persisting it as the reading
// would let a continuation install it on a later turn whose own admission
// refused the binding. BuildSemanticState stores the kind the reading stated
// before the replacement.

// ScopeAnchorKindAnchorBinding: the kind was applied from the anchor binding
// (a redeemed anchor receipt or an admitted carried binding).
const ScopeAnchorKindAnchorBinding ScopeAnchorKindSource = "anchor_binding"

// BoundAnchorKindDecision is the closed record of whether the binding's kind
// was applied to the turn's reading.
type BoundAnchorKindDecision string

const (
	// BoundAnchorKindSwitchOff: AnchorBindingCountEnabled is off, or the
	// shadow binding is off.
	BoundAnchorKindSwitchOff BoundAnchorKindDecision = "switch_off"
	// BoundAnchorKindNotEvaluated: the turn ended before the decision point.
	BoundAnchorKindNotEvaluated BoundAnchorKindDecision = "not_evaluated"
	// BoundAnchorKindNone: neither a redeemed anchor receipt nor an admitted
	// carried binding names a kind.
	BoundAnchorKindNone BoundAnchorKindDecision = "none"
	// BoundAnchorKindAgrees: the binding's kind is the model's.
	BoundAnchorKindAgrees BoundAnchorKindDecision = "agrees"
	// BoundAnchorKindRejectedByFrame: the frame does not admit the binding's
	// kind as its anchor; the model's kind stands.
	BoundAnchorKindRejectedByFrame BoundAnchorKindDecision = "rejected_by_frame"
	// BoundAnchorKindAppliedReceipt: a redeemed anchor receipt's kind
	// replaced the model's.
	BoundAnchorKindAppliedReceipt BoundAnchorKindDecision = "applied_receipt"
	// BoundAnchorKindAppliedCarried: an admitted carried binding's kind
	// replaced the model's.
	BoundAnchorKindAppliedCarried BoundAnchorKindDecision = "applied_carried"
)

func boundAnchorKindDecisions() []BoundAnchorKindDecision {
	return []BoundAnchorKindDecision{
		BoundAnchorKindSwitchOff, BoundAnchorKindNotEvaluated, BoundAnchorKindNone, BoundAnchorKindAgrees,
		BoundAnchorKindRejectedByFrame, BoundAnchorKindAppliedReceipt, BoundAnchorKindAppliedCarried,
	}
}

// boundAnchorKind is the kind the binding names for this turn and whether it
// came from a receipt: a redeemed anchor receipt first, then an admitted
// carried binding. Empty when neither names one.
func (t *anchorBindingTracker) boundAnchorKind() (SubjectKind, bool) {
	if ref := receiptAnchorRef(t.receipt); ref.Kind != "" {
		return ref.Kind, true
	}
	if t.admissionOutcome() == AnchorBindingAdmissionAdmitted && t.parent.Binding.active() {
		return t.parent.Binding.Kind, false
	}
	return "", false
}

// applyBoundAnchorKind decides, once, whether the binding's kind replaces the
// model's on the accepted reading, records the decision on the tracker, and
// returns the reading every later stage reads.
func (e *Engine) applyBoundAnchorKind(t *anchorBindingTracker, outcome QuestionFamilyOutcome) QuestionFamilyOutcome {
	if t == nil {
		return outcome
	}
	if !e.anchorBindingCountEnabled {
		t.boundKind = BoundAnchorKindSwitchOff
		return outcome
	}
	kind, fromReceipt := t.boundAnchorKind()
	switch {
	case kind == "":
		t.boundKind = BoundAnchorKindNone
		return outcome
	case kind == outcome.WinningSample.ScopeAnchorKind:
		t.boundKind = BoundAnchorKindAgrees
		return outcome
	case ScopeAnchorRetrievalKind(outcome.Frame, kind) != kind:
		t.boundKind = BoundAnchorKindRejectedByFrame
		return outcome
	}
	outcome.WinningSample.ModelScopeAnchorKind = outcome.WinningSample.ScopeAnchorKind
	outcome.WinningSample.ModelScopeAnchorKindSource = outcome.WinningSample.ScopeAnchorKindSource
	outcome.WinningSample.ScopeAnchorKind = kind
	outcome.WinningSample.ScopeAnchorKindSource = ScopeAnchorKindAnchorBinding
	t.boundKind = BoundAnchorKindAppliedCarried
	if fromReceipt {
		t.boundKind = BoundAnchorKindAppliedReceipt
	}
	return outcome
}

// boundKindDecision is the decision as the line publishes it.
func (t *anchorBindingTracker) boundKindDecision() BoundAnchorKindDecision {
	if t.boundKind == "" {
		return BoundAnchorKindNotEvaluated
	}
	return t.boundKind
}
