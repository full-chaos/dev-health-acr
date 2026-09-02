package contextfabric

// CHAOS-4452 stage 2 (S7b-i), design §13.5.2 and §13.6: frame validation
// and normalization.
//
// SHADOW ONLY -- see frame_vocab.go's package-level note.
//
// REPAIR IS NOT IN THIS SLICE, and its absence is a decision rather than an
// omission. §13.6 admits ONE bounded repair attempt on a frame that fails
// validation, and the mechanism that makes it safe is the BOUND -- the rule
// that a repair may only correct what the server has already proven
// inconsistent, and may not talk itself into a different question. That
// bound is carved into its own change, because five adversarial rounds
// found defects in it and none anywhere else in this file's surface.
//
// So a frame that fails validation here is REFUSED. That is the honest
// outcome and the design's own fallback -- §13.6 rule 4, "still invalid =>
// unclassified, refuse to guess" -- reached immediately rather than after
// an attempt. Nothing degrades: without a repair path the failure surfaces
// as `refused_invalid` with the failing invariant named, which is exactly
// what a shadow slice needs in order to measure how often frames are
// malformed and why.
//
// CONSENSUS IS UNCHANGED AND IS NOT A REPLACEMENT FOR REPAIR. The two
// handle different failure classes (§13.6): consensus handles stochastic
// instability -- the same question sampling to different objects -- while
// repair handles a frame that is STABLE but violates an invariant. This
// slice ships the second class as a REFUSAL and measures its rate; the
// repair that would rescue it lands with its bound.

// FrameValidationResult is the outcome of validating one proposed frame.
type FrameValidationResult struct {
	// Frame is the validated, normalized frame. On a refusal it is the
	// ZERO frame: §13.6's rule is "refuse to guess", and returning a
	// partially-validated frame would be the guess -- a downstream stage
	// handed one could not tell it from a frame that passed.
	Frame QuestionFrame
	// Outcome is the closed telemetry vocabulary value.
	Outcome FrameValidationOutcome
	// Failure is the FIRST failed invariant, in table order. Retained on
	// a refusal so telemetry records WHICH invariant refused the frame,
	// not merely that one did.
	Failure FrameValidationFailure
}

// ValidateFrame runs the §13.1 sequence over a proposed frame: phase A1
// over the model-emitted fields, then normalization, then phase A2 over
// the derived values.
//
// THE ORDER IS LAW L4 AND IS NOT NEGOTIABLE. A1 reads only what the model
// emitted; normalization is the single point at which the frame is
// written to; A2 reads derived values. The frame becomes immutable at the
// END of A2, not the end of A1 -- the design's own flow is
//
//	consensus -> winning sample WHOLE -> A1 -> normalize/derive -> A2
//	  -> FRAME IMMUTABLE -> resolution (phase B) -> fact read (phase C)
//
// and putting the derived-value invariants in a single phase is what round
// 2 (P1-6) caught: as written they were either evaluated before their
// inputs existed, or obligations were added after validation to a frame
// the design calls immutable.
//
// PHASE B AND C ARE NEVER EVALUATED HERE. A resolution or evidence failure
// is not a malformed frame; it produces a clarification, a narrowed
// answer, or a disclosed requirement outcome. Treating one as a validation
// failure would refuse questions that are perfectly well-formed and merely
// unanswerable on this org's data -- which North Star check 4 forbids and
// check 13 would trip over constantly.
//
// modelObligations is whatever obligation list the model emitted; it is
// admitted as a WIDENING and every member becomes advisory (§13.2.4).
func ValidateFrame(proposed QuestionFrame, modelObligations []AnswerObligation, emittedShape InvestigationShape) FrameValidationResult {
	// A1 -- emitted fields only, BEFORE normalization.
	if failure, bad := ValidateFramePhaseA1(proposed); bad {
		return FrameValidationResult{Outcome: FrameValidationOutcomeRefusedInvalid, Failure: failure}
	}
	// Normalize and derive. The ONLY point at which the frame is written
	// to, sitting between A1 and A2 exactly as law L4 requires.
	normalized := NormalizeFrame(proposed)
	normalized = DeriveFrameObligations(normalized, modelObligations)
	// A2 -- derived values.
	if failure, bad := ValidateFramePhaseA2(normalized, emittedShape); bad {
		return FrameValidationResult{Outcome: FrameValidationOutcomeRefusedInvalid, Failure: failure}
	}
	return FrameValidationResult{Frame: normalized, Outcome: FrameValidationOutcomeValid}
}

// NormalizeFrame applies the derivations §13.2.1's authorship table calls
// for, and NOTHING ELSE. It runs strictly between phase A1 and phase A2.
//
// It is a function rather than a few inline assignments because the A1/A2
// boundary has to be a NAMED point in the flow -- an inline default would
// put a write to the frame inside the validator, which is how the design
// got into the phase confusion round 2 caught.
//
// FOUR THINGS HAPPEN HERE, all of them §13.2.1's authorship table read
// literally:
//
//  1. "Temporal: unset derives `current`", and an out-of-vocabulary
//     Temporal derives it too. A Temporal outside the vocabulary is not an
//     error anywhere in the design, but left in place it is INVISIBLE: it
//     misses table 2's map and misses temporalDischarge's map, so it
//     contributes no obligation and no discharge and I16 cannot see the
//     axis. Deriving `current` makes the axis mean something rather than
//     silently nothing.
//
//  2. Emphasis and Dimensions are re-sanitized AND CANONICALIZED --
//     unknown members dropped (exactly what §13.2.1 says the server does
//     with one), then deduplicated and put in vocabulary order. Filtering
//     alone left `[positive, negative, positive]` validating as a set that
//     is not one, and a duplicate dimension produced a DUPLICATE axis
//     discharge, so the set property is not cosmetic.
//
//  3. Goals are canonicalized the same way -- deduplicated, in vocabulary
//     order -- but NOT filtered: an out-of-vocabulary goal is a phase-A1
//     failure under I15, and silently dropping one here would take that
//     failure away from the invariant that names it. Canonicalization
//     matters beyond tidiness because the family derivation of the next
//     slice is specified as a PURE FUNCTION of the frame, and a function
//     whose input can differ by order is not one.
//
//  4. Version is stamped UNCONDITIONALLY, never merely defaulted when
//     absent. Preserving a non-empty proposed version let a value the
//     server did not author survive into the receipt and into the
//     `frame_version` log field -- free text in a closed telemetry field,
//     and a way for model output to FALSIFY which derivation table
//     produced a frame, which is the one claim the version exists to make.
func NormalizeFrame(frame QuestionFrame) QuestionFrame {
	if !ValidTemporalIntent(frame.Temporal) {
		frame.Temporal = TemporalIntentCurrent
	}
	frame.Emphasis = validEmphasisOnly(frame.Emphasis)
	frame.Dimensions = validDimensionsOnly(frame.Dimensions)
	frame.Version = QuestionFrameVersion
	frame.Goals = canonicalGoals(frame.Goals)
	return frame
}

// validEmphasisOnly filters AND canonicalizes: deduplicated, in vocabulary
// order.
func validEmphasisOnly(in []AnswerEmphasis) []AnswerEmphasis {
	if len(in) == 0 {
		return in
	}
	seen := make(map[AnswerEmphasis]bool, len(in))
	for _, member := range in {
		if ValidAnswerEmphasis(member) {
			seen[member] = true
		}
	}
	out := make([]AnswerEmphasis, 0, len(seen))
	for _, member := range answerEmphases {
		if seen[member] {
			out = append(out, member)
		}
	}
	return out
}

// validDimensionsOnly filters AND canonicalizes, in published order, for
// the reason validEmphasisOnly above gives. A duplicate dimension also
// produced a duplicate axis discharge, so the set property is not merely
// cosmetic here.
func validDimensionsOnly(in []HealthDimension) []HealthDimension {
	if len(in) == 0 {
		return in
	}
	seen := make(map[HealthDimension]bool, len(in))
	for _, member := range in {
		if ValidHealthDimension(member) {
			seen[member] = true
		}
	}
	published := HealthDimensionVocabulary()
	out := make([]HealthDimension, 0, len(seen))
	for _, member := range published {
		if seen[member] {
			out = append(out, member)
		}
	}
	return out
}

// canonicalGoals deduplicates and returns the goal set in vocabulary
// order. Membership is NOT filtered here -- an out-of-vocabulary goal is a
// phase-A1 failure under I15, and silently dropping one in normalization
// would take that failure away from the invariant that names it.
func canonicalGoals(in []InvestigationGoal) []InvestigationGoal {
	if len(in) == 0 {
		return in
	}
	seen := make(map[InvestigationGoal]bool, len(in))
	for _, goal := range in {
		seen[goal] = true
	}
	out := make([]InvestigationGoal, 0, len(seen))
	for _, member := range investigationGoals {
		if seen[member] {
			out = append(out, member)
			delete(seen, member)
		}
	}
	// Any member left is outside the vocabulary. It is APPENDED rather
	// than dropped, in its own stable order, so I15 still sees it and
	// fails by name.
	remaining := make([]InvestigationGoal, 0, len(seen))
	for _, goal := range in {
		if seen[goal] {
			remaining = append(remaining, goal)
			delete(seen, goal)
		}
	}
	return append(out, remaining...)
}
