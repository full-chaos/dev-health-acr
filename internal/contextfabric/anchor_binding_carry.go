package contextfabric

// The anchor a same-question continuation carries, owned by the binding.
//
// Before this switch the confirmed-need ledger's engine_committed
// subject_anchor entry became a synthetic hint (engineCommittedAnchorHint),
// and resolution committed it through the caller-hint exact-commit channel.
// The ledger checks the question, the request identity and the graph epoch
// of the immediate parent only.
//
// With EngineOptions.AnchorBindingCarryEnabled on, the carried anchor comes
// from the ADMITTED binding and nothing else: its identity passed the origin
// question, the age, the ownership freshness and a live re-read this turn
// (anchor_admission.go). It reaches resolution through the same channel and
// the same caller-hint contest. A refused or absent binding injects nothing,
// whatever the ledger remembers. The carried anchor is effective only when
// this turn's own resolution commits it; when the turn instead ends asking
// which subject was meant, the remembered identity is offered first, the
// same way the subject-substitution guard offers a remembered subject. A
// redeemed anchor receipt is unaffected: it reaches resolution through its
// own redemption path.

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// BoundAnchorCarry is the closed record of what the binding carried into
// this turn's resolution.
type BoundAnchorCarry string

const (
	// BoundAnchorCarrySwitchOff: AnchorBindingCarryEnabled is off (or the
	// shadow binding is off); the ledger's carry runs as before.
	BoundAnchorCarrySwitchOff BoundAnchorCarry = "switch_off"
	// BoundAnchorCarryNotEvaluated: the turn ended before the carry point.
	BoundAnchorCarryNotEvaluated BoundAnchorCarry = "not_evaluated"
	// BoundAnchorCarryNone: the parent holds no binding.
	BoundAnchorCarryNone BoundAnchorCarry = "none"
	// BoundAnchorCarryNotAdmitted: the parent's binding was refused at
	// admission, or is contested; nothing is injected.
	BoundAnchorCarryNotAdmitted BoundAnchorCarry = "not_admitted"
	// BoundAnchorCarryInjected: the admitted binding's identity reached
	// resolution as the carried anchor.
	BoundAnchorCarryInjected BoundAnchorCarry = "injected"
	// BoundAnchorCarryCallerSuperseded: the caller supplied a hint of the
	// binding's kind, which outranks a carry; nothing is injected.
	BoundAnchorCarryCallerSuperseded BoundAnchorCarry = "caller_superseded"
	// BoundAnchorCarryRememberedOffered: the carried anchor was injected,
	// resolution committed nothing, and the turn's clarification offers the
	// remembered identity first.
	BoundAnchorCarryRememberedOffered BoundAnchorCarry = "remembered_offered"
)

func boundAnchorCarries() []BoundAnchorCarry {
	return []BoundAnchorCarry{
		BoundAnchorCarrySwitchOff, BoundAnchorCarryNotEvaluated, BoundAnchorCarryNone, BoundAnchorCarryNotAdmitted,
		BoundAnchorCarryInjected, BoundAnchorCarryCallerSuperseded, BoundAnchorCarryRememberedOffered,
	}
}

// carriedAnchor is the identity the admitted binding carries, the zero ref
// when it carries none.
func (t *anchorBindingTracker) carriedAnchor() anchorRef {
	if t.admissionOutcome() != AnchorBindingAdmissionAdmitted || !t.parent.Binding.active() {
		return anchorRef{}
	}
	if t.parent.Binding.State == AnchorBindingContested {
		return anchorRef{}
	}
	return anchorRef{Kind: t.parent.Binding.Kind, ID: t.parent.Binding.CanonicalID}
}

// bindingCarryHint decides, once, what the binding carries into resolution.
// It returns the hint to inject (ok=false when none) and records the
// decision on the tracker. Switched off it decides nothing and the caller
// keeps the ledger's carry.
func (e *Engine) bindingCarryHint(t *anchorBindingTracker, hints []contractsv1.ContextFabricSubjectHint) (contractsv1.ContextFabricSubjectHint, bool) {
	if t == nil {
		return contractsv1.ContextFabricSubjectHint{}, false
	}
	if !e.anchorBindingCarryEnabled {
		t.carry = BoundAnchorCarrySwitchOff
		return contractsv1.ContextFabricSubjectHint{}, false
	}
	held := t.carriedAnchor()
	switch {
	case !(t.parent.Status == AnchorBindingParentPresent && t.parent.Binding.active()):
		t.carry = BoundAnchorCarryNone
		return contractsv1.ContextFabricSubjectHint{}, false
	case !held.identifies():
		t.carry = BoundAnchorCarryNotAdmitted
		return contractsv1.ContextFabricSubjectHint{}, false
	case callerSuppliedHintOfKind(hints, held.Kind):
		t.carry = BoundAnchorCarryCallerSuperseded
		return contractsv1.ContextFabricSubjectHint{}, false
	}
	t.carry = BoundAnchorCarryInjected
	return contractsv1.ContextFabricSubjectHint{
		Kind: held.Kind, ID: held.ID, Label: held.ID,
		Source: string(hintsource.EngineCommittedAnchorCarry),
	}, true
}

// offerRememberedAnchor puts the carried identity first among the turn's
// candidates when the carry was injected and resolution committed nothing
// but still offers candidates to choose from. The offer is the identity the
// admission re-read just proved readable; its receipt is minted from the
// result that proved it.
func (t *anchorBindingTracker) offerRememberedAnchor(resolution SubjectResolution) SubjectResolution {
	if t == nil || t.carry != BoundAnchorCarryInjected || len(resolution.Committed) != 0 || len(resolution.Candidates) == 0 {
		return resolution
	}
	held := t.carriedAnchor()
	subject := SubjectRef{Kind: held.Kind, CanonicalID: held.ID, Label: held.ID}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == held.Kind && candidate.Subject.CanonicalID == held.ID {
			subject.Label = candidate.Subject.Label
		}
	}
	remembered := SubjectCandidate{
		ReceiptID:      subjectSubstitutionReceiptID(t.parent.Binding.OriginResultID, subject),
		Subject:        subject,
		State:          contractsv1.ContextFabricResolutionAmbiguous,
		MatchedTerms:   []string{},
		MatchReasons:   []string{"the subject the earlier answer to this question was about"},
		Confidence:     0,
		EvidenceRefIDs: []string{},
		Provider:       providerOfSubject(resolution.Candidates, subject),
	}
	candidates := []SubjectCandidate{remembered}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == held.Kind && candidate.Subject.CanonicalID == held.ID {
			continue
		}
		candidates = append(candidates, candidate)
	}
	out := resolution
	out.Candidates = candidates
	t.carry = BoundAnchorCarryRememberedOffered
	return out
}

// carryDecision is the decision as the line publishes it.
func (t *anchorBindingTracker) carryDecision() BoundAnchorCarry {
	if t.carry == "" {
		return BoundAnchorCarryNotEvaluated
	}
	return t.carry
}
