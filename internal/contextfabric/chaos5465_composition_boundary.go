package contextfabric

import "encoding/json"

// CHAOS-5465: the composition boundary for a window-only continuation.
//
// WHY THIS FILE EXISTS, and it is worth saying plainly because the previous
// attempt at this ticket was closed for getting it wrong.
//
// A window-only turn two -- identical question, one redeemed window receipt,
// nothing else changed -- should continue turn one's validated reading rather
// than re-derive it. The first attempt achieved that by SUBSTITUTING the
// carried group axis into the fresh frame after that frame had been validated,
// and keeping the fresh frame's passing gate. A design review reproduced the
// consequence: a frame that passes validation, has one field replaced, and is
// then served under the old gate can violate an invariant the gate certified.
// Measured, not argued:
//
//	fresh_valid=true fresh_gate=passed disposition=applied
//	accepted_group="team" executed_member="team"
//	invalid=true invariant=i6 detail=group_kind_equals_member_kind
//
// I6 forbids a grouped expression whose group kind equals its member kind. The
// fresh frame satisfied it; the composed frame did not; the gate still said
// `passed`, and discovery consumed the composition.
//
// THE RULE THIS FILE ENFORCES: the frame that consumers receive is the frame
// that was validated, and the gate they read is the gate decided ON THAT FRAME.
// Composition and validation are one step, in one place, or they are two
// authorities for one object and the second one wins silently.
//
// It follows that composition can FAIL. A carried reading that cannot be
// established as a valid frame is not a continuation to be served under a
// borrowed gate -- it is a continuation that could not be established, and the
// turn is refused (chaos5465_continuation_refusal.go).
//
// WHAT IS COMPOSED, NOW THAT THE READING PERSISTS. The prior turn's WHOLE
// accepted reading -- frame, gate verdict, roles, requirement declarations --
// is carried in its persisted semantic snapshot, so composition no longer
// substitutes a carried group axis into the fresh frame. The accepted frame IS
// the carried frame, revalidated under today's validation with the inputs it
// was validated with, and refused if today's rules would repair, reject or
// refuse it. The only delta a window-only continuation applies is the window,
// which is not part of the frame. The fresh frame is a comparison and nothing
// else: its disagreement -- even its own gate's refusal -- is not authority to
// rewrite or refuse the admitted reading.

// CompositionOutcome is the closed verdict of composeAcceptedContext.
//
// Its members are the states a COMPOSITION can be in, which is deliberately not
// the same vocabulary as "why a continuation was not admitted": admission asks
// whether this turn is a continuation at all, composition asks whether the
// accepted reading can be expressed as a valid frame. Sharing one vocabulary
// between the two would make "the caller changed the subject" and "the carried
// group axis is illegal here" indistinguishable in the data.
type CompositionOutcome string

const (
	// CompositionAccepted: the composed frame validated and its gate was
	// decided on that composition.
	CompositionAccepted CompositionOutcome = "accepted"
	// CompositionUnchanged: the carried reading already matched the fresh
	// frame, so the composition IS the fresh frame. Distinct from `accepted`
	// because it means no substitution occurred -- an operator reading a
	// continuation rate needs to tell "we carried something" from "there was
	// nothing to carry that was not already there".
	CompositionUnchanged CompositionOutcome = "unchanged"
	// CompositionInvalid: substituting the carried reading produced a frame
	// that fails a frame invariant. The named invariant rides with it.
	CompositionInvalid CompositionOutcome = "invalid"
	// CompositionNoFreshFrame: the interpreter proposed no frame, so there is
	// nothing to compose into. NOT an error: a turn with no proposed frame is
	// ordinary, and this says so rather than reporting a validation failure
	// for a validation that never ran.
	CompositionNoFreshFrame CompositionOutcome = "no_fresh_frame"
	// CompositionFreshRefused: the fresh frame did not pass its own gate, so
	// there is no validated frame to compose into and the fresh refusal stands.
	// Composition never rescues a frame the server already refused.
	CompositionFreshRefused CompositionOutcome = "fresh_refused"
	// CompositionNotEvaluated: no composition ran on this turn, because the
	// request was not a continuation at all.
	//
	// IT IS THE CONSTRUCTOR'S VALUE, not the zero value, and that is the whole
	// point of it. The first build left the field empty on every turn where no
	// composition ran, so `composition_outcome=""` meant BOTH "nothing was
	// composed" and "something was composed and nobody recorded what" -- and it
	// meant the second one on every applied continuation, which is how a
	// vocabulary with a membership check and a log key still reached the line
	// as an empty string. Non-execution has to be distinguishable from an
	// evaluated verdict, or the field cannot be counted.
	CompositionNotEvaluated CompositionOutcome = "not_evaluated"
)

// The composition's OWN invariants, deliberately not frame invariants i1..i19.
// Those name what is wrong INSIDE a frame; these name why a carried frame that
// may be internally fine cannot be established as this turn's reading.
const (
	// CompositionInvariantCarriedFrameNotCanonical: revalidating the carried
	// frame under today's rules, with the inputs it was validated with,
	// produced a DIFFERENT frame. Today's normalization would repair it, and a
	// repaired reading is a reinterpretation, not a continuation.
	CompositionInvariantCarriedFrameNotCanonical = "carried_frame_not_canonical"
	// CompositionInvariantCarriedFrameRefused: the carried frame validates and
	// today's gate refuses it (its member kind is no longer servable).
	CompositionInvariantCarriedFrameRefused = "carried_frame_refused"
	// CompositionInvariantCarriedStateIncomplete: the carried snapshot reached
	// composition without a usable reading -- a frame the recorded gate had
	// passed is missing, or the snapshot itself is absent.
	CompositionInvariantCarriedStateIncomplete = "carried_state_incomplete"
)

// compositionInvariants is the closed list of the composition's own
// invariants, in production so the emitter's membership check reads it.
func compositionInvariants() []string {
	return []string{
		CompositionInvariantCarriedFrameNotCanonical,
		CompositionInvariantCarriedFrameRefused,
		CompositionInvariantCarriedStateIncomplete,
		// Declared, and undrivable once the frame itself is carried -- see the
		// constant's own comment. It stays a member so the guard and the
		// published vocabulary agree; narrowing a closed contract member is a
		// separate decision from this change.
		CompositionInvariantCarriedAxisUnexpressible,
	}
}

// validCompositionFailedInvariant reports whether a failed-invariant value is
// one this boundary can produce: empty, a frame invariant, or its own.
func validCompositionFailedInvariant(value string) bool {
	if value == "" || ValidFrameInvariant(FrameInvariant(value)) {
		return true
	}
	for _, member := range compositionInvariants() {
		if member == value {
			return true
		}
	}
	return false
}

// CompositionInvariantCarriedAxisUnexpressible is the composition's OWN
// invariant, and it is deliberately not one of the frame invariants i1..i18.
//
// Those describe a frame that is internally wrong. This describes a frame that
// is entirely valid and simply CANNOT SAY the thing the carried reading needs
// said: a reading with no grouped expression has nowhere to put a carried group
// axis. Validation will never object, because there is nothing wrong with the
// frame -- the mismatch is between the frame and the carrier.
//
// IT IS UNDRIVABLE ONCE THE FRAME ITSELF IS CARRIED. The accepted context is
// now composed from the carrier's OWN validated frame, which expresses the
// carrier's own axis by construction, so there is no frame/carrier mismatch
// left for this boundary to find. The token stays in the published vocabulary
// -- narrowing a closed contract member is not this change's business, and the
// membership check must keep matching the schema -- and
// TestComposition_TheCarriedAxisIsNeverUnexpressible pins that no established
// transition can produce it.
const CompositionInvariantCarriedAxisUnexpressible = "carried_axis_unexpressible"

// AcceptedContext is what composeAcceptedContext returns, and it is the ONLY
// thing downstream may read.
//
// Frame and Gate travel TOGETHER, as one value, because the defect this file
// closes was exactly a frame and a gate that came from different objects. A
// caller cannot take one without the other.
type AcceptedContext struct {
	// Frame is the composed, VALIDATED frame -- the one consumers receive.
	// Nil when Outcome is CompositionNoFreshFrame.
	Frame *QuestionFrame
	// Gate is the gate decided on Frame. On CompositionInvalid it is the
	// gate for the FAILED composition, so a refusal is readable rather than
	// silently inheriting the fresh frame's `passed`.
	Gate FrameGate
	// Outcome is the closed verdict.
	Outcome CompositionOutcome
	// FailedInvariant names the invariant that refused the composition, empty
	// otherwise. It is the one thing that makes CompositionInvalid actionable.
	FailedInvariant string
	// GroupKind is THE effective group axis of the accepted context, and the
	// single accessor every consumer reads.
	//
	// ONE FIELD, ONE READER SET. The other half of the same review finding was
	// that the proposal comparison read the winning sample's group kind while
	// the planner preferred the frame's, so the decision event could publish
	// `agreement=true` with an empty conflict list while the effective axis had
	// in fact been replaced. Two sources for one value is how a log tells the
	// truth about a value nothing executed.
	GroupKind SubjectKind
}

// EffectiveGroupKind is the accessor of record for the accepted group axis.
//
// Comparison, planning and discovery all call THIS, never a frame field or a
// winning-sample field directly. It exists so that "which group axis did this
// turn execute under" has exactly one answer.
func (a AcceptedContext) EffectiveGroupKind() SubjectKind { return a.GroupKind }

// Usable reports whether downstream may execute under this context.
func (a AcceptedContext) Usable() bool {
	switch a.Outcome {
	case CompositionAccepted, CompositionUnchanged, CompositionNoFreshFrame:
		// NO FRESH FRAME IS NOT A REFUSAL. With no frame there is nothing to
		// substitute into and nothing to invalidate: the continuation carries
		// the plan alone, exactly as it did before frames existed. Treating it
		// as unusable refused every legitimate continuation of a frameless
		// turn -- a regression this boundary introduced and a review control
		// caught ("agreeing receipt must apply").
		//
		// Refusal is reserved for a SUBSTITUTION that produces an invalid
		// frame, and for a fresh frame the server had already refused.
		return true
	default:
		return false
	}
}

// compositionInput is everything the boundary needs to decide, in one value.
type compositionInput struct {
	// Carried is the admitted prior turn's persisted snapshot. Admission
	// never admits a carrier without one.
	Carried *PersistedSemanticState
	// Fresh and FreshFamily are this turn's own proposal, read ONLY to tell
	// `accepted` from `unchanged`.
	Fresh       *QuestionFrame
	FreshFamily QuestionFamily
	// FreshGate is this turn's own gate. It is consulted ONLY when the
	// window-only transition was not established; see below.
	FreshGate FrameGate
	// TransitionEstablished is the window-only transition, proven by admission:
	// the identical question bytes, exactly one valid window receipt, no
	// explicit window, and a readable taint-valid carrier. On such a turn the
	// caller has already confirmed which reading they want, so NO GATE
	// BELONGING TO THE FRESH INTERPRETATION IS CONSULTED -- not the axis, not
	// the bound, and not this frame gate. See composeAcceptedContext.
	//
	// The carried FRAME itself is durable in the snapshot, so an established
	// transition composes from what turn one actually validated and can never
	// withhold here for want of a frame to revalidate.
	TransitionEstablished bool
}

// composeAcceptedContext establishes the carried reading as this turn's
// accepted context: the carried frame, revalidated, with the gate decided on
// it. It never reads the fresh gate and never substitutes into the fresh frame.
func composeAcceptedContext(in compositionInput) AcceptedContext {
	// THE REFUSAL IS ANSWERED FIRST, BEFORE THE FRAME IS EXAMINED AT ALL.
	//
	// A refusal is a fact about the EVALUATION, not about the frame, so asking
	// "is there a frame?" ahead of it gets the order backwards. It shipped that
	// way: the nil-frame branch returned `no_fresh_frame`, which Usable()
	// accepts, so a turn whose gate had already refused came back usable and
	// ended as an applied continuation serving the carried family. The frame
	// was nil, the gate said refused_basis, and nothing in between looked.
	//
	// ON AN ESTABLISHED TRANSITION THE FRESH READING IS NOT A PARTY TO THIS
	// TURN AT ALL -- neither its frame nor the gate decided on it.
	//
	// A caller who sends the identical question bytes, exactly one valid window
	// receipt and no explicit window has already settled which reading they
	// want, and that reading has been served once under a gate of its own.
	// Consulting the fresh frame's gate there refuses the very turn the caller
	// just confirmed, for a reading nobody asked to execute. The rule is stated
	// once and covers every gate rather than excepting them one at a time: the
	// fresh proposal is DROPPED on this branch -- no frame, and a gate that says
	// none was proposed FOR THIS TURN. The carried reading rides out under its
	// own already-served frame, so a refusal on this path can come only from the
	// carried reading itself.
	if in.TransitionEstablished && in.FreshGate.Refuses() {
		in.Fresh = nil
		in.FreshGate = FrameGate{Outcome: FrameGateNotProposed}
	}
	// Every refusing cell now leaves here, frame or no frame.
	if in.FreshGate.Refuses() {
		// Composition does not get to launder a refusal: there is no validated
		// frame to substitute into, and a composition built on a refused
		// evaluation is a reading the gate never certified -- the defect this
		// file exists to close, inverted.
		return AcceptedContext{Gate: in.FreshGate, Outcome: CompositionFreshRefused}
	}
	carried := in.Carried
	if carried == nil {
		return AcceptedContext{Outcome: CompositionInvalid, FailedInvariant: CompositionInvariantCarriedStateIncomplete}
	}
	if !carried.FramePresent || carried.Frame == nil {
		if carried.Validation.GateOutcome == FrameGatePassed {
			// A passed gate certified a frame; its absence here is a
			// snapshot that lost the thing it certified.
			return AcceptedContext{Outcome: CompositionInvalid, FailedInvariant: CompositionInvariantCarriedStateIncomplete}
		}
		// A FRAMELESS READING is carried as frameless: turn one proposed no
		// frame, so neither does the continuation, whatever this turn's model
		// proposed. The group axis rides on the snapshot.
		gate := FrameGate{Outcome: carried.Validation.GateOutcome}
		if gate.Refuses() {
			return AcceptedContext{Gate: gate, Outcome: CompositionInvalid, FailedInvariant: CompositionInvariantCarriedFrameRefused}
		}
		outcome := CompositionAccepted
		if in.Fresh == nil && in.FreshFamily == carried.Family {
			outcome = CompositionUnchanged
		}
		return AcceptedContext{Gate: gate, Outcome: outcome, GroupKind: carried.GroupKind}
	}

	// REVALIDATE, DO NOT REPAIR. The carried frame is run through today's
	// validation with the inputs it was validated with; a result that differs
	// from the carried frame means today's rules would reinterpret it.
	recorded := cloneFrame(*carried.Frame)
	result := ValidateFrame(recorded, recorded.WidenedObligations, carried.Validation.EmittedShape)
	gate := DecideFrameGate(result, true)
	if result.Outcome != FrameValidationOutcomeValid {
		return AcceptedContext{Gate: gate, Outcome: CompositionInvalid, FailedInvariant: string(result.Failure.Invariant)}
	}
	if !framesEqual(result.Frame, recorded) {
		return AcceptedContext{Gate: gate, Outcome: CompositionInvalid, FailedInvariant: CompositionInvariantCarriedFrameNotCanonical}
	}
	if gate.Refuses() {
		return AcceptedContext{Gate: gate, Outcome: CompositionInvalid, FailedInvariant: CompositionInvariantCarriedFrameRefused}
	}
	validated := result.Frame
	outcome := CompositionAccepted
	if in.Fresh != nil && framesEqual(*in.Fresh, validated) && in.FreshFamily == carried.Family {
		outcome = CompositionUnchanged
	}
	return AcceptedContext{Frame: &validated, Gate: gate, Outcome: outcome, GroupKind: carried.GroupKind}
}

// framesEqual compares two frames by their canonical encodings.
func framesEqual(a, b QuestionFrame) bool {
	ae, aerr := json.Marshal(a)
	be, berr := json.Marshal(b)
	return aerr == nil && berr == nil && string(ae) == string(be)
}

// freshAcceptedContext is the accepted context for a turn with no continuation:
// the fresh frame, its own gate, and the group axis read through the SAME
// accessor every other path uses.
//
// It exists so that "no continuation" is not a special case with its own way of
// answering the group-axis question. Every turn produces an AcceptedContext;
// only the outcome differs.
func freshAcceptedContext(fresh *QuestionFrame, freshGate FrameGate, sampleGroupKind SubjectKind) AcceptedContext {
	// Same order as composeAcceptedContext, for the same reason: a refused
	// evaluation is not a usable context whether or not a frame was proposed.
	if freshGate.Refuses() {
		return AcceptedContext{Gate: freshGate, Outcome: CompositionFreshRefused}
	}
	if fresh == nil {
		// With no frame, the sample's group kind is the only thing that
		// describes the axis, and the planner has always read it there.
		return AcceptedContext{Gate: freshGate, Outcome: CompositionNoFreshFrame, GroupKind: sampleGroupKind}
	}
	group, ok := fresh.SubjectExpression.GroupKind()
	if !ok {
		group = sampleGroupKind
	}
	return AcceptedContext{Frame: fresh, Gate: freshGate, Outcome: CompositionUnchanged, GroupKind: group}
}

// compositionOutcomeVocabulary is the closed member list, enumerated FROM the
// producer rather than hand-listed beside it.
//
// A hand-maintained second list is an oracle that agrees with itself: the
// previous attempt shipped exactly that, and it could not fail for the defect
// it was written to prevent. Consumers that need the vocabulary import this.
func compositionOutcomeVocabulary() []CompositionOutcome {
	return []CompositionOutcome{
		CompositionAccepted,
		CompositionUnchanged,
		CompositionInvalid,
		CompositionNoFreshFrame,
		CompositionFreshRefused,
		CompositionNotEvaluated,
	}
}

// ValidCompositionOutcome reports membership. Used by the emitter so an
// unrecognised value cannot reach a log line.
func ValidCompositionOutcome(outcome CompositionOutcome) bool {
	for _, member := range compositionOutcomeVocabulary() {
		if member == outcome {
			return true
		}
	}
	return false
}
