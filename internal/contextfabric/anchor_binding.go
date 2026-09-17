package contextfabric

// CHAOS-5884: the accepted scope anchor as ONE typed binding, computed in
// shadow beside the anchor decisions the engine serves from.
//
// INVARIANTS.
//   - Shadow only. Nothing served reads the binding: the served document, the
//     outgoing confirmed-need ledger and every public field are the same bytes
//     with the shadow on or off. A disagreement between the binding and the
//     served decision is DATA on the transition line, never a behaviour.
//   - One binder. bindAnchor is a pure function of the inputs the turn already
//     computed: the parent's persisted binding, the model's stated kinds, a
//     redeemed anchor receipt, the caller's own subject hints, and the
//     identity-proven commits of the resolution that ran (the offers-only
//     resolution a window gate discards included).
//   - The binding's kind outranks the current model kind. A carried or
//     receipt-confirmed kind decides which committed subject can be the
//     anchor; the model's kind is a proposal, used only when nothing is
//     carried.
//   - Silence preserves. A carried binding survives a turn whose resolution
//     neither re-proves it nor proves a distinct identity. A distinct identity
//     proven without a caller choice contests it; it never replaces it.
//   - A window gate keeps proof as pending. An identity-proven anchor an
//     offers-only resolution found is recorded pending_window_confirmation,
//     and becomes bound on the first later turn that passes the gate.
//   - Additive persistence. The binding rides in the semantic snapshot as its
//     "anchor_binding" extension member, decoded only here; a row without it
//     reads back with no binding, a member that does not decode or validate
//     reports the parent binding invalid while the reading stays available,
//     and a binding that would make the snapshot unencodable is left out
//     while the capture stays exactly what it was.
//   - One line per decision. Every Save and every reuse serve emits exactly
//     one transition line carrying the pre-entry state, the proposal, the
//     decision with its reason, and the served decision it is compared to.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AnchorBindingTransitionLogMessage is the msg of the transition Info line.
const AnchorBindingTransitionLogMessage = "context fabric anchor binding transition"

// AnchorBindingState is the closed state of the binding.
type AnchorBindingState string

const (
	// AnchorBindingUnbound: no anchor is held.
	AnchorBindingUnbound AnchorBindingState = "unbound"
	// AnchorBindingBound: the anchor is held and effective.
	AnchorBindingBound AnchorBindingState = "bound"
	// AnchorBindingPendingWindowConfirmation: identity was proven on a turn
	// the window gate ended; the anchor is held but not yet effective.
	AnchorBindingPendingWindowConfirmation AnchorBindingState = "pending_window_confirmation"
	// AnchorBindingContested: the held anchor is retained, and a distinct
	// identity proven without a caller choice contests it. Not effective.
	AnchorBindingContested AnchorBindingState = "contested"
)

func anchorBindingStates() []AnchorBindingState {
	return []AnchorBindingState{AnchorBindingUnbound, AnchorBindingBound, AnchorBindingPendingWindowConfirmation, AnchorBindingContested}
}

// AnchorBindingProof is the closed basis the held anchor stands on.
type AnchorBindingProof string

const (
	AnchorBindingProofNone           AnchorBindingProof = "none"
	AnchorBindingProofCallerReceipt  AnchorBindingProof = "caller_receipt"
	AnchorBindingProofCallerHint     AnchorBindingProof = "caller_hint"
	AnchorBindingProofIdentityProven AnchorBindingProof = "identity_proven"
)

func anchorBindingProofs() []AnchorBindingProof {
	return []AnchorBindingProof{AnchorBindingProofNone, AnchorBindingProofCallerReceipt, AnchorBindingProofCallerHint, AnchorBindingProofIdentityProven}
}

// AnchorBindingReason is the closed reason for one transition.
type AnchorBindingReason string

const (
	// AnchorBindingReasonNoProof: nothing is carried and nothing was proven.
	AnchorBindingReasonNoProof AnchorBindingReason = "no_proof"
	// AnchorBindingReasonIdentityProven: this turn's resolution proved one
	// anchor identity with nothing carried.
	AnchorBindingReasonIdentityProven AnchorBindingReason = "identity_proven"
	// AnchorBindingReasonCallerReceipt: the caller redeemed an anchor receipt.
	AnchorBindingReasonCallerReceipt AnchorBindingReason = "caller_receipt"
	// AnchorBindingReasonCallerHint: a caller-supplied hint committed as the
	// one anchor, with nothing carried.
	AnchorBindingReasonCallerHint AnchorBindingReason = "caller_hint"
	// AnchorBindingReasonCarriedReconfirmed: resolution committed the carried
	// identity again.
	AnchorBindingReasonCarriedReconfirmed AnchorBindingReason = "carried_reconfirmed"
	// AnchorBindingReasonCarriedSilent: resolution ran and neither re-proved
	// nor contradicted the carried identity.
	AnchorBindingReasonCarriedSilent AnchorBindingReason = "carried_silent"
	// AnchorBindingReasonCarriedNotEvaluated: the turn ended before a
	// resolution could re-prove or contradict the carried identity.
	AnchorBindingReasonCarriedNotEvaluated AnchorBindingReason = "carried_not_evaluated"
	// AnchorBindingReasonReplacedByCaller: the caller chose a different
	// identity (receipt or hint) over the carried one.
	AnchorBindingReasonReplacedByCaller AnchorBindingReason = "replaced_by_caller"
	// AnchorBindingReasonContestedByResolution: a distinct identity was
	// proven without a caller choice while one was carried.
	AnchorBindingReasonContestedByResolution AnchorBindingReason = "contested_by_resolution"
	// AnchorBindingReasonAmbiguousProof: more than one distinct anchor
	// identity was proven.
	AnchorBindingReasonAmbiguousProof AnchorBindingReason = "ambiguous_proof"
	// AnchorBindingReasonPendingWindowConfirmation: the window gate ended the
	// turn after its offers-only resolution proved one anchor identity.
	AnchorBindingReasonPendingWindowConfirmation AnchorBindingReason = "pending_window_confirmation"
	// AnchorBindingReasonWindowConfirmed: a pending anchor reached a turn
	// that passed the window gate.
	AnchorBindingReasonWindowConfirmed AnchorBindingReason = "window_confirmed"
	// AnchorBindingReasonUnrecorded: a Save reached persistence with no
	// binding decision attached. Never expected; the parity test fails on it.
	AnchorBindingReasonUnrecorded AnchorBindingReason = "unrecorded"
)

func anchorBindingReasons() []AnchorBindingReason {
	return []AnchorBindingReason{
		AnchorBindingReasonNoProof, AnchorBindingReasonIdentityProven, AnchorBindingReasonCallerReceipt,
		AnchorBindingReasonCallerHint, AnchorBindingReasonCarriedReconfirmed, AnchorBindingReasonCarriedSilent,
		AnchorBindingReasonCarriedNotEvaluated, AnchorBindingReasonReplacedByCaller, AnchorBindingReasonContestedByResolution,
		AnchorBindingReasonAmbiguousProof, AnchorBindingReasonPendingWindowConfirmation, AnchorBindingReasonWindowConfirmed,
		AnchorBindingReasonUnrecorded,
	}
}

// AnchorBinding is the persisted binding. Kind and CanonicalID are set
// exactly when State is not unbound; ContenderKind and ContenderID exactly
// when State is contested. OriginResultID is the result whose turn proved
// the held identity. GraphEpoch is the graph epoch that proof stood on.
type AnchorBinding struct {
	State          AnchorBindingState  `json:"state"`
	Kind           SubjectKind         `json:"kind,omitempty"`
	CanonicalID    string              `json:"canonical_id,omitempty"`
	Proof          AnchorBindingProof  `json:"proof"`
	Reason         AnchorBindingReason `json:"reason"`
	OriginResultID string              `json:"origin_result_id,omitempty"`
	GraphEpoch     int64               `json:"graph_epoch"`
	ContenderKind  SubjectKind         `json:"contender_kind,omitempty"`
	ContenderID    string              `json:"contender_id,omitempty"`
}

// active reports whether the binding holds an identity.
func (b AnchorBinding) active() bool {
	return b.State == AnchorBindingBound || b.State == AnchorBindingPendingWindowConfirmation || b.State == AnchorBindingContested
}

// effective is the anchor a consumer of the binding would bind to: the held
// identity when bound, nothing otherwise.
func (b AnchorBinding) effective() anchorRef {
	if b.State != AnchorBindingBound {
		return anchorRef{}
	}
	return anchorRef{Kind: b.Kind, ID: b.CanonicalID}
}

var errAnchorBindingInvalid = errors.New("anchor binding invalid")

// ValidateAnchorBinding checks the binding's closed vocabularies, bounds and
// cross-field consistency.
func ValidateAnchorBinding(b AnchorBinding) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", errAnchorBindingInvalid, fmt.Sprintf(format, args...))
	}
	if !memberOf(anchorBindingStates(), b.State) {
		return invalid("state %q is not a vocabulary member", b.State)
	}
	if !memberOf(anchorBindingProofs(), b.Proof) {
		return invalid("proof %q is not a vocabulary member", b.Proof)
	}
	if !memberOf(anchorBindingReasons(), b.Reason) || b.Reason == AnchorBindingReasonUnrecorded {
		return invalid("reason %q is not a persistable vocabulary member", b.Reason)
	}
	for _, field := range []struct{ name, value string }{{"canonical_id", b.CanonicalID}, {"contender_id", b.ContenderID}, {"origin_result_id", b.OriginResultID}} {
		if len(field.value) > SemanticStateMaxTermBytes {
			return invalid("%s is %d bytes, exceeds %d", field.name, len(field.value), SemanticStateMaxTermBytes)
		}
	}
	if b.GraphEpoch < 0 {
		return invalid("graph_epoch %d is negative", b.GraphEpoch)
	}
	if !b.active() {
		if b.Kind != "" || b.CanonicalID != "" || b.Proof != AnchorBindingProofNone || b.ContenderKind != "" || b.ContenderID != "" {
			return invalid("an unbound binding carries an identity, a proof or a contender")
		}
		return nil
	}
	if !contractsv1.ValidContextFabricSubjectKind(b.Kind) || b.CanonicalID == "" || b.Proof == AnchorBindingProofNone {
		return invalid("a held binding needs a vocabulary kind, a canonical id and a proof")
	}
	if b.State != AnchorBindingContested {
		if b.ContenderKind != "" || b.ContenderID != "" {
			return invalid("only a contested binding carries a contender")
		}
		return nil
	}
	if !contractsv1.ValidContextFabricSubjectKind(b.ContenderKind) || b.ContenderID == "" {
		return invalid("a contested binding needs a vocabulary contender kind and id")
	}
	if b.ContenderKind == b.Kind && b.ContenderID == b.CanonicalID {
		return invalid("a contested binding's contender is the held identity")
	}
	return nil
}

func memberOf[T comparable](members []T, value T) bool {
	for _, member := range members {
		if member == value {
			return true
		}
	}
	return false
}

// anchorRef is one (kind, canonical id) pair; the zero value is "none".
type anchorRef struct {
	Kind SubjectKind
	ID   string
}

func (r anchorRef) none() bool { return r.ID == "" }

// AnchorBindingParentStatus is the closed status of the parent's binding.
type AnchorBindingParentStatus string

const (
	AnchorBindingParentNoReference     AnchorBindingParentStatus = "no_reference"
	AnchorBindingParentUnloadable      AnchorBindingParentStatus = "unloadable"
	AnchorBindingParentAbsent          AnchorBindingParentStatus = "absent"
	AnchorBindingParentInvalid         AnchorBindingParentStatus = "invalid"
	AnchorBindingParentStaleGraphEpoch AnchorBindingParentStatus = "stale_graph_epoch"
	AnchorBindingParentPresent         AnchorBindingParentStatus = "present"
)

func anchorBindingParentStatuses() []AnchorBindingParentStatus {
	return []AnchorBindingParentStatus{AnchorBindingParentNoReference, AnchorBindingParentUnloadable, AnchorBindingParentAbsent, AnchorBindingParentInvalid, AnchorBindingParentStaleGraphEpoch, AnchorBindingParentPresent}
}

// AnchorBindingEvaluation is the closed reach of the turn that decided.
type AnchorBindingEvaluation string

const (
	// AnchorBindingEvaluationNotResolved: the turn ended before any subject
	// resolution produced proof.
	AnchorBindingEvaluationNotResolved AnchorBindingEvaluation = "not_resolved"
	// AnchorBindingEvaluationWindowGated: the window gate ended the turn;
	// proof is the offers-only resolution's.
	AnchorBindingEvaluationWindowGated AnchorBindingEvaluation = "window_gated"
	// AnchorBindingEvaluationResolved: the decisive resolution ran.
	AnchorBindingEvaluationResolved AnchorBindingEvaluation = "resolved"
	// AnchorBindingEvaluationReused: a reuse hit served a stored row.
	AnchorBindingEvaluationReused AnchorBindingEvaluation = "reused"
)

func anchorBindingEvaluations() []AnchorBindingEvaluation {
	return []AnchorBindingEvaluation{AnchorBindingEvaluationNotResolved, AnchorBindingEvaluationWindowGated, AnchorBindingEvaluationResolved, AnchorBindingEvaluationReused}
}

// AnchorBindingAgreement is the closed comparison with the served decision.
type AnchorBindingAgreement string

const (
	AnchorBindingAgree        AnchorBindingAgreement = "agree"
	AnchorBindingDisagree     AnchorBindingAgreement = "disagree"
	AnchorBindingNotEvaluated AnchorBindingAgreement = "not_evaluated"
)

// AnchorBindingDisagreementField names the first served field that differs.
type AnchorBindingDisagreementField string

const (
	AnchorBindingFieldNone AnchorBindingDisagreementField = "none"
	// AnchorBindingFieldPendingProof: the binding holds a pending proof the
	// served state discarded.
	AnchorBindingFieldPendingProof AnchorBindingDisagreementField = "pending_proof"
	// AnchorBindingFieldCarriedAnchor: the served outgoing ledger's anchor
	// differs from the binding's effective anchor.
	AnchorBindingFieldCarriedAnchor AnchorBindingDisagreementField = "carried_anchor"
	// AnchorBindingFieldCountAnchor: the served count-population decision's
	// anchor differs from the binding's effective anchor.
	AnchorBindingFieldCountAnchor AnchorBindingDisagreementField = "count_anchor"
)

// AnchorBindingPersistence is the closed fate of the binding at its Save.
type AnchorBindingPersistence string

const (
	// AnchorBindingStateAbsent: the Save carried no semantic snapshot, so
	// there was nothing to attach the binding to.
	AnchorBindingStateAbsent AnchorBindingPersistence = "state_absent"
	// AnchorBindingUnencodable: the snapshot with the binding attached does
	// not encode (over the cap, or a string the store cannot hold); the
	// capture was saved without it.
	AnchorBindingUnencodable AnchorBindingPersistence = "binding_unencodable"
	// AnchorBindingNotSaved: no Save happened (reuse serve).
	AnchorBindingNotSaved AnchorBindingPersistence = "not_saved"
)

// anchorBindingUndeclared is the token the transition line writes for a
// value outside its key's closed vocabulary. It is declared with every closed
// key and never produced by the binder.
const anchorBindingUndeclared = "undeclared"

// AnchorBindingUndeclaredToken exports anchorBindingUndeclared for the
// eventspec declaration.
const AnchorBindingUndeclaredToken = anchorBindingUndeclared

// AnchorBindingCarryChecks is the closed statement of which admission checks
// a carried binding passed before the binder used it. The shadow carries on
// the parent reference alone: no same-question admission, no live
// re-authorization of the carried identity.
type AnchorBindingCarryChecks string

const (
	// AnchorBindingCarryChecksNotApplicable: no parent binding was used.
	AnchorBindingCarryChecksNotApplicable AnchorBindingCarryChecks = "not_applicable"
	// AnchorBindingCarryChecksNotEvaluated: a parent binding was used, and
	// neither admission check was evaluated.
	AnchorBindingCarryChecksNotEvaluated AnchorBindingCarryChecks = "not_evaluated"
)

// storedEpoch is the parent binding's graph epoch for the line, -1 when the
// parent carries no binding that was read.
func (p anchorBindingParent) storedEpoch() int64 {
	switch p.Status {
	case AnchorBindingParentInvalid, AnchorBindingParentStaleGraphEpoch, AnchorBindingParentPresent:
		return p.StoredEpoch
	default:
		return -1
	}
}

func (p anchorBindingParent) carryChecks() AnchorBindingCarryChecks {
	if p.Status == AnchorBindingParentPresent {
		return AnchorBindingCarryChecksNotEvaluated
	}
	return AnchorBindingCarryChecksNotApplicable
}

// AnchorBindingTransitionLineVocabulary is the transition line's closed
// vocabulary for one key, read from the producers. The eventspec declaration
// reads this one list.
func AnchorBindingTransitionLineVocabulary(key string) []string {
	strs := func(n int, at func(int) string) []string {
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, at(i))
		}
		return out
	}
	switch key {
	case "from_state", "to_state":
		states := anchorBindingStates()
		return strs(len(states), func(i int) string { return string(states[i]) })
	case "proof", "from_proof":
		proofs := anchorBindingProofs()
		return strs(len(proofs), func(i int) string { return string(proofs[i]) })
	case "reason", "from_reason":
		reasons := anchorBindingReasons()
		return strs(len(reasons), func(i int) string { return string(reasons[i]) })
	case "parent_binding":
		statuses := anchorBindingParentStatuses()
		return strs(len(statuses), func(i int) string { return string(statuses[i]) })
	case "evaluation":
		evaluations := anchorBindingEvaluations()
		return strs(len(evaluations), func(i int) string { return string(evaluations[i]) })
	case "shadow_agreement":
		return []string{string(AnchorBindingAgree), string(AnchorBindingDisagree), string(AnchorBindingNotEvaluated)}
	case "disagreement_field":
		return []string{string(AnchorBindingFieldNone), string(AnchorBindingFieldPendingProof), string(AnchorBindingFieldCarriedAnchor), string(AnchorBindingFieldCountAnchor)}
	case "persisted":
		out := []string{}
		for _, decision := range semanticStatePersistenceDecisions() {
			out = append(out, string(decision))
		}
		return append(out, string(AnchorBindingStateAbsent), string(AnchorBindingUnencodable), string(AnchorBindingNotSaved))
	case "site":
		out := []string{}
		for _, stage := range BudgetAssertStageVocabulary() {
			out = append(out, string(stage))
		}
		return out
	case "served_count_decision":
		// Only a children_of_scope count is compared, so the organization and
		// frame-absent decisions never reach the line.
		return []string{"not_evaluated", string(CountPopulationScopeAnchorCommitted), string(CountPopulationScopeAnchorAmbiguous), string(CountPopulationScopeAnchorUnresolved)}
	case "carry_checks":
		return []string{string(AnchorBindingCarryChecksNotApplicable), string(AnchorBindingCarryChecksNotEvaluated)}
	default:
		return nil
	}
}

// anchorBindingInput is everything bindAnchor reads.
type anchorBindingInput struct {
	From       AnchorBinding
	Evaluation AnchorBindingEvaluation
	// Frame and ModelAnchorKind are the accepted reading and the winning
	// sample's stated scope anchor kind.
	Frame           *QuestionFrame
	ModelAnchorKind SubjectKind
	// Receipt is this turn's redeemed subject_anchor, nil when none.
	Receipt *confirmedStructureMember
	// CallerHints are the caller-sourced subject hints resolution received,
	// the engine's own carry hint excluded.
	CallerHints []SubjectHint
	// Resolution and Bases are the proof: the decisive resolution restricted
	// to what the saved document still commits, the offers-only resolution
	// on a window-gated turn, or the replayed row's resolution on a reuse
	// serve.
	Resolution SubjectResolution
	Bases      CommitBasisSet
	// ResultID and GraphEpoch stamp an identity this turn proves.
	ResultID   string
	GraphEpoch int64
}

// anchorBindingProposal is what the binder measured before deciding.
type anchorBindingProposal struct {
	EffectiveKind SubjectKind
	Proven        []anchorRef
	// Contradicting are identities the turn proved under their OWN kind that
	// the effective kind excludes. They can never be bound, and they are
	// never silence: a proof of another kind contradicts the held anchor.
	Contradicting []anchorRef
}

// bindAnchor is the one binder. PURE.
func bindAnchor(in anchorBindingInput) (AnchorBinding, anchorBindingProposal) {
	to, proposal := bindAnchorOnProof(in)
	if in.Evaluation != AnchorBindingEvaluationReused || !to.active() || to.State == AnchorBindingContested {
		return to, proposal
	}
	// A reuse serve runs no resolution, so a caller hint the replayed row
	// never committed is unproven: one of the held kind naming another
	// identity contests the held anchor. It never replaces it.
	// A decided binding is the only proven anchor of its kind -- two would
	// have left it unbound or contested above -- so a hint of that kind
	// naming another identity is unproven by construction.
	for _, hint := range in.CallerHints {
		ref := anchorRef{Kind: hint.Kind, ID: hint.ID}
		if ref.Kind != to.Kind || ref.ID == to.CanonicalID {
			continue
		}
		to.State, to.Reason, to.ContenderKind, to.ContenderID = AnchorBindingContested, AnchorBindingReasonAmbiguousProof, ref.Kind, ref.ID
		return to, proposal
	}
	return to, proposal
}

// bindAnchorOnProof decides from the proof alone. A reuse serve is decided
// exactly as a resolved turn over the replayed row's resolution.
func bindAnchorOnProof(in anchorBindingInput) (AnchorBinding, anchorBindingProposal) {
	from := in.From
	carried := from.active()
	var proposal anchorBindingProposal
	switch {
	case in.Receipt != nil:
		proposal.EffectiveKind = in.Receipt.AppliedKind
	case carried:
		proposal.EffectiveKind = from.Kind
	default:
		proposal.EffectiveKind = ScopeAnchorRetrievalKind(in.Frame, in.ModelAnchorKind)
	}
	if in.Evaluation != AnchorBindingEvaluationNotResolved {
		proposal.Proven = provenAnchors(in.Frame, proposal.EffectiveKind, in.Resolution, in.Bases)
		for _, ref := range provenAnchors(in.Frame, "", in.Resolution, in.Bases) {
			if !memberOf(proposal.Proven, ref) {
				proposal.Contradicting = append(proposal.Contradicting, ref)
			}
		}
	}
	fresh := func(ref anchorRef, proof AnchorBindingProof, reason AnchorBindingReason) AnchorBinding {
		return AnchorBinding{State: AnchorBindingBound, Kind: ref.Kind, CanonicalID: ref.ID, Proof: proof, Reason: reason, OriginResultID: in.ResultID, GraphEpoch: in.GraphEpoch}
	}
	keep := func(state AnchorBindingState, reason AnchorBindingReason) AnchorBinding {
		kept := from
		kept.State, kept.Reason = state, reason
		if state != AnchorBindingContested {
			kept.ContenderKind, kept.ContenderID = "", ""
		}
		return kept
	}
	unbound := func(reason AnchorBindingReason) AnchorBinding {
		return AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: reason, GraphEpoch: in.GraphEpoch}
	}
	contest := func(contender anchorRef, reason AnchorBindingReason) AnchorBinding {
		kept := keep(AnchorBindingContested, reason)
		kept.ContenderKind, kept.ContenderID = contender.Kind, contender.ID
		return kept
	}

	if in.Receipt != nil && in.Receipt.AppliedValue != "" {
		ref := anchorRef{Kind: in.Receipt.AppliedKind, ID: in.Receipt.AppliedValue}
		reason := AnchorBindingReasonCallerReceipt
		if carried && (from.Kind != ref.Kind || from.CanonicalID != ref.ID) {
			reason = AnchorBindingReasonReplacedByCaller
		}
		return fresh(ref, AnchorBindingProofCallerReceipt, reason), proposal
	}

	switch in.Evaluation {
	case AnchorBindingEvaluationWindowGated:
		if carried {
			if contender, ok := firstOther(proposal, anchorRef{Kind: from.Kind, ID: from.CanonicalID}); ok {
				return contest(contender, AnchorBindingReasonContestedByResolution), proposal
			}
			return keep(from.State, AnchorBindingReasonCarriedNotEvaluated), proposal
		}
		switch len(proposal.Proven) {
		case 0:
			return unbound(AnchorBindingReasonNoProof), proposal
		case 1:
			pending := fresh(proposal.Proven[0], AnchorBindingProofIdentityProven, AnchorBindingReasonPendingWindowConfirmation)
			pending.State = AnchorBindingPendingWindowConfirmation
			return pending, proposal
		default:
			return unbound(AnchorBindingReasonAmbiguousProof), proposal
		}
	case AnchorBindingEvaluationResolved, AnchorBindingEvaluationReused:
	default:
		if carried {
			return keep(from.State, AnchorBindingReasonCarriedNotEvaluated), proposal
		}
		return unbound(AnchorBindingReasonNoProof), proposal
	}

	caller := map[anchorRef]bool{}
	for _, hint := range in.CallerHints {
		caller[anchorRef{Kind: hint.Kind, ID: hint.ID}] = true
	}
	if !carried {
		switch len(proposal.Proven) {
		case 0:
			if len(proposal.Contradicting) > 1 {
				return unbound(AnchorBindingReasonAmbiguousProof), proposal
			}
			return unbound(AnchorBindingReasonNoProof), proposal
		case 1:
			if caller[proposal.Proven[0]] {
				return fresh(proposal.Proven[0], AnchorBindingProofCallerHint, AnchorBindingReasonCallerHint), proposal
			}
			return fresh(proposal.Proven[0], AnchorBindingProofIdentityProven, AnchorBindingReasonIdentityProven), proposal
		default:
			return unbound(AnchorBindingReasonAmbiguousProof), proposal
		}
	}

	held := anchorRef{Kind: from.Kind, ID: from.CanonicalID}
	reproven := memberOf(proposal.Proven, held)
	var others, callerOthers []anchorRef
	for _, ref := range proposal.Proven {
		if ref == held {
			continue
		}
		others = append(others, ref)
		if caller[ref] {
			callerOthers = append(callerOthers, ref)
		}
	}
	switch {
	// TWO PROVED IDENTITIES ARE NEVER BOUND, whoever named them: the turn
	// proved more than one anchor, which is exactly what the served count
	// reports as ambiguous. A caller's own choice does not break that tie.
	case len(proposal.Proven) > 1:
		return contest(others[0], AnchorBindingReasonAmbiguousProof), proposal
	case len(proposal.Contradicting) > 0:
		return contest(proposal.Contradicting[0], AnchorBindingReasonContestedByResolution), proposal
	case len(callerOthers) == 1:
		return fresh(callerOthers[0], AnchorBindingProofCallerHint, AnchorBindingReasonReplacedByCaller), proposal
	case len(others) > 0:
		return contest(others[0], AnchorBindingReasonContestedByResolution), proposal
	case from.State == AnchorBindingPendingWindowConfirmation:
		return keep(AnchorBindingBound, AnchorBindingReasonWindowConfirmed), proposal
	case reproven:
		return keep(AnchorBindingBound, AnchorBindingReasonCarriedReconfirmed), proposal
	default:
		return keep(from.State, AnchorBindingReasonCarriedSilent), proposal
	}
}

// firstOther is the first proved or contradicting identity that is not held.
func firstOther(proposal anchorBindingProposal, held anchorRef) (anchorRef, bool) {
	for _, ref := range append(append([]anchorRef{}, proposal.Proven...), proposal.Contradicting...) {
		if ref != held {
			return ref, true
		}
	}
	return anchorRef{}, false
}

// provenAnchors lists, in commit order and without duplicates, every
// committed subject anchorBound admits as the frame's anchor under kind.
// Every admitted subject is committed on a caller canonical id or an
// identity-proven basis matching an anchor term; a statistical commit is
// never proof.
func provenAnchors(frame *QuestionFrame, kind SubjectKind, resolution SubjectResolution, bases CommitBasisSet) []anchorRef {
	memberKind := SubjectKind("")
	if frame != nil {
		memberKind, _ = frame.SubjectExpression.MemberKind()
	}
	seen := map[anchorRef]bool{}
	var out []anchorRef
	for _, subject := range resolution.Committed {
		if subject.Kind == memberKind {
			continue
		}
		if !anchorBound(frame, kind, subject, resolution, bases) {
			continue
		}
		ref := anchorRef{Kind: subject.Kind, ID: subject.CanonicalID}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// anchorBindingParent is the parent's binding as this turn reads it.
type anchorBindingParent struct {
	ResultID string
	Status   AnchorBindingParentStatus
	Binding  AnchorBinding
	// StoredEpoch is the graph epoch of the binding the parent row carries;
	// read only when Status is invalid, stale_graph_epoch or present.
	StoredEpoch int64
}

// from is the binding a transition starts from: the parent's when present,
// unbound otherwise.
func (p anchorBindingParent) from() AnchorBinding {
	if p.Status == AnchorBindingParentPresent {
		return p.Binding
	}
	return AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonNoProof}
}

// readAnchorBindingParent reads the parent's binding from the request's carry
// memo only. It never calls the store: the confirmed-need ledger has already
// loaded the same parent through the same memo, and a parent that load could
// not read is unloadable here too.
func readAnchorBindingParent(ctx context.Context, request InvestigationRequest, epoch int64) anchorBindingParent {
	parent := carryParentSeed(request)
	if parent == "" {
		return anchorBindingParent{Status: AnchorBindingParentNoReference}
	}
	stored, ok := carryPeekResult(ctx, parent)
	if !ok || stored.SemanticStateRead != SemanticStateReadAvailable || stored.SemanticState == nil {
		return anchorBindingParent{ResultID: parent, Status: AnchorBindingParentUnloadable}
	}
	return anchorBindingParentOf(parent, stored.SemanticState, epoch)
}

func anchorBindingParentOf(parent string, state *PersistedSemanticState, epoch int64) anchorBindingParent {
	binding, present, err := storedAnchorBinding(state)
	switch {
	case !present:
		return anchorBindingParent{ResultID: parent, Status: AnchorBindingParentAbsent}
	case err != nil:
		return anchorBindingParent{ResultID: parent, Status: AnchorBindingParentInvalid, StoredEpoch: -1}
	}
	if ValidateAnchorBinding(binding) != nil {
		return anchorBindingParent{ResultID: parent, Status: AnchorBindingParentInvalid, StoredEpoch: binding.GraphEpoch}
	}
	if binding.GraphEpoch != epoch {
		return anchorBindingParent{ResultID: parent, Status: AnchorBindingParentStaleGraphEpoch, StoredEpoch: binding.GraphEpoch}
	}
	return anchorBindingParent{ResultID: parent, Status: AnchorBindingParentPresent, Binding: binding, StoredEpoch: binding.GraphEpoch}
}

// anchorBindingTracker collects, across one Investigate call, the inputs the
// binder reads. Each exit's capture carries a pointer to it, and the Save
// decides from the values as they stand at that exit. A nil tracker is the
// shadow switched off.
type anchorBindingTracker struct {
	parent          anchorBindingParent
	epoch           int64
	evaluation      AnchorBindingEvaluation
	frame           *QuestionFrame
	modelAnchorKind SubjectKind
	readingSeen     bool
	receipt         *confirmedStructureMember
	callerHints     []SubjectHint
	resolution      SubjectResolution
	bases           CommitBasisSet
	servedCount     *CountPopulationScope
}

// newAnchorBindingTracker returns nil when the shadow is off.
func (e *Engine) newAnchorBindingTracker(ctx context.Context, request InvestigationRequest, binding ResolvedGraphBinding) *anchorBindingTracker {
	if e.anchorBindingShadowDisabled {
		return nil
	}
	return &anchorBindingTracker{
		parent:      readAnchorBindingParent(ctx, request, binding.Epoch),
		epoch:       binding.Epoch,
		evaluation:  AnchorBindingEvaluationNotResolved,
		callerHints: append([]SubjectHint(nil), request.RequestedScope.SubjectHints...),
	}
}

// observeAnchorVeto drops a redeemed anchor receipt the public decision
// vetoed: a binding never asserts an anchor the served decision refused.
func (t *anchorBindingTracker) observeAnchorVeto(vetoed []contractsv1.ContextFabricStructureNeedKind) {
	if t == nil || t.receipt == nil {
		return
	}
	for _, member := range vetoed {
		if member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.receipt = nil
			return
		}
	}
}

func (t *anchorBindingTracker) observeReceipt(confirmed []confirmedStructureMember) {
	if t == nil {
		return
	}
	for i := range confirmed {
		if confirmed[i].Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			member := confirmed[i]
			t.receipt = &member
			return
		}
	}
}

func (t *anchorBindingTracker) observeReading(outcome QuestionFamilyOutcome, callerHints []SubjectHint) {
	if t == nil {
		return
	}
	t.readingSeen = true
	t.frame = outcome.Frame
	t.modelAnchorKind = outcome.WinningSample.ScopeAnchorKind
	t.callerHints = append([]SubjectHint(nil), callerHints...)
}

func (t *anchorBindingTracker) observeResolution(evaluation AnchorBindingEvaluation, resolution SubjectResolution, bases CommitBasisSet) {
	if t == nil {
		return
	}
	t.evaluation = evaluation
	t.resolution = resolution
	t.bases = bases
}

func (t *anchorBindingTracker) observeServedCount(scope CountPopulationScope) {
	if t == nil {
		return
	}
	t.servedCount = &scope
}

// AnchorBindingTransitionEvent is one transition line.
type AnchorBindingTransitionEvent struct {
	ResultID       string
	ParentResultID string
	Site           BudgetAssertStage
	Evaluation     AnchorBindingEvaluation
	ParentBinding  AnchorBindingParentStatus
	CarryChecks    AnchorBindingCarryChecks
	From           AnchorBinding
	// ParentGraphEpoch is the parent binding's graph epoch, -1 when none was
	// read; GraphEpoch is this turn's.
	ParentGraphEpoch int64
	GraphEpoch       int64
	// Proposal.
	FrameExpressionKind SubjectExpressionKind
	AnchorTermCount     int
	CommittedSubjects   []string
	ModelAnchorKind     SubjectKind
	NamedExpectedKind   SubjectKind
	ReceiptAnchor       anchorRef
	CallerHintIDs       []string
	ProvenAnchorIDs     []string
	EffectiveKind       SubjectKind
	// Decision.
	To AnchorBinding
	// Post-decision.
	Persisted         AnchorBindingPersistence
	Agreement         AnchorBindingAgreement
	DisagreementField AnchorBindingDisagreementField
	ServedAnchor      anchorRef
	ServedCount       string
	ServedCountAnchor anchorRef
}

// decide runs the binder for one Save and returns the binding and the line
// minus its persistence outcome.
func (t *anchorBindingTracker) decide(site BudgetAssertStage, result InvestigationResult, state *PersistedSemanticState) (AnchorBinding, AnchorBindingTransitionEvent) {
	// No tracker is no decision, here as well as at the capture: every
	// reader of a turn's tracker holds it as a pointer that is nil while the
	// shadow is off.
	if t == nil {
		return AnchorBinding{}, unrecordedAnchorBindingEvent(site, result)
	}
	in := anchorBindingInput{
		From: t.parent.from(), Evaluation: t.evaluation, Frame: t.frame, ModelAnchorKind: t.modelAnchorKind,
		Receipt: t.receipt, CallerHints: t.callerHints, Bases: t.bases,
		ResultID: result.ResultID, GraphEpoch: t.epoch,
	}
	switch t.evaluation {
	case AnchorBindingEvaluationResolved:
		in.Resolution = servedResolutionProof(t.resolution, result.SubjectResolution)
	case AnchorBindingEvaluationWindowGated:
		in.Resolution = t.resolution
	}
	to, proposal := bindAnchor(in)
	event := t.lineFor(site, result, in, proposal, to)
	if state == nil {
		event.Agreement, event.DisagreementField = AnchorBindingNotEvaluated, AnchorBindingFieldNone
		return to, event
	}
	event.ServedAnchor = ledgerAnchor(confirmedNeedsOf(state))
	countEvaluated := false
	if t.servedCount != nil && t.servedCount.ExpressionKind == SubjectExpressionChildrenOfScope {
		countEvaluated = true
		event.ServedCount = string(t.servedCount.Decision)
		if t.servedCount.Decision == CountPopulationScopeAnchorCommitted {
			event.ServedCountAnchor = anchorRef{Kind: t.servedCount.AnchorSubjectKind, ID: t.servedCount.AnchorID}
		}
	}
	effective := to.effective()
	switch {
	case to.State == AnchorBindingPendingWindowConfirmation:
		event.Agreement, event.DisagreementField = AnchorBindingDisagree, AnchorBindingFieldPendingProof
	case effective != event.ServedAnchor:
		event.Agreement, event.DisagreementField = AnchorBindingDisagree, AnchorBindingFieldCarriedAnchor
	case countEvaluated && effective != event.ServedCountAnchor:
		event.Agreement, event.DisagreementField = AnchorBindingDisagree, AnchorBindingFieldCountAnchor
	default:
		event.Agreement, event.DisagreementField = AnchorBindingAgree, AnchorBindingFieldNone
	}
	return to, event
}

// lineFor is the transition line's pre-entry, proposal and decision: every
// input the binder read, as it read it.
func (t *anchorBindingTracker) lineFor(site BudgetAssertStage, result InvestigationResult, in anchorBindingInput, proposal anchorBindingProposal, to AnchorBinding) AnchorBindingTransitionEvent {
	event := AnchorBindingTransitionEvent{
		ResultID: result.ResultID, ParentResultID: t.parent.ResultID, Site: site,
		Evaluation: in.Evaluation, ParentBinding: t.parent.Status, CarryChecks: t.parent.carryChecks(), From: in.From,
		ParentGraphEpoch: t.parent.storedEpoch(), GraphEpoch: in.GraphEpoch,
		CommittedSubjects: committedSubjectIDs(in.Resolution, in.Bases),
		ModelAnchorKind:   in.ModelAnchorKind, NamedExpectedKind: namedKindOf(in.Frame),
		CallerHintIDs: hintIDs(in.CallerHints), ProvenAnchorIDs: refIDs(proposal.Proven),
		EffectiveKind: proposal.EffectiveKind, To: to, ServedCount: "not_evaluated",
	}
	if in.Frame != nil {
		event.FrameExpressionKind = in.Frame.SubjectExpression.Kind
		if in.Frame.SubjectExpression.Scoped != nil {
			event.AnchorTermCount = len(in.Frame.SubjectExpression.Scoped.AnchorTerms)
		}
	}
	if in.Receipt != nil {
		event.ReceiptAnchor = anchorRef{Kind: in.Receipt.AppliedKind, ID: in.Receipt.AppliedValue}
	}
	return event
}

func namedKindOf(frame *QuestionFrame) SubjectKind {
	if frame != nil && frame.SubjectExpression.Named != nil && frame.SubjectExpression.Named.ExpectedKind != nil {
		return *frame.SubjectExpression.Named.ExpectedKind
	}
	return ""
}

// committedSubjectIDs is every committed subject the binder weighed, as
// "<kind>:<canonical id>=<commit basis>", in commit order.
func committedSubjectIDs(resolution SubjectResolution, bases CommitBasisSet) []string {
	out := make([]string, 0, len(resolution.Committed))
	for _, subject := range resolution.Committed {
		out = append(out, string(subject.Kind)+":"+subject.CanonicalID+"="+string(bases.For(subject)))
	}
	return out
}

// servedResolutionProof restricts the decisive resolution's commits to the
// subjects the saved document still commits: a subject an exit dropped (an
// authorization failure, a collapsed group axis) is no proof for the result
// being saved. Candidates stay the decisive resolution's, because they carry
// the matched terms anchorBound reads.
func servedResolutionProof(resolved, served SubjectResolution) SubjectResolution {
	kept := map[anchorRef]bool{}
	for _, subject := range served.Committed {
		kept[anchorRef{Kind: subject.Kind, ID: subject.CanonicalID}] = true
	}
	out := SubjectResolution{Candidates: resolved.Candidates}
	for _, subject := range resolved.Committed {
		if kept[anchorRef{Kind: subject.Kind, ID: subject.CanonicalID}] {
			out.Committed = append(out.Committed, subject)
		}
	}
	return out
}

// confirmedNeedsOf is the snapshot's outgoing ledger, empty when there is no
// snapshot to read one from.
func confirmedNeedsOf(state *PersistedSemanticState) []ConfirmedNeedEntry {
	if state == nil {
		return nil
	}
	return state.ConfirmedNeeds
}

// ledgerAnchor is the outgoing ledger's subject_anchor entry, or none.
func ledgerAnchor(entries []ConfirmedNeedEntry) anchorRef {
	for _, entry := range entries {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			return anchorRef{Kind: entry.AppliedKind, ID: entry.AppliedValue}
		}
	}
	return anchorRef{}
}

func hintIDs(hints []SubjectHint) []string {
	out := make([]string, 0, len(hints))
	for _, hint := range hints {
		out = append(out, string(hint.Kind)+":"+hint.ID)
	}
	sort.Strings(out)
	return out
}

func refIDs(refs []anchorRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, string(ref.Kind)+":"+ref.ID)
	}
	return out
}

// withAnchorShadow attaches the tracker to a capture.
func (c semanticStateCapture) withAnchorShadow(tracker *anchorBindingTracker) semanticStateCapture {
	c.anchorShadow = tracker
	return c
}

// attachAnchorBinding decides the binding for this Save and returns the
// capture to persist plus the pending line. A capture with no snapshot, or
// one the binding would make unencodable, is returned unchanged.
func (c semanticStateCapture) attachAnchorBinding(site BudgetAssertStage, result InvestigationResult) (semanticStateCapture, *AnchorBindingTransitionEvent) {
	if c.anchorShadow == nil {
		return c, nil
	}
	binding, event := c.anchorShadow.decide(site, result, c.Write.State)
	if c.Write.State == nil {
		event.Persisted = AnchorBindingStateAbsent
		return c, &event
	}
	state, err := withAnchorBindingMember(c.Write.State, binding)
	var encoded []byte
	if err == nil {
		encoded, err = EncodeSemanticState(state)
	}
	if err != nil {
		event.Persisted = AnchorBindingUnencodable
		return c, &event
	}
	out := c
	out.Write = SemanticStateOf(state)
	out.EncodedBytes = len(encoded)
	return out, &event
}

// anchorBindingExtension is the snapshot extension member the binding is
// stored under.
const anchorBindingExtension = "anchor_binding"

// semanticStateShadowMembers are the members replay equality never compares.
var semanticStateShadowMembers = map[string]bool{anchorBindingExtension: true}

// withAnchorBindingMember returns a copy of state carrying binding as its
// extension member; state itself is not modified.
func withAnchorBindingMember(state *PersistedSemanticState, binding AnchorBinding) (*PersistedSemanticState, error) {
	// encoding/json rewrites invalid UTF-8 silently, so the member would hold
	// a different identity than the one decided: refused before encoding.
	if path, ok := firstUnencodableString(anchorBindingExtension, binding); !ok {
		return nil, fmt.Errorf("%w: %s is not valid UTF-8", ErrSemanticStateRejected, path)
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSemanticStateRejected, err)
	}
	out := *state
	out.Extensions = make(SemanticStateExtensions, len(state.Extensions)+1)
	for name, value := range state.Extensions {
		out.Extensions[name] = value
	}
	out.Extensions[anchorBindingExtension] = raw
	return &out, nil
}

// storedAnchorBinding reads the binding member of a stored snapshot: present
// is false when the member is absent, and err is set when it does not decode
// as a binding with no unknown keys.
func storedAnchorBinding(state *PersistedSemanticState) (binding AnchorBinding, present bool, err error) {
	if state == nil {
		return AnchorBinding{}, false, nil
	}
	raw, ok := state.Extensions[anchorBindingExtension]
	if !ok {
		return AnchorBinding{}, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return AnchorBinding{}, true, err
	}
	// The member is one JSON value by construction: the snapshot decoder
	// already parsed it as one.
	return binding, true, nil
}

// unrecordedAnchorBindingEvent is the line a Save emits when no tracker was
// attached while the shadow is on.
func unrecordedAnchorBindingEvent(site BudgetAssertStage, result InvestigationResult) AnchorBindingTransitionEvent {
	none := AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonUnrecorded}
	return AnchorBindingTransitionEvent{
		ResultID: result.ResultID, Site: site, Evaluation: AnchorBindingEvaluationNotResolved,
		ParentBinding: AnchorBindingParentNoReference, CarryChecks: AnchorBindingCarryChecksNotApplicable, From: none, To: none,
		ParentGraphEpoch: -1, CommittedSubjects: []string{}, CallerHintIDs: []string{}, ProvenAnchorIDs: []string{},
		Agreement: AnchorBindingNotEvaluated, DisagreementField: AnchorBindingFieldNone, ServedCount: "not_evaluated",
	}
}

// reuseEvent decides and describes a reuse serve: the replayed row's reading
// and resolution stand in for this turn's, nothing is saved, and nothing
// served is compared.
func (t *anchorBindingTracker) reuseEvent(result InvestigationResult, reading storedCountReading) AnchorBindingTransitionEvent {
	in := anchorBindingInput{
		From: t.parent.from(), Evaluation: AnchorBindingEvaluationReused, Frame: reading.Frame, ModelAnchorKind: reading.AnchorKind,
		Receipt: t.receipt, CallerHints: t.callerHints,
		Resolution: result.SubjectResolution, Bases: CommitBasisSetFromDigests(result.SubjectResolution.CommitDecisionDigests),
		ResultID: result.ResultID, GraphEpoch: t.epoch,
	}
	to, proposal := bindAnchor(in)
	event := t.lineFor(BudgetAssertReuse, result, in, proposal, to)
	event.Persisted, event.Agreement, event.DisagreementField = AnchorBindingNotSaved, AnchorBindingNotEvaluated, AnchorBindingFieldNone
	return event
}

func (e *Engine) recordAnchorBindingTransition(ctx context.Context, principal storage.Principal, event AnchorBindingTransitionEvent) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordAnchorBindingTransition(ctx, principal, event)
}

// AnchorBindingTransitionLogArgs builds the Info line's fields. Every key is
// written on every line.
func AnchorBindingTransitionLogArgs(event AnchorBindingTransitionEvent, orgID string) []any {
	closed := anchorBindingClosedToken
	return []any{
		"org_id", SanitizeLogAttr(orgID),
		"result_id", SanitizeLogAttr(event.ResultID),
		"parent_result_id", SanitizeLogAttr(event.ParentResultID),
		"site", SanitizeLogAttr(closed("site", string(event.Site))),
		"evaluation", SanitizeLogAttr(closed("evaluation", string(event.Evaluation))),
		// PRE-ENTRY: the binding this turn started from.
		"parent_binding", SanitizeLogAttr(closed("parent_binding", string(event.ParentBinding))),
		"carry_checks", SanitizeLogAttr(closed("carry_checks", string(event.CarryChecks))),
		"from_state", SanitizeLogAttr(closed("from_state", string(event.From.State))),
		"from_kind", SanitizeLogAttr(string(event.From.Kind)),
		"from_id", SanitizeLogAttr(event.From.CanonicalID),
		"from_proof", SanitizeLogAttr(closed("from_proof", string(event.From.Proof))),
		"from_reason", SanitizeLogAttr(closed("from_reason", string(event.From.Reason))),
		"from_origin_result_id", SanitizeLogAttr(event.From.OriginResultID),
		"from_graph_epoch", event.From.GraphEpoch,
		"from_contender_kind", SanitizeLogAttr(string(event.From.ContenderKind)),
		"from_contender_id", SanitizeLogAttr(event.From.ContenderID),
		"parent_graph_epoch", event.ParentGraphEpoch,
		"graph_epoch", event.GraphEpoch,
		// PROPOSAL: what the binder read.
		"frame_expression_kind", SanitizeLogAttr(string(event.FrameExpressionKind)),
		"anchor_term_count", event.AnchorTermCount,
		"committed_subjects", SanitizeLogStrings(nonNilStrings(event.CommittedSubjects)),
		"model_anchor_kind", SanitizeLogAttr(string(event.ModelAnchorKind)),
		"named_expected_kind", SanitizeLogAttr(string(event.NamedExpectedKind)),
		"receipt_anchor_kind", SanitizeLogAttr(string(event.ReceiptAnchor.Kind)),
		"receipt_anchor_id", SanitizeLogAttr(event.ReceiptAnchor.ID),
		"caller_hint_ids", SanitizeLogStrings(nonNilStrings(event.CallerHintIDs)),
		"proven_anchor_ids", SanitizeLogStrings(nonNilStrings(event.ProvenAnchorIDs)),
		"effective_kind", SanitizeLogAttr(string(event.EffectiveKind)),
		// DECISION.
		"to_state", SanitizeLogAttr(closed("to_state", string(event.To.State))),
		"to_kind", SanitizeLogAttr(string(event.To.Kind)),
		"to_id", SanitizeLogAttr(event.To.CanonicalID),
		"proof", SanitizeLogAttr(closed("proof", string(event.To.Proof))),
		"reason", SanitizeLogAttr(closed("reason", string(event.To.Reason))),
		"origin_result_id", SanitizeLogAttr(event.To.OriginResultID),
		"to_graph_epoch", event.To.GraphEpoch,
		"contender_kind", SanitizeLogAttr(string(event.To.ContenderKind)),
		"contender_id", SanitizeLogAttr(event.To.ContenderID),
		// POST-DECISION: its fate and the served decision it is compared to.
		"persisted", SanitizeLogAttr(closed("persisted", string(event.Persisted))),
		"shadow_agreement", SanitizeLogAttr(closed("shadow_agreement", string(event.Agreement))),
		"disagreement_field", SanitizeLogAttr(closed("disagreement_field", string(event.DisagreementField))),
		"served_anchor_kind", SanitizeLogAttr(string(event.ServedAnchor.Kind)),
		"served_anchor_id", SanitizeLogAttr(event.ServedAnchor.ID),
		"served_count_decision", SanitizeLogAttr(closed("served_count_decision", event.ServedCount)),
		"served_count_kind", SanitizeLogAttr(string(event.ServedCountAnchor.Kind)),
		"served_count_id", SanitizeLogAttr(event.ServedCountAnchor.ID),
	}
}

// anchorBindingClosedToken is value when it is a member of key's closed
// vocabulary, the out-of-vocabulary token otherwise.
func anchorBindingClosedToken(key, value string) string {
	if memberOf(AnchorBindingTransitionLineVocabulary(key), value) {
		return value
	}
	return anchorBindingUndeclared
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
