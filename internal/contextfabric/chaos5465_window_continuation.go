package contextfabric

// CHAOS-5465: a verified window-only turn-two confirmation is a CONTINUATION
// of turn one's validated reading, not a fresh interpretation that happens to
// carry a window hint.
//
// WHAT WAS WRONG, measured rather than argued (D-0 probe 1, run on the parent
// at 3822c1d7 through Engine.Investigate with a real store, a real receipt and
// a forced interpreter disagreement):
//
//	plan-carry OUTCOME    outcome="hit" source_result_id=<turn one> seed_source="receipt"
//	APPLIED-carry emits   0
//	SERVED                family="grouped_cohort_status" family_source="model"
//
// The carry LOOKED UP the validated turn-one reading, found it, and then
// discarded it -- because applyCarriedPlan applies only when this turn
// classified nothing of its own, and turn two classified. The single line an
// operator sees says `hit`. The negative control (same carrier, same receipt,
// a turn that classifies nothing) served family_source="carried", so the zero
// above is discriminating and not a broken fixture.
//
// Measured over the archive as well (D-0 probe 2, 38 t1/t2 pairs of one corpus
// row): 38/38 turn-two requests carry EXACTLY ONE window receipt and no other
// prior-result reference, 37/38 name turn one and repeat its question bytes
// exactly, 37/38 turn-one plans are carriable -- and `family_source=carried`
// appears 0 times.
//
// WHAT THIS FILE DECIDES. Recognition of the transition (D-a), which context
// wins and how disagreement is disclosed (D-b), and the one Info event that
// makes the decision observable (D-c). Containment against everything that is
// NOT this transition (D-d) is enforced here too, by refusing to recognise it.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO, and why, so the next reader does
// not mistake the gap for an oversight:
//
//   - THE CARRIED CONTEXT IS THE PERSISTED SEMANTIC SNAPSHOT. Every result
//     now saves its accepted reading -- family, normalized frame, gate
//     verdict, role slots and requirement declarations -- beside its payload
//     (semantic_state.go). Admission requires it: a carrier without one (a row
//     written before the column, or one whose snapshot is malformed, oversized
//     or of an unsupported format) is WITHHELD with its own reason and the
//     turn is refused, never continued on a reading reconstructed from the
//     plan's family label.
//
//   - THE REFUSAL IS NOT DECIDED HERE. A carrier that cannot be established
//     is reported as `withheld` with its own reason, and the turn it ends is
//     refused by chaos5465_continuation_refusal.go through its own terminal,
//     with the wire basis `continuation_context_unverifiable` and a fixed
//     sentence. ContextFabricRefusalBasisLimitation is NOT used for it: that
//     sentence names a declared member kind, which this condition has none of.
//
// The comparison this file performs is COMPLETE with respect to the context it
// accepts: every semantic component the snapshot carries is compared against
// the fresh proposal's own, and `agreement` never claims agreement about a
// component nothing carried.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ContinuationDisposition is what the engine DID about the continuation, as a
// closed, content-safe vocabulary.
//
// THREE MEMBERS, NOT A BOOL, for the reason carryOriginVerdict states one file
// over: "we withheld a context we could see" and "there was no continuation to
// make" are different facts and must not share a label. A rate computed over a
// two-valued disposition would bucket every ordinary non-continuation request
// with every genuine withholding.
type ContinuationDisposition string

const (
	// ContinuationNotApplicable: this request is not a window-only
	// continuation, or it is one whose referenced turn decided nothing to
	// continue. Nothing was withheld because nothing was available.
	ContinuationNotApplicable ContinuationDisposition = "not_applicable"
	// ContinuationApplied: the carried context was admitted and is what the
	// rest of the turn executes under.
	ContinuationApplied ContinuationDisposition = "applied"
	// ContinuationWithheld: this request IS a window-only continuation and a
	// carrier was named, but admission could not be established. Fail-closed
	// against semantic substitution: the carrier is not used, and neither is
	// the old family-only carry (see D-d).
	ContinuationWithheld ContinuationDisposition = "withheld"
)

// ContinuationDecisionReason is the closed admission/evaluation reason. It is
// ALWAYS present -- there is no empty member, because an empty reason beside a
// `not_applicable` disposition is indistinguishable from a code path that
// never decided.
//
// `unspecified` NEVER means admission succeeded. It is the fail-closed member,
// the same discipline ContextFabricRefusalBasisUnspecified holds: reaching it
// means a decision site was added without a reason line here.
type ContinuationDecisionReason string

const (
	// ContinuationReasonNone: no reason to withhold. Valid ONLY alongside
	// ContinuationApplied.
	ContinuationReasonNone ContinuationDecisionReason = "none"
	// ContinuationReasonNotWindowOnly: the request carries a window receipt
	// but is not the window-only shape -- plural window receipts, another
	// prior-result reference, or an added typed selection. D-d: a window
	// receipt riding along with a subject/kind/anchor/handle/candidate
	// selection is that selection's turn, not a continuation.
	ContinuationReasonNotWindowOnly ContinuationDecisionReason = "not_window_only"
	// ContinuationReasonWindowVeto: window validation vetoed this request, so
	// there was never a verified confirmation to continue from. Existing
	// window vetoes keep their existing handling; this reason only records
	// that the continuation decision did not run (D-e).
	ContinuationReasonWindowVeto ContinuationDecisionReason = "window_veto"
	// ContinuationReasonChangedQuestion: the referenced turn answered a
	// DIFFERENT question. The window receipt stays subject to the existing
	// window rules; it cannot import the prior non-window context (D-d).
	ContinuationReasonChangedQuestion ContinuationDecisionReason = "changed_question"
	// ContinuationReasonIndeterminateIdentity: one of the two questions
	// canonicalizes to the empty string, so there is no identity to compare.
	// Not drift -- nothing was shown to differ (carryOriginVerdict's own
	// three-state rule, reused rather than re-derived).
	ContinuationReasonIndeterminateIdentity ContinuationDecisionReason = "indeterminate_identity"
	// ContinuationReasonMissingContext: the carrier read back perfectly and
	// carried no reading to continue -- an unclassified or absent plan.
	// carriablePlan refuses unclassified deliberately ("the next turn is
	// entitled to its own attempt"), and that shipped rule is preserved: this
	// is not_applicable, not a withholding.
	ContinuationReasonMissingContext ContinuationDecisionReason = "missing_context"
	// ContinuationReasonInvalidContext: the carrier could not be read at all,
	// failed the CHAOS-3898 ingress taint gate, or records a time axis a window
	// confirmation cannot apply to (CHAOS-5582: a window is canonicalized ONLY
	// on the current axis, so a carrier whose recorded axis is not current has
	// no reading this window-only turn can continue).
	ContinuationReasonInvalidContext ContinuationDecisionReason = "invalid_context"
	// ContinuationReasonContextVersionMismatch: the carrier's recorded family
	// definition-table version is not the one in force. D-a: revalidate under
	// the RECORDED standard; an unsupported version is an admission failure,
	// never permission to reinterpret the context under today's tables.
	ContinuationReasonContextVersionMismatch ContinuationDecisionReason = "context_version_mismatch"
	// ContinuationReasonFreshContextUnavailable: the diagnostic fresh proposal
	// was not produced. It does NOT invalidate an independently admitted
	// carrier -- see admitWindowContinuation.
	ContinuationReasonFreshContextUnavailable ContinuationDecisionReason = "fresh_context_unavailable"
	// ContinuationReasonBindingUnavailable: the graph binding failed, so the
	// turn ended before admission could run. Its OWN member: a path that was
	// never reached is not a decision site that forgot to record one.
	ContinuationReasonBindingUnavailable ContinuationDecisionReason = "binding_unavailable"
	// ContinuationReasonStructureVeto: structure canonicalisation vetoed.
	ContinuationReasonStructureVeto ContinuationDecisionReason = "structure_veto"
	// NOTE (r3 F4, second instance): there is deliberately NO
	// `window_confirmation_required` member either. That gate fires only on
	// windowCanon.ExplicitUnconfirmed, which window.go documents as true ONLY
	// when the effective window came from the MCP bare-explicit field with
	// Provenance==WindowInferredDefault -- "never question_stated or
	// clarification_confirmed". A redeemed window receipt resolves to
	// clarification_confirmed, so a request in this event's own population can
	// never reach that gate. Found by giving the member a driver and watching
	// the driver fail to reach it, which is the whole reason the enumeration
	// now drives production instead of comparing two lists.
	// NOTE (r3 F4): there is deliberately NO `answer_reused` member. A request
	// carrying a window receipt BYPASSES answer reuse by construction --
	// reuseBypassReason keys the bypass on the same receipt population
	// carryReferencedResultIDs collects (CHAOS-4998), so it returns
	// `prior_result_reference` for exactly the requests this event describes.
	// A member no request in this event's own population can reach is a member
	// that reads as coverage and measures nothing; it was removed rather than
	// given a driver that could not exist.
	// ContinuationReasonExplicitStructureHint: this request states an explicit
	// expected kind or subject handle. That is a semantic change the caller
	// made THIS turn, so the turn is not "changed only the window" -- it is
	// that hint's turn. A DISQUALIFIER, evaluated inside admission before any
	// effect.
	ContinuationReasonExplicitStructureHint ContinuationDecisionReason = "explicit_structure_hint"
	// NOTE (CHAOS-5582): there is deliberately NO `interpreted_axis_veto`
	// member. It disqualified admission whenever THIS turn's fresh
	// interpretation moved the axis off current, so one valid receipt, the
	// identical question and no explicit window were refused on a sampled
	// axis alone -- the falsification shape the design of record names for
	// keeping the receipt conflicts (plural receipts, an explicit window
	// beyond skew) apart from continuation drift. Those conflicts veto before
	// Interpret and report `window_veto`; the fresh axis of an ADMITTED
	// continuation is now a diagnostic (ContinuationAxisOutcome), and a turn
	// the axis-conflict veto does end is one that was never admitted, so it
	// already carries the reason it was not.
	// ContinuationReasonRequestInvalid: the request failed validation, so the
	// turn ended before anything about a continuation could be decided.
	ContinuationReasonRequestInvalid ContinuationDecisionReason = "request_invalid"
	// ContinuationReasonPrincipalUnauthenticated: no authenticated org.
	ContinuationReasonPrincipalUnauthenticated ContinuationDecisionReason = "principal_unauthenticated"
	// ContinuationReasonRequestTimeUnresolvable: the CALLER's own time bounds
	// were unanswerable (the wire-side clamp).
	ContinuationReasonRequestTimeUnresolvable ContinuationDecisionReason = "request_time_unresolvable"
	// ContinuationReasonRequestCancelled: the caller's context was already
	// cancelled. Its own member (r3 F2): a cancelled turn is not a decision
	// site that forgot to record a reason.
	ContinuationReasonRequestCancelled ContinuationDecisionReason = "request_cancelled"
	// ContinuationReasonAsOfUnresolvable: the INTERPRETED time bounds were
	// unanswerable (r3 F1). Distinct from the wire-side member above because
	// the two name different actors -- the caller and the interpreter -- and
	// collapsing them would hide which one produced the unanswerable bound.
	ContinuationReasonAsOfUnresolvable ContinuationDecisionReason = "as_of_unresolvable"
	// ContinuationReasonCompositionInvalid: the carried reading was admitted,
	// and composing it into a frame produced one that fails a frame invariant.
	// Its OWN member: "we would not continue this turn" and "the continuation
	// could not be expressed as a valid frame" are different facts, and the
	// second is the one a design review reproduced being served under a gate
	// that had certified something else.
	ContinuationReasonCompositionInvalid ContinuationDecisionReason = "composition_invalid"
	// ContinuationReasonWindowSuperseded (r4 R4-3): the continuation was
	// applied and then the SAVE-time window supersession veto discarded the
	// result it was applied to. The decision is not final until save; an event
	// left reporting `applied` here describes a turn that did not happen.
	ContinuationReasonWindowSuperseded ContinuationDecisionReason = "window_superseded"
	// ContinuationReasonAnswerBudgetChanged: this request's effective response
	// byte budget differs from the one the carrier's plan recorded. The budget
	// shapes what the answer may contain, so a turn that changes it has changed
	// more than the evidence window: it is not a window-only continuation, and
	// it takes the fresh path rather than the carrier's plan.
	ContinuationReasonAnswerBudgetChanged ContinuationDecisionReason = "answer_budget_changed"
	// ContinuationReasonSemanticStateAbsent: the carrier read back and carries
	// NO persisted semantic snapshot -- a row saved before the column existed,
	// or one whose turn recorded a closed absence. The reading is
	// unavailable; it is never reconstructed from the plan's family label.
	ContinuationReasonSemanticStateAbsent ContinuationDecisionReason = "semantic_state_absent"
	// ContinuationReasonSemanticStateInvalid: the carrier's snapshot is
	// present and unusable -- malformed, oversized, unreported by the store,
	// or inconsistent with the carrier's own public plan.
	ContinuationReasonSemanticStateInvalid ContinuationDecisionReason = "semantic_state_invalid"
	// ContinuationReasonUnspecified: a decision site reached a return without
	// recording a reason. Loud by construction, and NEVER expected to reach the
	// emitter -- TestWindowContinuation_EveryReasonIsAssignedBySomePath
	// enumerates the vocabulary against the paths that produce it.
	ContinuationReasonUnspecified ContinuationDecisionReason = "unspecified"
)

// ContinuationAxisOutcome is what the engine did with THIS turn's fresh
// interpreted time axis on a request carrying a window receipt (CHAOS-5582).
//
// A CLOSED VOCABULARY, NOT A BOOL, because four different facts end in the same
// two served axes: the fresh axis already agreed, the receipt overrode a fresh
// drift, a fresh drift was followed and the axis-conflict veto ended the turn,
// and the turn ended before any axis was reconciled at all.
type ContinuationAxisOutcome string

const (
	// ContinuationAxisNotEvaluated: no fresh axis was reconciled against a
	// window commitment -- the turn ended before interpretation, the
	// interpreted bound was unanswerable, or no window commitment reached the
	// decision.
	ContinuationAxisNotEvaluated ContinuationAxisOutcome = "not_evaluated"
	// ContinuationAxisAgreed: the fresh axis is current, the axis the window
	// was confirmed under. Nothing was overridden. Without an established
	// transition a current axis with an unanswerable bound also reads `agreed`:
	// the axis agrees, and the bound exit refuses on its own reason.
	ContinuationAxisAgreed ContinuationAxisOutcome = "agreed"
	// ContinuationAxisOverriddenByReceipt: the window-only TRANSITION was
	// established (one window receipt, a readable taint-valid carrier, the
	// identical question) on a carrier that recorded the current axis, and the
	// fresh time moved off current or carried an unanswerable bound; the turn executes under the current axis
	// with the confirmed window, and the fresh axis is a diagnostic on this
	// line. A sampled axis is not a user change -- the user changed only the
	// window. Whether the carried READING was then applied, withheld or absent
	// is decision_reason's to say; the axis does not depend on it.
	ContinuationAxisOverriddenByReceipt ContinuationAxisOutcome = "overridden_by_receipt"
	// ContinuationAxisVetoed: the window-only transition was NOT established on
	// a current-axis carrier (a changed or indeterminate question, a
	// disqualified, unreadable or other-axis carrier), the fresh axis moved off
	// current and a window commitment was resolved, so the fresh interpretation
	// governs and the axis-conflict veto ends the turn.
	ContinuationAxisVetoed ContinuationAxisOutcome = "vetoed"
)

func continuationAxisOutcomes() []ContinuationAxisOutcome {
	return []ContinuationAxisOutcome{
		ContinuationAxisNotEvaluated,
		ContinuationAxisAgreed,
		ContinuationAxisOverriddenByReceipt,
		ContinuationAxisVetoed,
	}
}

// ValidContinuationAxisOutcome reports membership.
func ValidContinuationAxisOutcome(value ContinuationAxisOutcome) bool {
	for _, member := range continuationAxisOutcomes() {
		if member == value {
			return true
		}
	}
	return false
}

// ContinuationConflictReason is the Info-only disagreement token.
//
// A RECONCILED DISAGREEMENT IS NOT A REFUSAL. The user already supplied the
// authorized change by redeeming the window offer; asking them to arbitrate a
// model proposal the server did not use would turn sampler variance into a new
// product requirement.
type ContinuationConflictReason string

const (
	// ContinuationConflictNone: explicit, never the zero value standing in for
	// "not measured". An unevaluated comparison also reports `none` and is
	// told apart by ComparisonEvaluated.
	ContinuationConflictNone ContinuationConflictReason = "none"
	// ContinuationConflictNonWindowContext: the fresh proposal disagreed with
	// the admitted carried context on something other than the window.
	ContinuationConflictNonWindowContext ContinuationConflictReason = "non_window_context_conflict"
)

// ContinuationConflictField names a SEMANTIC COMPONENT that differed. These
// are not wire fields and they are not new schema.
//
// The vocabulary is declared WHOLE, matching the design of record, so that the
// members a later slice can populate are already named rather than invented
// twice. This build populates only the members its accepted context actually
// carries -- see continuationComparableFields, and the pin that asserts which
// members are populatable today.
type ContinuationConflictField string

const (
	ContinuationConflictFieldFamily             ContinuationConflictField = "family"
	ContinuationConflictFieldSubjectExpression  ContinuationConflictField = "subject_expression"
	ContinuationConflictFieldNarrowingBasis     ContinuationConflictField = "narrowing_basis"
	ContinuationConflictFieldRoles              ContinuationConflictField = "roles"
	ContinuationConflictFieldGoals              ContinuationConflictField = "goals"
	ContinuationConflictFieldTemporal           ContinuationConflictField = "temporal"
	ContinuationConflictFieldEmphasis           ContinuationConflictField = "emphasis"
	ContinuationConflictFieldDimensions         ContinuationConflictField = "dimensions"
	ContinuationConflictFieldObligations        ContinuationConflictField = "obligations"
	ContinuationConflictFieldWidenedObligations ContinuationConflictField = "widened_obligations"
	ContinuationConflictFieldRequirements       ContinuationConflictField = "requirements"
	ContinuationConflictFieldInterpretation     ContinuationConflictField = "interpretation"
	ContinuationConflictFieldFrameGate          ContinuationConflictField = "frame_gate"
)

// continuationComparableFields is the subset this build compares, in a fixed
// order so conflict_fields is deterministic: every component the persisted
// snapshot carries.
//
// INTERPRETATION IS NOT COMPARED because the snapshot does not carry the
// interpretation (shape, judgment, fact requirements) -- only the frame
// validation's emitted shape -- so there is nothing carried for a fresh
// interpretation to disagree with.
//
// NARROWING BASIS IS CARRIED BUT NOT COMPARED, and the distinction is the
// point. The carried basis reaches the plan through the existing plan-carry
// path (Investigate stamps plan.Budget.NarrowingBasis from a carry hit), but
// the FRESH side has no basis of its own to disagree with: it is derived
// inside PlanAnswer, after this comparison, from a default. Listing it as
// comparable would manufacture a comparison against a value the interpreter
// never proposed -- which is precisely the "compare a copy that merely happens
// to be equal" shape this package has been bitten by before.
func continuationComparableFields() []ContinuationConflictField {
	return []ContinuationConflictField{
		ContinuationConflictFieldFamily,
		ContinuationConflictFieldSubjectExpression,
		ContinuationConflictFieldRoles,
		ContinuationConflictFieldGoals,
		ContinuationConflictFieldTemporal,
		ContinuationConflictFieldEmphasis,
		ContinuationConflictFieldDimensions,
		ContinuationConflictFieldObligations,
		ContinuationConflictFieldWidenedObligations,
		ContinuationConflictFieldRequirements,
		ContinuationConflictFieldFrameGate,
	}
}

// continuationCarriedContext is the validated prior context this build
// preserves: the answer plan's own reading of the question.
type continuationCarriedContext struct {
	Family         QuestionFamily
	GroupKind      SubjectKind
	NarrowingBasis contractsv1.ContextFabricNarrowingBasis
	FamilyVersion  string
	SourceResultID string
	// State is the carrier's persisted semantic snapshot -- the WHOLE
	// reading composition establishes and planning consumes. Never nil on an
	// admitted carrier.
	State *PersistedSemanticState
}

// continuationFreshProposal is the interpreter's return, held as a
// NON-AUTHORITATIVE comparison for an admitted continuation.
type continuationFreshProposal struct {
	Available bool
	Family    QuestionFamily
	GroupKind SubjectKind
	// State is the fresh proposal rendered in the snapshot's own shape, so
	// the two readings are compared component by component with one
	// representation. Diagnostic only: it is never validated as a carrier
	// and never persisted.
	State *PersistedSemanticState
}

// windowContinuationDecision is the whole decision, and the single value that
// both drives execution and populates the event.
//
// ONE VALUE, TWO USES, deliberately: a decision logged from one variable and
// executed from another is the "log the intended selection, pass the fresh one
// downstream" defect the mutant matrix names, and it is invisible from the
// event alone.
type windowContinuationDecision struct {
	// Observed is false when the request carries no window receipt at all. No
	// event is emitted for such a request -- the event's denominator is
	// "requests carrying window receipts", and widening it to every request
	// would make the rate meaningless.
	Observed bool
	// WindowOnlyShape is windowOnlyReferencedResultID's verdict: this request
	// IS the window-only continuation shape, whatever admission then decided.
	//
	// SEPARATE FROM Disposition BECAUSE THE CONTAINMENT NEEDS THE SHAPE, NOT
	// THE OUTCOME (r1 finding R1-2, reproduced). "The caller changed only the
	// window and we refused the carrier" and "the caller did something else"
	// are different facts: the first must stop the old family-only carry from
	// serving the very carrier this gate just refused, the second must NOT,
	// because there the family-only carry is the correct mechanism.
	WindowOnlyShape bool

	Disposition ContinuationDisposition
	Reason      ContinuationDecisionReason

	Carried  *continuationCarriedContext
	Fresh    continuationFreshProposal
	Accepted *continuationCarriedContext

	SeedSource CarrySeedSource

	ComparisonEvaluated bool
	Agreement           bool
	ConflictReason      ContinuationConflictReason
	ConflictFields      []ContinuationConflictField

	AppliedWindow *contractsv1.ContextFabricEffectiveEvidenceWindow

	// CompositionOutcome and CompositionFailedInvariant carry the boundary's
	// own verdict onto the event, so a withheld continuation says WHICH
	// invariant refused the composition rather than only that one did.
	CompositionOutcome         CompositionOutcome
	CompositionFailedInvariant string

	// CarriedStateConsulted is true once admission has consulted the
	// carrier's snapshot, and CarriedStateRead is the snapshot read status the
	// store reported for it -- kept apart so "never consulted" and "consulted,
	// status unreported" cannot share the empty value.
	CarriedStateConsulted bool
	CarriedStateRead      SemanticStateReadStatus

	// RefusalBasis is the wire refusal basis this decision SERVED, empty when
	// it served none. Set only where the continuation refusal is taken, and
	// cleared again at the exit when that refusal produced no document -- so
	// the line never claims a refusal the caller did not receive.
	RefusalBasis contractsv1.ContextFabricRefusalBasis

	// ReferencedResultID is the prior result the window-only request names,
	// set as soon as the shape is recognised and BEFORE admission, so every
	// withheld decision -- including one refused before a carrier was
	// admitted, when Carried is still nil -- names the carrier it is about.
	ReferencedResultID string
	// CarrierRead is what admission's read of that carrier returned: an
	// operator must be able to tell "could not read the carrier" from "read it
	// and proved it invalid" (a stale epoch), which share decision_reason.
	CarrierRead ContinuationCarrierRead

	// THE AXIS DECISION'S PRE-DECISION STATE, DECISION AND POST-DECISION STATE
	// (CHAOS-5582), all on the one line, so a refused or overridden turn is
	// readable from the trace alone.
	//
	// WindowReceiptCount and ExplicitWindowPresent are the request inputs the
	// receipt conflicts veto on (plural receipts; an explicit window beyond
	// skew), counted exactly as resolveWindowReceipts counts them.
	WindowReceiptCount    int
	ExplicitWindowPresent bool
	// TransitionEstablished is D-a's transition, proven: the window-only
	// shape, a carrier that loaded and passed the ingress taint gate, and the
	// identical question with a nonempty canonical identity. It is set BEFORE
	// the carried reading is examined (plan, family-table version, recorded
	// axis, composition), because the time the user confirmed the window under
	// is a property of the transition, not of whether the family reading could
	// be continued. Read from the line as: carried_axis non-empty and
	// decision_reason not changed_question/indeterminate_identity.
	TransitionEstablished bool
	// InterpretedAxis is THIS turn's fresh interpreted axis, empty when
	// interpretation never produced one.
	InterpretedAxis contractsv1.ContextFabricTemporalAxis
	// CarriedAxis is the axis the referenced carrier recorded, empty when no
	// carrier was loaded.
	CarriedAxis contractsv1.ContextFabricTemporalAxis
	// ExecutedAxis is the axis the rest of the turn executed under, empty when
	// the turn ended before the axis was decided.
	ExecutedAxis contractsv1.ContextFabricTemporalAxis
	AxisOutcome  ContinuationAxisOutcome
}

// ContinuationCarrierRead is the CLOSED outcome of admission's carrier read.
type ContinuationCarrierRead string

const (
	// ContinuationCarrierNotRead: admission ended before reading (the shape
	// was disqualified, or there is no store). The zero value reads as this.
	ContinuationCarrierNotRead ContinuationCarrierRead = "not_read"
	// ContinuationCarrierReadOK: the carrier was read (from the per-request
	// memo when window-receipt redemption already read it).
	ContinuationCarrierReadOK ContinuationCarrierRead = "read"
	// ContinuationCarrierReadFailed: the store returned an error.
	ContinuationCarrierReadFailed ContinuationCarrierRead = "failed"
)

// ValidContinuationCarrierRead reports membership.
func ValidContinuationCarrierRead(value ContinuationCarrierRead) bool {
	switch value {
	case ContinuationCarrierNotRead, ContinuationCarrierReadOK, ContinuationCarrierReadFailed:
		return true
	}
	return false
}

// ObservableCarrierRead is the token the decision line carries.
func (d windowContinuationDecision) ObservableCarrierRead() ContinuationCarrierRead {
	if d.CarrierRead == "" {
		return ContinuationCarrierNotRead
	}
	return d.CarrierRead
}

// Applies reports whether the carried context is authoritative for this turn.
// continuationDecisionReasons is the closed reason vocabulary, IN PRODUCTION.
//
// IT LIVES HERE, BESIDE THE MEMBERS, AND NOT IN A TEST. The r3 rewrite moved
// the enumeration pin onto real engine drivers but left the member list itself
// hand-written in the test file, and the r1 review of the re-cut found the
// consequence immediately: two reasons were added to production, neither was
// added to the list, and the pin that exists to catch exactly that could not
// see them. A vocabulary a test maintains is a vocabulary that agrees with
// whoever edited the test last.
//
// Every consumer -- the emitter's membership check and the enumeration pin
// alike -- reads THIS list, so a member added below is a member both of them
// must account for.
// continuationDispositions, continuationConflictReasons and
// continuationConflictFields are the remaining closed vocabularies on this
// event, in production for the same reason the reason list is.
func continuationDispositions() []ContinuationDisposition {
	return []ContinuationDisposition{ContinuationNotApplicable, ContinuationApplied, ContinuationWithheld}
}

// ValidContinuationDisposition reports membership.
func ValidContinuationDisposition(value ContinuationDisposition) bool {
	for _, member := range continuationDispositions() {
		if member == value {
			return true
		}
	}
	return false
}

func continuationConflictReasons() []ContinuationConflictReason {
	return []ContinuationConflictReason{ContinuationConflictNone, ContinuationConflictNonWindowContext}
}

// ValidContinuationConflictReason reports membership.
func ValidContinuationConflictReason(value ContinuationConflictReason) bool {
	for _, member := range continuationConflictReasons() {
		if member == value {
			return true
		}
	}
	return false
}

func continuationConflictFieldVocabulary() []ContinuationConflictField {
	return []ContinuationConflictField{
		ContinuationConflictFieldFamily,
		ContinuationConflictFieldSubjectExpression,
		ContinuationConflictFieldNarrowingBasis,
		ContinuationConflictFieldRoles,
		ContinuationConflictFieldGoals,
		ContinuationConflictFieldTemporal,
		ContinuationConflictFieldEmphasis,
		ContinuationConflictFieldDimensions,
		ContinuationConflictFieldObligations,
		ContinuationConflictFieldWidenedObligations,
		ContinuationConflictFieldRequirements,
		ContinuationConflictFieldInterpretation,
		ContinuationConflictFieldFrameGate,
	}
}

// ValidContinuationConflictField reports membership.
func ValidContinuationConflictField(value ContinuationConflictField) bool {
	for _, member := range continuationConflictFieldVocabulary() {
		if member == value {
			return true
		}
	}
	return false
}

// carrySeedSources is the closed carry seed vocabulary, in one place for the
// membership check and the line vocabulary alike.
func carrySeedSources() []CarrySeedSource {
	return []CarrySeedSource{CarrySeedNone, CarrySeedReceipt, CarrySeedParentField, CarrySeedBoth}
}

// ValidCarrySeedSource reports membership of the carry seed vocabulary.
func ValidCarrySeedSource(value CarrySeedSource) bool {
	for _, member := range carrySeedSources() {
		if member == value {
			return true
		}
	}
	return false
}

func continuationDecisionReasons() []ContinuationDecisionReason {
	return []ContinuationDecisionReason{
		ContinuationReasonNone,
		ContinuationReasonNotWindowOnly,
		ContinuationReasonWindowVeto,
		ContinuationReasonChangedQuestion,
		ContinuationReasonIndeterminateIdentity,
		ContinuationReasonMissingContext,
		ContinuationReasonInvalidContext,
		ContinuationReasonContextVersionMismatch,
		ContinuationReasonFreshContextUnavailable,
		ContinuationReasonBindingUnavailable,
		ContinuationReasonStructureVeto,
		ContinuationReasonExplicitStructureHint,
		ContinuationReasonRequestInvalid,
		ContinuationReasonPrincipalUnauthenticated,
		ContinuationReasonRequestTimeUnresolvable,
		ContinuationReasonRequestCancelled,
		ContinuationReasonAsOfUnresolvable,
		ContinuationReasonCompositionInvalid,
		ContinuationReasonWindowSuperseded,
		ContinuationReasonAnswerBudgetChanged,
		ContinuationReasonSemanticStateAbsent,
		ContinuationReasonSemanticStateInvalid,
		ContinuationReasonUnspecified,
	}
}

// ValidContinuationDecisionReason reports membership, so an unrecognised value
// cannot reach a log line.
func ValidContinuationDecisionReason(reason ContinuationDecisionReason) bool {
	for _, member := range continuationDecisionReasons() {
		if member == reason {
			return true
		}
	}
	return false
}

func (d windowContinuationDecision) Applies() bool {
	return d.Disposition == ContinuationApplied && d.Accepted != nil
}

// newWindowContinuationDecision is the ONLY way this package builds a decision.
//
// A CONSTRUCTOR RATHER THAN A STRUCT LITERAL, and r3 is why. Two exits above the
// old literal -- an unanswerable caller time bound and an already-cancelled
// context -- returned without assigning a reason, and one of them published
// `unspecified` on a live line while the other published nothing at all. A
// literal lets a new field default; a constructor makes every field a decision
// someone had to make. Callers then narrow the reason with withReason as they
// learn more.
//
// The initial reason is the strongest thing knowable with no I/O: the
// window-only SHAPE is a pure function of the request, so a turn that ends
// before admission still reports `not_window_only` when that is true rather
// than the fail-closed member.
func newWindowContinuationDecision(request InvestigationRequest) windowContinuationDecision {
	decision := windowContinuationDecision{
		Observed:       requestCarriesWindowReceipts(request),
		Disposition:    ContinuationNotApplicable,
		Reason:         ContinuationReasonUnspecified,
		SeedSource:     CarrySeedNone,
		ConflictReason: ContinuationConflictNone,
		ConflictFields: []ContinuationConflictField{},
		// SET IN THE CONSTRUCTOR, ABOVE EVERY RETURN. The composition outcome
		// is a field on this event, so it reaches the line on every turn the
		// event is emitted -- including the turns where no composition ran at
		// all, which is what this member says. Assigning it only where a
		// composition FAILED left the successful path publishing the empty
		// string, and left the vocabulary's own membership check with no
		// production caller at all.
		CompositionOutcome: CompositionNotEvaluated,
		// A pure function of the request, set above every return for the same
		// reason: an early exit still reports what the caller sent.
		WindowReceiptCount:    len(request.PriorWindowReceipts),
		ExplicitWindowPresent: request.TimeContext.EvidenceWindow != nil,
		AxisOutcome:           ContinuationAxisNotEvaluated,
	}
	if decision.Observed {
		decision.SeedSource = CarrySeedReceipt
		if _, windowOnly := windowOnlyReferencedResultID(request); !windowOnly {
			decision.Reason = ContinuationReasonNotWindowOnly
		}
	}
	return decision
}

// withReason narrows the reason on a path that is about to end the turn. It
// never widens back to `unspecified`: a site that already knows something more
// specific keeps it.
func (d windowContinuationDecision) withReason(reason ContinuationDecisionReason) windowContinuationDecision {
	if reason == ContinuationReasonUnspecified {
		return d
	}
	d.Reason = reason
	return d
}

// requestCarriesWindowReceipts is the event's denominator.
func requestCarriesWindowReceipts(request InvestigationRequest) bool {
	for _, receipt := range request.PriorWindowReceipts {
		if strings.TrimSpace(receipt.ResultID) != "" {
			return true
		}
	}
	return false
}

// windowOnlyReferencedResultID reports the ONE prior result a window-only
// continuation may continue, and whether the request has that shape at all.
//
// THE SHAPE IS THE CONTAINMENT (D-d). Every other prior-result reference and
// every typed selection disqualifies the transition, because each of them is a
// semantic change the caller made on THIS turn -- and a continuation is
// defined as the turn that changed only the evidence window. A request that
// selects a subject, kind, anchor, handle or candidate AND redeems a window
// offer is that selection's turn; it takes its own ratified transition.
//
// Deliberately computed from the REQUEST alone, with no I/O, so it can be
// decided before anything expensive runs and so a veto path can report it.
func windowOnlyReferencedResultID(request InvestigationRequest) (string, bool) {
	var seen []string
	for _, receipt := range request.PriorWindowReceipts {
		id := strings.TrimSpace(receipt.ResultID)
		if id == "" {
			continue
		}
		seen = append(seen, id)
	}
	// Plural window receipts are CHAOS-5271's territory (resolveWindowReceipts
	// vetoes them). They are never a continuation here either, and the two
	// facts are kept separate: this returns false, it does not suppress the
	// veto.
	if len(seen) != 1 {
		return "", false
	}
	if strings.TrimSpace(request.ParentResultID) != "" {
		return "", false
	}
	if len(request.PriorSubjectReceipts) > 0 ||
		len(request.PriorKindReceipts) > 0 ||
		len(request.PriorAnchorReceipts) > 0 ||
		len(request.PriorHandleReceipts) > 0 ||
		len(request.PriorCandidateReceipts) > 0 {
		return "", false
	}
	return seen[0], true
}

// continuationQuestionIdentity applies D-a's identity rule to the two
// questions.
//
// RAW BYTES, and the design says why: "equal byte lengths alone prove nothing"
// and "a byte-different question is outside this first-cut continuation rule,
// including when canonicalization considers it equivalent". The canonical
// identity guard is kept ON TOP of that, reused from the same-question
// containment one file over rather than re-derived, because two questions that
// both canonicalize to the empty string are not the same question -- they are
// questions the hash cannot tell apart.
func continuationQuestionIdentity(requestQuestion, priorQuestion string) ContinuationDecisionReason {
	if CanonicalizeQuestion(requestQuestion) == "" || CanonicalizeQuestion(priorQuestion) == "" {
		return ContinuationReasonIndeterminateIdentity
	}
	if requestQuestion != priorQuestion {
		return ContinuationReasonChangedQuestion
	}
	return ContinuationReasonNone
}

// admitWindowContinuation decides eligibility and carrier admission. It runs
// AFTER receipt validation and graph binding and BEFORE the fresh
// interpretation is consumed for anything, so the accepted context is settled
// before any consumer of a fresh semantic value can act on it.
//
// It performs NO fresh-value comparison: that is a separate step
// (compareContinuationProposal) taken once the proposal exists, precisely so a
// failure of the DIAGNOSTIC proposal cannot invalidate an independently
// admitted carrier.
func (e *Engine) admitWindowContinuation(
	ctx context.Context,
	principal storage.Principal,
	request InvestigationRequest,
	binding ResolvedGraphBinding,
	preloaded map[string]StoredInvestigationResult,
	appliedWindow *contractsv1.ContextFabricEffectiveEvidenceWindow,
	// interpretedAxis is RECORDED, NEVER DECIDED ON (CHAOS-5582). It is passed
	// in for the same reason appliedWindow is: this function builds the
	// decision from the constructor, so a value the caller stamped on the
	// previous decision would not survive the rebuild, and the line would
	// publish an empty fresh axis for exactly the turns it exists to explain.
	interpretedAxis contractsv1.ContextFabricTemporalAxis,
) windowContinuationDecision {
	// ONE CONSTRUCTOR (r2 F2). This function used to build its own struct
	// literal, which silently dropped every field the constructor sets and the
	// literal did not repeat -- the composition outcome among them, so an
	// ordinary admission exit published `unrecognised` on a closed field. Two
	// initialisers for one struct is the same shape as two authorities for one
	// object, which is the defect this whole seam was re-cut to remove.
	decision := newWindowContinuationDecision(request)
	// R1-4: the window that this turn actually resolved, passed in from the
	// canonicalisation that decided it rather than re-derived here. Declared
	// and logged is not the same as populated -- a field that is always empty
	// is a field the line cannot answer with.
	decision.AppliedWindow = appliedWindow
	decision.InterpretedAxis = interpretedAxis

	// THE SHAPE IS DECIDED ONCE, HERE, BEFORE ANY DISQUALIFIER CAN RETURN
	// (r2 F1). WindowOnlyShape describes the CARRIER'S SHAPE -- identical
	// question bytes, exactly one window receipt, nothing else referenced --
	// and nothing a disqualifier later finds changes what shape the request
	// arrived in. Assigning it after the disqualifiers meant the interpreted-
	// axis veto returned with the flag still false, `BlocksLegacyCarry`
	// answered false, and the old family-only carry served the very carrier
	// this gate had just refused. That is the third exit to lose this
	// containment; deciding it above every return is what stops there being a
	// fourth.
	referenced, windowOnly := windowOnlyReferencedResultID(request)
	decision.WindowOnlyShape = decision.Observed && windowOnly
	if !decision.Observed {
		decision.Reason = ContinuationReasonNotWindowOnly
		return decision
	}
	decision.SeedSource = CarrySeedReceipt

	if !windowOnly {
		decision.Reason = ContinuationReasonNotWindowOnly
		return decision
	}
	decision.ReferencedResultID = referenced
	// DISQUALIFIER (R2-1). A caller stating structure on THIS turn -- an
	// expected kind, a subject handle, or any requested scope (repositories,
	// projects, teams, subject hints) -- is not a receipt, so the receipt-field
	// scan above cannot see it, and it is exactly the semantic change that
	// makes this NOT a window-only continuation. Every request field is decided
	// by name in TestWindowContinuation_EveryRequestFieldIsDecidedByName.
	if requestStatesStructure(request) {
		decision.Reason = ContinuationReasonExplicitStructureHint
		return decision
	}
	// NO FRESH-AXIS DISQUALIFIER (CHAOS-5582). The fresh interpreted axis is
	// deliberately not an input to admission: admission decides whether the
	// carrier's reading continues, and the carrier recorded its own axis. What
	// happens to a fresh axis that disagrees is decided once, after admission
	// and composition are final, by decideContinuationAxis -- and R2-4's rule
	// still holds, because a continuation that is applied executes under the
	// carried axis, so the axis-conflict veto cannot undo it.
	if e.results == nil {
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonInvalidContext
		return decision
	}

	// Normally a memo hit: window-receipt redemption read this carrier
	// successfully moments ago in the same request (engine.go, carryCtx), and
	// a request whose redemption read FAILED never reaches admission -- it
	// ends on the retryable window veto. A failure here is therefore reached
	// only by a caller without the memo, and it is published as its own
	// carrier_read value rather than folded silently into invalid_context.
	stored, err := carryLoadResult(ctx, e.results, principal, referenced)
	if err != nil {
		decision.CarrierRead = ContinuationCarrierReadFailed
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonInvalidContext
		return decision
	}
	decision.CarrierRead = ContinuationCarrierReadOK
	// The SAME CHAOS-3898 ingress taint gate every other carrier check
	// applies, and it is applied here even for a preloaded entry's id,
	// because this decision is about semantic authority rather than about a
	// hint: a rebuild between turns can legitimately change what the prior
	// reading meant.
	if stored.GraphEpoch == nil || *stored.GraphEpoch != binding.Epoch {
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonInvalidContext
		return decision
	}
	// The PRELOAD CACHE holds whole carriers, snapshot included, so a cached
	// entry is read exactly as a fresh one is -- never narrowed to its
	// payload and then missing the reading it carries.
	if cached, ok := preloaded[referenced]; ok {
		stored = cached
	}
	prior := stored.Result
	decision.CarriedAxis = prior.Interpretation.TimeContext.Axis

	if reason := continuationQuestionIdentity(request.Question, prior.Question); reason != ContinuationReasonNone {
		// NOT a withholding: the transition was never established, so there
		// was no continuation to withhold. The window receipt keeps its
		// existing treatment; what it may not do is import this context.
		decision.Disposition = ContinuationNotApplicable
		decision.Reason = reason
		return decision
	}
	// CHAOS-5582: the transition is established here -- shape, readable
	// taint-valid carrier, identical question. Every check below is about the
	// carried READING, and none of them changes what the user confirmed.
	decision.TransitionEstablished = true

	plan := carriablePlan(prior)
	if plan == nil {
		// The carrier read back perfectly and had nothing to continue.
		decision.Disposition = ContinuationNotApplicable
		decision.Reason = ContinuationReasonMissingContext
		return decision
	}
	// THE ANSWER-SHAPING OPTION TURN ONE RECORDED. Every consumer sends every
	// option on every request, so an option cannot disqualify by being
	// present -- only by DIFFERING from turn one. The one turn one recorded is
	// the effective response byte budget (service ceiling narrowed by the
	// caller's max_serialized_bytes), stamped on the carrier's plan. Compared
	// as the EFFECTIVE value on both sides, byte for byte: the raw option is
	// not what shaped either answer. The other answer-shaping options are not
	// recorded at turn one; the stacked semantic-state change compares them.
	if e.effectiveResponseBudget(request).MaxSerializedBytes != plan.Budget.MaxSerializedBytes {
		decision.Disposition = ContinuationNotApplicable
		decision.Reason = ContinuationReasonAnswerBudgetChanged
		return decision
	}
	// D-a: revalidate under the RECORDED standard. A carrier stamped by a
	// different family definition table is not reinterpreted under today's;
	// an unsupported version is an admission failure.
	//
	// EXACT, AND NO "ABSENT" ALLOWANCE. The stamp is compared byte for byte:
	// a blank, whitespace-only or padded stamp names no table this build can
	// verify, so it is a mismatch like any other. (The contract already
	// requires a non-empty family_version, so an allowance for "no stamp"
	// admitted only whitespace -- a stamp that is not the table in force.)
	if plan.FamilyVersion != QuestionFamilyTableVersion {
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonContextVersionMismatch
		return decision
	}
	// CHAOS-5582, D-a's "revalidate under the recorded standard" for the time
	// axis: the window this turn redeems was offered under the carrier's
	// current axis, so a carrier recording any other axis has no reading a
	// window-only turn can continue. Withheld, never reinterpreted onto today's
	// axis.
	if decision.CarriedAxis != contractsv1.ContextFabricTemporalCurrent {
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonInvalidContext
		return decision
	}

	// THE READING ITSELF. The plan above is the public half; the snapshot is
	// the whole of it. Without a usable snapshot there is no reading to
	// continue, and the plan's family label is not a substitute for one.
	decision.CarriedStateConsulted = true
	decision.CarriedStateRead = stored.SemanticStateRead
	if reason := semanticStateAdmission(stored, plan); reason != ContinuationReasonNone {
		decision.Disposition = ContinuationWithheld
		decision.Reason = reason
		return decision
	}
	decision.Carried = &continuationCarriedContext{
		Family:         plan.Family,
		GroupKind:      plan.GroupKind,
		NarrowingBasis: plan.Budget.NarrowingBasis,
		FamilyVersion:  plan.FamilyVersion,
		SourceResultID: prior.ResultID,
		State:          cloneSemanticState(stored.SemanticState),
	}
	decision.Accepted = decision.Carried
	// THE FRAME IS NOT BUILT HERE. Admission decides WHETHER this turn
	// continues and WHAT reading it continues; composing that reading into a
	// frame, validating the composition and deciding its gate belong to
	// composeAcceptedContext. Building a frame here is what the previous
	// attempt did, and it is why a frame could be served under a gate that had
	// certified a different object.
	decision.Disposition = ContinuationApplied
	decision.Reason = ContinuationReasonNone
	return decision
}

// decideContinuationAxis reconciles THIS turn's fresh interpreted time with an
// established window-only transition, and returns the time the rest of the
// turn executes under together with the outcome the line reports (CHAOS-5582).
//
// ONE RULE. When the transition is established, the carrier recorded
// `current`, the caller's own axis is `current` and a window commitment was
// resolved, a fresh axis that moved off current is a DIAGNOSTIC: the turn
// executes under the caller's clamped current axis -- the one the window was
// canonicalized against -- and the confirmed window stays applied. In every
// other case the fresh interpretation governs, exactly as before, and a
// resolved window commitment on a non-current fresh axis ends at the
// axis-conflict veto.
//
// KEYED ON THE TRANSITION, NOT ON THE APPLIED READING. A corpus replicate
// measured the difference: the carried family could not be composed onto the
// fresh frame (withheld, composition_invalid), the fresh axis drifted to
// `range`, and a rule keyed on `applied` let the sampled axis refuse the very
// question whose window the user had just confirmed. Whether the family
// reading continues and which time the confirmed window speaks for are
// separate facts; a changed question still follows its own fresh reading (D-d).
//
// THE RECEIPT CONFLICTS ARE NOT DECIDED HERE, and that is the separation the
// design of record holds: plural receipts and an explicit window beyond skew
// veto in resolveWindowReceipts before Interpret runs, so no request carrying
// either reaches this function.
//
// Called after composition and the comparison, so every field it reads is
// final for the turn.
//
// THE FRESH TIME IS ONE VALUE, AXIS AND BOUND TOGETHER. A fresh `current` axis
// whose bound is unanswerable (a present-zero instant) is as much a sampled
// failure as a drifted axis, and on an established transition it is overridden
// the same way; only a fresh `current` axis with an answerable bound `agrees`.
// Without an established transition the fresh time governs whole: a drifted
// axis reaches the axis-conflict veto, an unanswerable bound the bound exit.
func decideContinuationAxis(
	decision windowContinuationDecision,
	fresh TimeContext,
	freshAnswerable bool,
	requestTime TimeContext,
	windowCommitted bool,
) (TimeContext, ContinuationAxisOutcome) {
	if !windowCommitted {
		return fresh, ContinuationAxisNotEvaluated
	}
	if fresh.Axis == contractsv1.ContextFabricTemporalCurrent && freshAnswerable {
		return fresh, ContinuationAxisAgreed
	}
	if decision.TransitionEstablished &&
		decision.CarriedAxis == contractsv1.ContextFabricTemporalCurrent &&
		requestTime.Axis == contractsv1.ContextFabricTemporalCurrent {
		// The caller's axis and instant only: a fresh proposal's range bounds
		// are not carried onto the current axis, and the caller's requested
		// window is already the applied window, not a second copy on the
		// interpretation.
		return TimeContext{Axis: requestTime.Axis, AsOf: requestTime.AsOf}, ContinuationAxisOverriddenByReceipt
	}
	if fresh.Axis == contractsv1.ContextFabricTemporalCurrent {
		// The axis agrees; the unanswerable bound is the bound exit's to refuse.
		return fresh, ContinuationAxisAgreed
	}
	return fresh, ContinuationAxisVetoed
}

// compareContinuationProposal folds the fresh interpreter return in as a
// NON-AUTHORITATIVE comparison and records the disagreement.
//
// FAIL-CLOSED AGAINST SEMANTIC SUBSTITUTION, and this is the whole ordering
// rule in one function: an unavailable or failed proposal records an
// unevaluated comparison and the carrier still wins. Only failure to establish
// the CARRIER changes what is served.
func compareContinuationProposal(decision windowContinuationDecision, fresh continuationFreshProposal) windowContinuationDecision {
	decision.Fresh = fresh
	if !decision.Applies() {
		// Nothing was accepted, so there is nothing to compare against. Both
		// booleans stay false; the disposition is what tells the two apart.
		decision.ComparisonEvaluated = false
		decision.Agreement = false
		decision.ConflictReason = ContinuationConflictNone
		decision.ConflictFields = []ContinuationConflictField{}
		return decision
	}
	if !fresh.Available {
		decision.ComparisonEvaluated = false
		decision.Agreement = false
		decision.ConflictReason = ContinuationConflictNone
		decision.ConflictFields = []ContinuationConflictField{}
		return decision
	}

	accepted := *decision.Accepted
	// EVERY differing component, in a fixed order -- not merely the first.
	// A same-family subject-expression substitution is exactly the shape that
	// is invisible when only the family is compared.
	differs := semanticStateDifferences(accepted.State, fresh.State)
	differs[ContinuationConflictFieldFamily] = accepted.Family != fresh.Family
	fields := []ContinuationConflictField{}
	for _, field := range continuationComparableFields() {
		if differs[field] {
			fields = append(fields, field)
		}
	}
	decision.ComparisonEvaluated = true
	decision.ConflictFields = fields
	decision.Agreement = len(fields) == 0
	if len(fields) == 0 {
		decision.ConflictReason = ContinuationConflictNone
	} else {
		decision.ConflictReason = ContinuationConflictNonWindowContext
	}
	return decision
}

// applyWindowContinuation makes the accepted context the turn's own reading.
//
// IT TAKES THE COMPOSED CONTEXT, and that is the whole difference from the
// attempt this replaces. The frame and the gate come from
// composeAcceptedContext, which validated them together; this function does not
// build, substitute into, or nil a frame. It only installs what the boundary
// already certified.
//
// A context that is not Usable installs NOTHING: a composition that failed
// validation is not a continuation to be served under the fresh frame's gate.
func applyWindowContinuation(outcome QuestionFamilyOutcome, decision windowContinuationDecision, accepted AcceptedContext) (QuestionFamilyOutcome, bool) {
	if !decision.Applies() || !accepted.Usable() {
		return outcome, false
	}
	carried := *decision.Accepted
	replaced := outcome.Family
	outcome.Family = carried.Family
	outcome.Source = QuestionFamilySourceCarried
	// ONE ACCESSOR. The winning sample and the frame are both written from the
	// SAME accepted value, so the comparison, the planner and discovery cannot
	// read different group axes for one turn -- the false-agreement defect.
	outcome.WinningSample.GroupKind = accepted.EffectiveGroupKind()
	// THE WHOLE CARRIED READING, INCLUDING ITS ABSENCE OF A FRAME. A
	// frameless carrier is continued frameless: installing the fresh frame
	// beside a carried family is the half-and-half context the continuation
	// exists to prevent. FrameObligations moves with the frame, as it does
	// everywhere it is set.
	outcome.Frame = accepted.Frame
	outcome.FrameObligations = nil
	if accepted.Frame != nil {
		outcome.FrameObligations = append([]AnswerObligation(nil), accepted.Frame.Obligations...)
	}
	outcome.Gate = accepted.Gate
	outcome.Route = FamilyRouteDecision{
		Family:      carried.Family,
		Source:      FamilyRouteSourceCarried,
		Class:       "",
		Disposition: FamilyRouteCarried,
		Switched:    carried.Family != replaced,
	}
	return outcome, true
}

// continuationContextID is a short, content-safe digest identifying an
// immutable decision artifact.
//
// IDS, CLOSED VALUES AND EQUALITY RESULTS ONLY -- never corpus question text
// or subject labels. The carried context's artifact is the prior RESULT, so
// its id is that result id; a fresh proposal has no stored artifact, so it is
// identified by a digest of its own closed values, which is stable across
// replicates and reveals nothing.
func continuationContextID(family QuestionFamily, groupKind SubjectKind, basis contractsv1.ContextFabricNarrowingBasis) string {
	sum := sha256.Sum256([]byte(string(family) + "\x1f" + string(groupKind) + "\x1f" + string(basis)))
	return "cfctx_" + hex.EncodeToString(sum[:8])
}

// CarriedContextID / FreshContextID / AcceptedContextID render the three
// artifact ids, explicitly empty when that context is absent.
func (d windowContinuationDecision) CarriedContextID() string {
	if d.Carried == nil {
		return ""
	}
	return d.Carried.SourceResultID
}

func (d windowContinuationDecision) FreshContextID() string {
	if !d.Fresh.Available {
		return ""
	}
	return continuationContextID(d.Fresh.Family, d.Fresh.GroupKind, "")
}

func (d windowContinuationDecision) AcceptedContextID() string {
	if d.Accepted == nil {
		return ""
	}
	return d.Accepted.SourceResultID
}

// FamilyCarried / FamilyFresh / FamilyAccepted are the three family readings,
// explicitly empty where that context is absent -- never a fabricated family.
func (d windowContinuationDecision) FamilyCarried() QuestionFamily {
	if d.Carried == nil {
		return ""
	}
	return d.Carried.Family
}

func (d windowContinuationDecision) FamilyFresh() QuestionFamily {
	if !d.Fresh.Available {
		return ""
	}
	return d.Fresh.Family
}

func (d windowContinuationDecision) FamilyAccepted() QuestionFamily {
	if d.Accepted == nil {
		return ""
	}
	return d.Accepted.Family
}

// AcceptedGroupKind is the group axis of the accepted context, empty when
// nothing was accepted.
//
// NIL-SAFE ON PURPOSE, and the reason is a property rather than defensiveness:
// a WITHHELD turn clears Accepted, so every reader of the accepted axis has to
// cope with "there is no accepted context" -- which is exactly the state the
// event exists to report. A reader that dereferenced the pointer would work
// only on the applied path and crash on the disclosure the event was added for.
func (d windowContinuationDecision) AcceptedGroupKind() SubjectKind {
	if d.Accepted == nil {
		return ""
	}
	return d.Accepted.GroupKind
}

// AcceptedFamilySource is the provenance value of the accepted context, empty
// when nothing was accepted.
func (d windowContinuationDecision) AcceptedFamilySource() QuestionFamilySource {
	if d.Accepted == nil {
		return ""
	}
	return QuestionFamilySourceCarried
}

// carriedState and freshState are the two snapshots the decision line renders,
// nil where that reading does not exist.
func (d windowContinuationDecision) carriedState() *PersistedSemanticState {
	if d.Carried == nil {
		return nil
	}
	return d.Carried.State
}

func (d windowContinuationDecision) freshState() *PersistedSemanticState {
	if !d.Fresh.Available {
		return nil
	}
	return d.Fresh.State
}

// ConflictCount is explicitly zero or the number of differing components.
func (d windowContinuationDecision) ConflictCount() int { return len(d.ConflictFields) }

// ConflictFieldTokens renders conflict_fields as a stable, content-safe list.
func (d windowContinuationDecision) ConflictFieldTokens() []string {
	tokens := make([]string, 0, len(d.ConflictFields))
	for _, field := range d.ConflictFields {
		tokens = append(tokens, string(field))
	}
	return tokens
}

// AppliedWindowToken renders the actual applied window -- relative id, frozen
// bounds and provenance -- or the empty string when no window applied.
//
// Rendered rather than logged as a struct so the line is one closed,
// greppable value and so an absent window is explicitly empty rather than a
// zero-valued object that reads like a real window with empty bounds.
func (d windowContinuationDecision) AppliedWindowToken() string {
	if d.AppliedWindow == nil {
		return ""
	}
	window := *d.AppliedWindow
	var start, end string
	if window.Start != nil {
		start = window.Start.UTC().Format("2006-01-02T15:04:05Z")
	}
	if window.End != nil {
		end = window.End.UTC().Format("2006-01-02T15:04:05Z")
	}
	return string(window.RelativeID) + "|" + start + "|" + end + "|" + string(window.Provenance)
}

// BlocksLegacyCarry reports whether this decision must also stop the OLD
// family-only plan carry from applying.
//
// D-d, stated as its own predicate because the design names the exact escape:
// "Do not let failure of the continuation gate fall through into the old
// family-only carry when the fresh question is `unclassified`."
//
// The hole is real and it is reachable today. carryReferencedResultIDs scans
// PriorWindowReceipts FIRST and the receipt walk is deliberately UNGATED for
// question identity -- a receipt is a redeemed server offer, so the ungated
// treatment is correct for the window it redeems. But it means a window
// receipt can seed the plan carry from a carrier this gate refused, and if
// this turn then classifies nothing, applyCarriedPlan installs that reading
// anyway.
//
// KEYED ON THE SHAPE AND THE OUTCOME, NOT ON A LIST OF REASONS. The first
// version of this predicate named two reasons (changed question, indeterminate
// identity) and the counted review found the gap by executing it: a carrier
// refused for a VERSION MISMATCH was withheld by this gate and then served by
// the legacy carry, `family_source=carried`, from the same refused carrier.
// Enumerating reasons is the same open-set mistake carryOriginSameQuestionVerdict's
// own doc comment describes -- every reason added later has to be remembered
// here. So the rule is the closed one: if this request IS the window-only
// continuation shape and the continuation did NOT apply, no other route may
// serve that carrier's reading this turn.
//
// It deliberately does NOT fire when the shape is not window-only. There the
// family-only carry is the correct mechanism and blocking it would break the
// clarification loop the carry exists to serve.
//
// Reachability is UNCHANGED by this: carryReferencedResultIDs returns exactly
// what it returned, so the answer-reuse bypass keyed on the same population
// (CHAOS-4998) is untouched. What is narrowed is what a receipt-rooted hit is
// allowed to MEAN.
func (d windowContinuationDecision) BlocksLegacyCarry() bool {
	return d.Observed && d.WindowOnlyShape && d.Disposition != ContinuationApplied
}

// blockedLegacyCarryOutcome is the miss this containment reports.
//
// The two identity reasons map to the EXISTING members with their existing
// meanings -- carryOriginVerdict already refuses to collapse drift into
// indeterminacy and that distinction is preserved here. Everything else is
// reported as its own member rather than being forced into one of those two,
// for the same reason: a false basis in the telemetry is worse than a new
// member, and "the continuation gate refused this carrier" is not "the origin
// answered a different question".
func (d windowContinuationDecision) blockedLegacyCarryOutcome() PlanCarryOutcome {
	switch d.Reason {
	case ContinuationReasonIndeterminateIdentity:
		return PlanCarryMissQuestionIndeterminate
	case ContinuationReasonChangedQuestion:
		return PlanCarryMissQuestionDrift
	default:
		return PlanCarryMissContinuationWithheld
	}
}

// applyAndRecordContinuation applies the accepted context and emits the ONE
// existing event that can carry `family_source=carried`.
//
// IT EMITS RecordPlanCarry TOO, and that is deliberate rather than incidental.
// A continuation IS an applied carry: it stamps QuestionFamilySourceCarried on
// the served plan. Leaving the existing applied-carry counter at zero while
// `family_source=carried` appeared on the wire would reintroduce, in the
// opposite direction, exactly the numerator/denominator split CHAOS-5003
// closed -- an operator would see served carries that the carry telemetry says
// never happened.
//
// The event is built from the SAME decision that drives execution, through the
// existing PlanCarryEventFrom constructor, so the two lines cannot describe
// different families.
func (e *Engine) applyAndRecordContinuation(ctx context.Context, principal storage.Principal, outcome QuestionFamilyOutcome, decision windowContinuationDecision, accepted AcceptedContext) QuestionFamilyOutcome {
	carried, applied := applyWindowContinuation(outcome, decision, accepted)
	if !applied {
		return outcome
	}
	if e.telemetry != nil {
		// THE EVENT IS BUILT DIRECTLY, NOT THROUGH A SYNTHESIZED
		// planCarryResult, and the reason is a structural guard this package
		// already enforces: TestCarryGateClosure refuses any
		// planCarryResult{Outcome: PlanCarryHit} constructed outside
		// resolveCarriedPlan's reach, because a hit built elsewhere is a hit
		// that never met the same-question comparison. That refusal is
		// correct and it is kept. This continuation met a STRICTER identity
		// rule (raw question bytes, not a hash) inside
		// admitWindowContinuation -- but manufacturing a carry hit to say so
		// would put a value into the one shape the guard exists to forbid,
		// and the next reader would have to re-derive why it was safe.
		//
		// outcome.Family is still the PRE-continuation value here, matching
		// applyAndRecordCarry's own ordering and for the same reason.
		e.telemetry.RecordPlanCarry(ctx, principal, PlanCarryEvent{
			FamilyReplaced: outcome.Family,
			FamilyCarried:  carried.Family,
			SourceResultID: decision.Accepted.SourceResultID,
			Route:          carried.Route,
		})
	}
	return carried
}

// requestStatesStructure reports whether the request states subject structure
// of its own: an expected kind, a subject handle, or any requested scope.
func requestStatesStructure(request InvestigationRequest) bool {
	scope := request.RequestedScope
	return len(request.ExpectedKinds) > 0 || len(request.SubjectHandles) > 0 ||
		len(scope.RepositorySlugs) > 0 || len(scope.ProjectIDs) > 0 || len(scope.TeamIDs) > 0 || len(scope.SubjectHints) > 0
}
