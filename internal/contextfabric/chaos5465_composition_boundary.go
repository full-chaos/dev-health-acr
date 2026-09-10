package contextfabric

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
// composed into a valid frame is not a continuation to be served under a
// borrowed gate -- it is a continuation that could not be established, and the
// turn proceeds on its own fresh reading, disclosed.

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

// CompositionInvariantCarriedAxisUnexpressible is the composition's OWN
// invariant, and it is deliberately not one of the frame invariants i1..i18.
//
// Those describe a frame that is internally wrong. This describes a frame that
// is entirely valid and simply CANNOT SAY the thing the carried reading needs
// said: a fresh reading with no grouped expression has nowhere to put a carried
// group axis. Validation will never object, because there is nothing wrong with
// the frame -- the mismatch is between the frame and the carrier, which is a
// question only this boundary is in a position to ask.
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
//
// THE FAMILY IS AN INPUT NOW, and it was the omission that produced the worst
// defect this file has had. Composition used to be told only about the group
// axis, so it could not tell "the fresh reading already says this" from "the
// fresh reading cannot say this at all" -- and for a grouped family landing on
// a non-grouped frame it reported `unchanged`, served the carried family, and
// dropped the axis that family groups by. The answer was grouped-family data
// with no groups. A carried family and its axis move together or neither does,
// and a boundary that cannot see the family cannot enforce that.
type compositionInput struct {
	// Fresh is the interpreter's frame, nil when none was proposed.
	Fresh *QuestionFrame
	// FreshGate is the gate already decided on Fresh.
	FreshGate FrameGate
	// FreshFamily is the family the fresh reading resolved to.
	FreshFamily QuestionFamily
	// CarriedFamily and CarriedGroupKind are the admitted prior reading.
	CarriedFamily    QuestionFamily
	CarriedGroupKind SubjectKind
	// ModelObligations and EmittedShape are ValidateFrame's own inputs, passed
	// through unchanged so the composition is validated by exactly the sequence
	// the fresh frame was. A second, laxer validator here would reintroduce the
	// two-authorities defect in a new place.
	ModelObligations []AnswerObligation
	EmittedShape     InvestigationShape
}

// composeAcceptedContext produces the frame consumers receive, validates THAT
// frame, and decides the gate on it.
func composeAcceptedContext(in compositionInput) AcceptedContext {
	if in.Fresh == nil {
		// Nothing to compose into. The gate travels through unchanged: a turn
		// with no proposed frame has already been described by its own gate
		// (not_proposed, or a refusal), and inventing a composition verdict for
		// it would overwrite a decision someone else made correctly.
		//
		// The carried group axis still rides out, and here it IS expressible:
		// with no frame the planner reads the axis off the winning sample, which
		// is exactly where applyWindowContinuation writes it.
		return AcceptedContext{Gate: in.FreshGate, Outcome: CompositionNoFreshFrame, GroupKind: in.CarriedGroupKind}
	}
	if in.FreshGate.Refuses() {
		// The server already refused this frame. Composition does not get to
		// launder that: there is no validated frame to substitute into, and a
		// composition built on a refused frame would be a frame the gate never
		// certified -- the defect, inverted.
		return AcceptedContext{Gate: in.FreshGate, Outcome: CompositionFreshRefused}
	}

	freshGroup, grouped := in.Fresh.SubjectExpression.GroupKind()
	if !grouped {
		if in.CarriedGroupKind != "" {
			// THE FRAME CANNOT SAY IT. A reading with no grouped expression has
			// nowhere to put the carried axis, so carrying the family here would
			// serve a grouped family whose groups do not exist. Refused, and
			// named: this is not a frame invariant, it is a mismatch between an
			// entirely valid frame and the carrier.
			return AcceptedContext{
				Gate: in.FreshGate, Outcome: CompositionInvalid,
				FailedInvariant: CompositionInvariantCarriedAxisUnexpressible,
			}
		}
		if in.CarriedFamily == in.FreshFamily {
			// Nothing was carried that was not already there.
			return AcceptedContext{Frame: in.Fresh, Gate: in.FreshGate, Outcome: CompositionUnchanged, GroupKind: freshGroup}
		}
		// A family IS carried; the frame needs no substitution to express it,
		// but the composition is a real one and says so.
		return AcceptedContext{Frame: in.Fresh, Gate: in.FreshGate, Outcome: CompositionAccepted, GroupKind: freshGroup}
	}
	if in.CarriedGroupKind == freshGroup {
		if in.CarriedFamily == in.FreshFamily {
			// UNCHANGED MEANS THE FRESH READING ALREADY MATCHES, on BOTH axes of
			// the carry. Reporting it for a turn that carried a different family
			// made "we carried something" and "there was nothing to carry"
			// indistinguishable in the data.
			return AcceptedContext{Frame: in.Fresh, Gate: in.FreshGate, Outcome: CompositionUnchanged, GroupKind: freshGroup}
		}
		return AcceptedContext{Frame: in.Fresh, Gate: in.FreshGate, Outcome: CompositionAccepted, GroupKind: freshGroup}
	}

	// THE SUBSTITUTION, ON A COPY. The pointer is shared with the interpretation
	// receipt and the family outcome; rewriting it in place would change the
	// record of what the model actually proposed, which must stay true even
	// where the server overrides it.
	composed := *in.Fresh
	if composed.SubjectExpression.Kind == SubjectExpressionGroupedMembers && composed.SubjectExpression.Grouped != nil {
		grouped := *composed.SubjectExpression.Grouped
		grouped.GroupKind = in.CarriedGroupKind
		composed.SubjectExpression.Grouped = &grouped
	}

	// VALIDATE THE COMPOSITION, not the input. This is the whole point of the
	// file: what follows is decided about the object consumers receive.
	result := ValidateFrame(composed, in.ModelObligations, in.EmittedShape)
	gate := DecideFrameGate(result, true)
	if result.Outcome != FrameValidationOutcomeValid {
		return AcceptedContext{
			Gate: gate, Outcome: CompositionInvalid,
			FailedInvariant: string(result.Failure.Invariant),
		}
	}

	// The accepted group axis is read back OFF THE VALIDATED FRAME, never
	// assumed to be the value that was substituted -- normalization runs
	// between A1 and A2 and is entitled to change it.
	acceptedGroup, _ := result.Frame.SubjectExpression.GroupKind()
	validated := result.Frame
	return AcceptedContext{
		Frame: &validated, Gate: gate,
		Outcome: CompositionAccepted, GroupKind: acceptedGroup,
	}
}

// freshAcceptedContext is the accepted context for a turn with no continuation:
// the fresh frame, its own gate, and the group axis read through the SAME
// accessor every other path uses.
//
// It exists so that "no continuation" is not a special case with its own way of
// answering the group-axis question. Every turn produces an AcceptedContext;
// only the outcome differs.
func freshAcceptedContext(fresh *QuestionFrame, freshGate FrameGate, sampleGroupKind SubjectKind) AcceptedContext {
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
