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
// which subject was meant and the resolver offered the remembered identity,
// that candidate is offered first.
//
// THE BINDING OWNS EVERY LEDGER-ANCHOR EFFECT, not only the hint. The
// ledger's subject_anchor entry also feeds the confirmed-anchor selection
// both resolution calls receive (confirmedAnchorSelection), the anchor-pool
// kind scope that selection narrows, and the carried-entry disclosure. With
// the switch on, that entry stays applied only when the admitted binding
// injects the SAME identity; otherwise it is withheld before the first
// resolution call and disclosed as withheld (withholdLedgerAnchor). A
// redeemed anchor receipt this turn outranks the carry: resolution commits
// exact hints before it reads the selection, so a carry injected beside a
// receipt would win against the caller's own choice.

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// BoundAnchorCarry is the closed record of what the binding carried into
// this turn's resolution.
type BoundAnchorCarry string

const (
	// BoundAnchorCarrySwitchOff: AnchorBindingCarryEnabled is off; the
	// ledger's carry runs as before.
	BoundAnchorCarrySwitchOff BoundAnchorCarry = "switch_off"
	// BoundAnchorCarryNotEvaluated: the turn ended before the carry point.
	BoundAnchorCarryNotEvaluated BoundAnchorCarry = "not_evaluated"
	// BoundAnchorCarryNone: the parent holds no binding.
	BoundAnchorCarryNone BoundAnchorCarry = "none"
	// BoundAnchorCarryNotAdmitted: the parent's binding was refused at
	// admission, or is contested; nothing is injected.
	BoundAnchorCarryNotAdmitted BoundAnchorCarry = "not_admitted"
	// BoundAnchorCarryPendingHeld: the parent's admitted binding is still
	// pending window confirmation and the pending carry is off; nothing is
	// injected.
	BoundAnchorCarryPendingHeld BoundAnchorCarry = "pending_held"
	// BoundAnchorCarryInjected: the admitted binding's identity reached
	// resolution as the carried anchor.
	BoundAnchorCarryInjected BoundAnchorCarry = "injected"
	// BoundAnchorCarryCallerSuperseded: the caller supplied a hint of the
	// binding's kind, which outranks a carry; nothing is injected.
	BoundAnchorCarryCallerSuperseded BoundAnchorCarry = "caller_superseded"
	// BoundAnchorCarryReceiptSuperseded: this turn redeemed an anchor
	// receipt, which outranks a carry; nothing is injected.
	BoundAnchorCarryReceiptSuperseded BoundAnchorCarry = "receipt_superseded"
	// BoundAnchorCarryCapFull: the carry was due, but the request already
	// holds the maximum number of subject hints; nothing is injected.
	BoundAnchorCarryCapFull BoundAnchorCarry = "cap_full"
	// BoundAnchorCarryFollowUpOffered: the parent was admitted as a follow-up
	// (anchor_binding_follow_up.go), the turn's own resolution committed
	// nothing, and the remembered identity is offered first.
	BoundAnchorCarryFollowUpOffered BoundAnchorCarry = "follow_up_offered"
	// BoundAnchorCarryFollowUpCommitted: admitted as a follow-up, and the
	// turn's own resolution committed a subject; nothing is offered.
	BoundAnchorCarryFollowUpCommitted BoundAnchorCarry = "follow_up_committed"
	// BoundAnchorCarryFollowUpFrameRefused: admitted as a follow-up, but the
	// follow-up's frame does not take the binding's kind as its scope anchor.
	BoundAnchorCarryFollowUpFrameRefused BoundAnchorCarry = "follow_up_frame_refused"
	// BoundAnchorCarryFollowUpParentMismatch: admitted as a follow-up, but the
	// parent did not serve the binding's identity, so there is no label the
	// caller already saw to offer it under.
	BoundAnchorCarryFollowUpParentMismatch BoundAnchorCarry = "follow_up_parent_mismatch"
	// BoundAnchorCarryRememberedOffered: the carried anchor was injected,
	// resolution committed nothing, and the turn's clarification offers the
	// remembered identity first.
	BoundAnchorCarryRememberedOffered BoundAnchorCarry = "remembered_offered"
)

func boundAnchorCarries() []BoundAnchorCarry {
	return []BoundAnchorCarry{
		BoundAnchorCarrySwitchOff, BoundAnchorCarryNotEvaluated, BoundAnchorCarryNone, BoundAnchorCarryNotAdmitted, BoundAnchorCarryPendingHeld,
		BoundAnchorCarryInjected, BoundAnchorCarryCallerSuperseded, BoundAnchorCarryReceiptSuperseded,
		BoundAnchorCarryCapFull, BoundAnchorCarryRememberedOffered, BoundAnchorCarryFollowUpOffered,
		BoundAnchorCarryFollowUpCommitted, BoundAnchorCarryFollowUpFrameRefused, BoundAnchorCarryFollowUpParentMismatch,
	}
}

// maxCarrySubjectHints is the ContextFabricRequestedScope.SubjectHints v1
// bound resolvePriorSubjectHints also enforces; a carry never exceeds it.
const maxCarrySubjectHints = 50

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

// bindingCarryHint decides, once and right after admission, what the
// binding carries into resolution. It returns the hint the carry is due to
// inject (ok=false when none) and records every refusal on the tracker. A
// due carry is recorded only where it is appended (recordCarryAppend), so a
// turn that exits before then reads not_evaluated and a full hint list
// reads cap_full, never injected. Switched off it decides nothing and the
// caller keeps the ledger's carry. A nil tracker carries nothing: NewEngine
// refuses the switch without the shadow binding.
func (e *Engine) bindingCarryHint(t *anchorBindingTracker, hints []contractsv1.ContextFabricSubjectHint, confirmed []confirmedStructureMember) (contractsv1.ContextFabricSubjectHint, bool) {
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
	case t.admissionOutcome() == AnchorBindingAdmissionAdmittedFollowUp:
		// Never a hint: the offer is decided after resolution
		// (anchor_binding_follow_up.go).
		return contractsv1.ContextFabricSubjectHint{}, false
	case !held.identifies():
		t.carry = BoundAnchorCarryNotAdmitted
		return contractsv1.ContextFabricSubjectHint{}, false
	case t.parent.Binding.State == AnchorBindingPendingWindowConfirmation && !e.anchorBindingPendingCarryEnabled:
		t.carry = BoundAnchorCarryPendingHeld
		return contractsv1.ContextFabricSubjectHint{}, false
	case confirmedAnchorReceipt(confirmed):
		t.carry = BoundAnchorCarryReceiptSuperseded
		return contractsv1.ContextFabricSubjectHint{}, false
	case callerSuppliedHintOfKind(hints, held.Kind):
		t.carry = BoundAnchorCarryCallerSuperseded
		return contractsv1.ContextFabricSubjectHint{}, false
	}
	return contractsv1.ContextFabricSubjectHint{
		Kind: held.Kind, ID: held.ID, Label: held.ID,
		Source: string(hintsource.EngineCommittedAnchorCarry),
	}, true
}

// confirmedAnchorReceipt reports whether this turn redeemed a subject_anchor
// receipt.
func confirmedAnchorReceipt(confirmed []confirmedStructureMember) bool {
	for _, c := range confirmed {
		if c.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			return true
		}
	}
	return false
}

// recordCarryAppend records a due carry once the engine tried to append it:
// injected when it reached resolution, cap_full when the hint list was full.
// A decision already recorded (a refusal, or switch_off) is kept.
func (t *anchorBindingTracker) recordCarryAppend(appended bool) {
	if t == nil || t.carry != "" {
		return
	}
	if appended {
		t.carry = BoundAnchorCarryInjected
		return
	}
	t.carry = BoundAnchorCarryCapFull
}

// withholdLedgerAnchor decides, with the switch on, whether the ledger's
// applied subject_anchor entry may stay applied. It stays only when the
// carry is due and names the same identity; otherwise it is withheld before
// either resolution call reads it, with the disposition the served carried
// entry reports: superseded_by_caller when the caller's own hint or receipt
// outranks it, vetoed_conflict when the binding carries a different
// identity, and vetoed_unresolved when no admitted binding proves it.
func withholdLedgerAnchor(t *anchorBindingTracker, applied map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember, hint contractsv1.ContextFabricSubjectHint, due bool) (confirmedStructureMember, contractsv1.ContextFabricStructureDisposition, bool) {
	entry, ok := applied[contractsv1.ContextFabricStructureNeedSubjectAnchor]
	if !ok {
		return confirmedStructureMember{}, "", false
	}
	switch {
	case due && entry.AppliedKind == hint.Kind && entry.AppliedValue == hint.ID:
		return confirmedStructureMember{}, "", false
	case due:
		return entry, contractsv1.ContextFabricStructureDispositionVetoedConflict, true
	case t != nil && (t.carry == BoundAnchorCarryCallerSuperseded || t.carry == BoundAnchorCarryReceiptSuperseded):
		return entry, contractsv1.ContextFabricStructureDispositionSupersededByCaller, true
	default:
		return entry, contractsv1.ContextFabricStructureDispositionVetoedUnresolved, true
	}
}

// offerRememberedAnchor puts the carried identity first among the turn's
// candidates when the carry was injected, resolution committed nothing, and
// the resolver itself offered that identity. The candidate keeps its own
// receipt, label and provider; only its place moves. The prompt is rebuilt
// when it is the generic candidate prompt (or empty, filled later from the
// same list), so it names the remembered identity first too; a turn whose
// prompt says something else keeps its order and its prompt.
func (t *anchorBindingTracker) offerRememberedAnchor(resolution SubjectResolution) SubjectResolution {
	if t == nil || t.carry != BoundAnchorCarryInjected || len(resolution.Committed) != 0 || len(resolution.Candidates) == 0 {
		return resolution
	}
	generic := resolution.ClarificationPrompt == "" || resolution.ClarificationPrompt == ClarificationPrompt(resolution.Candidates)
	if !generic {
		return resolution
	}
	held := t.carriedAnchor()
	at := -1
	for i, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == held.Kind && candidate.Subject.CanonicalID == held.ID {
			at = i
			break
		}
	}
	if at < 0 {
		return resolution
	}
	candidates := make([]SubjectCandidate, 0, len(resolution.Candidates))
	candidates = append(candidates, resolution.Candidates[at])
	for i, candidate := range resolution.Candidates {
		if i == at || (candidate.Subject.Kind == held.Kind && candidate.Subject.CanonicalID == held.ID) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	out := resolution
	out.Candidates = candidates
	if resolution.ClarificationPrompt != "" {
		out.ClarificationPrompt = ClarificationPrompt(candidates)
	}
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

// holdsPendingProof reports whether the binding a window-gated turn will
// persist is pending window confirmation, which the binder decides only on
// an identity the offers-only pass proved (a held binding always names it).
func (t *anchorBindingTracker) holdsPendingProof() bool {
	if t == nil || t.evaluation != AnchorBindingEvaluationWindowGated {
		return false
	}
	to, _ := bindAnchor(t.bindingInput(InvestigationResult{}))
	return to.State == AnchorBindingPendingWindowConfirmation
}

// withheldForUnevaluatedExit is the outgoing ledger and carried-entry
// disclosure of an exit that ends before resolution, once the binding
// withheld the ledger's anchor: the anchor entry reads its withheld
// disposition (every other entry not_evaluated, as before), and the
// outgoing ledger drops it, exactly as a post-resolution drop does.
func withheldForUnevaluatedExit(base []ConfirmedNeedEntry, entries []*contractsv1.ContextFabricConfirmedStructureEntry, withheld confirmedStructureMember, disposition contractsv1.ContextFabricStructureDisposition) ([]ConfirmedNeedEntry, []*contractsv1.ContextFabricConfirmedStructureEntry) {
	return confirmedNeedsForCaptureWithoutVetoedAnchor(base, true), carriedStructureEntriesForDecisive(carriedStructureEntriesUnevaluated(entries), withheld, true, disposition)
}
