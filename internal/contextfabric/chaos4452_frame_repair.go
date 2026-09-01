package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4452 stage 2 (S7b-i), design §13.6: bounded repair-on-invalid.
//
// SHADOW ONLY -- see chaos4452_frame_vocab.go's package-level note.
//
// CONSENSUS IS NOT REPLACED, and this is a decided ruling (D3) rather than
// an implementation preference. The feedback preferred one bounded repair
// pass over "routinely sampling several incomplete objects and voting";
// as a REPLACEMENT that relitigates a decided ruling and discards a
// shipped, measured mechanism on argument alone. It is adopted as an
// ADDITION, because the two handle DIFFERENT failure classes:
//
//   - CONSENSUS handles stochastic instability -- the same question
//     sampling to different objects. Unchanged: N distinct
//     deterministically derived seeds, strict majority, the winning sample
//     taken WHOLE, no field-wise mixing.
//   - REPAIR handles a frame that is STABLE but violates a validation
//     invariant. That class has no mechanism today, and the server-side
//     invariants are what finally give repair a well-defined target.
//
// Order of operations: consensus first; the winner is selected WHOLE; the
// winner's frame is validated; only a phase-A failure triggers repair.
//
// HONEST NOTE ON TODAY'S NUMBERS, carried from §13.6 rather than left for
// a reader to discover: S2 measured stability 12/12 at N=1 and concluded
// "the ensemble earns nothing at this stability, so N=1 ships as the
// default". At N=1 there is no vote, so in practice REPAIR IS THE PRIMARY
// INVALID-HANDLER and consensus is retained-but-dormant, ready for the
// instability it was measured against.

// FrameRepairRequest is what a repairer is given: the frame that failed,
// and the invariant it failed. Nothing else -- in particular, no
// permission to reconsider the question.
type FrameRepairRequest struct {
	// Proposed is the frame as validated, unmodified.
	Proposed QuestionFrame
	// Failure is the FIRST failed invariant, which is also the only thing
	// the repair is permitted to address.
	Failure FrameValidationFailure
	// EmittedShape is the sampler's own Shape, carried so a re-validation
	// of phase A2 can evaluate I18 against the same input.
	EmittedShape InvestigationShape
}

// FrameRepairer is the port a bounded repair attempt goes through.
//
// It is a PORT rather than a direct model call for the reason every other
// model boundary in this package is: the repair BOUND is server-side
// arithmetic that must be testable without a model, and a genkit call
// baked into the bound would make the oracles depend on a live provider.
// The production implementation lives in genkitruntime; the oracles drive
// a stub.
type FrameRepairer interface {
	// RepairFrame returns ONE repaired candidate. It is called at most
	// once per investigation. An error is not a failure of the frame --
	// it is a failure to repair, and the caller refuses rather than
	// retrying.
	RepairFrame(ctx context.Context, principal storage.Principal, request FrameRepairRequest) (QuestionFrame, error)
}

// FrameRepairResult is the whole outcome of the validate-repair-revalidate
// sequence.
type FrameRepairResult struct {
	// Frame is the frame to use. On a refusal it is the zero frame: the
	// design's rule is "still invalid => unclassified, refuse to guess",
	// and returning a partially-repaired frame would be the guess.
	Frame QuestionFrame
	// Outcome is the closed telemetry vocabulary value.
	Outcome FrameValidationOutcome
	// Failure is the failure that triggered the repair, on any path
	// except `valid`. Retained on a refusal so telemetry records WHICH
	// invariant could not be repaired, not merely that one could not.
	Failure FrameValidationFailure
	// RepairAttempted records whether the repairer was called at all.
	RepairAttempted bool
	// RepairLatency is the wall time of the repairer call. Behaviour
	// change B4 is "a malformed frame can spend one extra model call",
	// and its gate is "inside the reserved deadline; measured repair rate
	// + latency" -- an unmeasured repair is an unbounded one.
	RepairLatency time.Duration
	// ViolatedBound names why a repaired candidate was rejected, when it
	// was. Closed vocabulary.
	ViolatedBound FrameRepairBoundViolation
}

// FrameRepairBoundViolation is the closed vocabulary of ways a repaired
// candidate can breach the bound.
type FrameRepairBoundViolation string

const (
	// FrameRepairBoundNone: no breach.
	FrameRepairBoundNone FrameRepairBoundViolation = ""
	// FrameRepairBoundKindChanged: the candidate changed
	// SubjectExpression.Kind and the violated invariant does not name it.
	FrameRepairBoundKindChanged FrameRepairBoundViolation = "kind_changed_unnamed"
	// FrameRepairBoundGoalAdded: the candidate added a goal and the
	// violated invariant does not name the goal axis.
	FrameRepairBoundGoalAdded FrameRepairBoundViolation = "goal_added_unnamed"
	// FrameRepairBoundGoalRemoved: the candidate removed a goal the
	// violated invariant does not name. Narrowing is the failure mode,
	// so this is the strictest of the three.
	FrameRepairBoundGoalRemoved FrameRepairBoundViolation = "goal_removed_unnamed"
	// FrameRepairBoundEmphasisNarrowed: the candidate discarded emphasis
	// the user asked for.
	FrameRepairBoundEmphasisNarrowed FrameRepairBoundViolation = "emphasis_narrowed"
	// FrameRepairBoundDimensionsNarrowed: the candidate removed a
	// dimension. Dimensions are ADDITIVE-ONLY.
	FrameRepairBoundDimensionsNarrowed FrameRepairBoundViolation = "dimensions_narrowed"
	// FrameRepairBoundUnnamedFieldChanged: the candidate changed a field
	// the violated invariant does not read at all.
	FrameRepairBoundUnnamedFieldChanged FrameRepairBoundViolation = "unnamed_field_changed"
)

// invariantNamedGoals declares, per invariant, WHICH goals that
// invariant's condition names. Removing a goal is permitted only for a
// goal in this set.
//
// "The invariant names it" is not a figure of speech: I7's condition is
// literally "Goals ∋ compare ⇒ Kind == explicit_set", so I7 names
// `compare`. I9 names `count_or_aggregate`. I8 names the trend pair. I14
// and I15 read the goal axis without naming a member, so they permit
// ADDING (widening, always safe) and permit removing NOTHING.
//
// THE ASYMMETRY IS THE POINT (§13.6 rule 2): "Adding a goal is permitted
// where the violated invariant names the goal axis; REMOVING a goal is
// permitted only when the invariant names it. This asymmetry is
// deliberate: widening is safe, narrowing is the failure mode."
var invariantNamedGoals = map[FrameInvariant]map[InvestigationGoal]bool{
	FrameInvariantI7:  {GoalCompare: true},
	FrameInvariantI8:  {GoalDescribeTrend: true, GoalExplainChange: true},
	FrameInvariantI9:  {GoalCountOrAggregate: true},
	FrameInvariantI17: {GoalCountOrAggregate: true},
}

// invariantReads returns the declared Reads list for an invariant.
// Sourced from the SAME spec table the validator and law L4's property
// test use, so "the fields the failed invariant names" has exactly one
// definition in this package. A second table here would be a parallel
// authority, which law L6 bans.
func invariantReads(id FrameInvariant) map[FrameField]bool {
	for _, spec := range frameInvariantSpecs {
		if spec.ID != id {
			continue
		}
		reads := make(map[FrameField]bool, len(spec.Reads))
		for _, field := range spec.Reads {
			reads[field] = true
		}
		return reads
	}
	return nil
}

// CheckFrameRepairBound reports whether a repaired candidate stays inside
// §13.6 rule 2's bound, given the invariant that failed.
//
// THE BOUND WAS TOO TIGHT ONCE AND THAT IS WHY IT READS THE WAY IT DOES.
// The design's first revision pinned Goals and Kind ABSOLUTELY, and round
// 2 showed that made declared repair targets STRUCTURALLY UNREACHABLE:
// every I7 failure (compare with a non-explicit_set kind) and every I9
// failure (a count over an illegal kind) can only be corrected by changing
// one of the pinned fields, so those frames always refused even when the
// misclassification was obviously repairable. The rule that restores
// reachability without losing the safety property: the repair may change
// Kind or drop a goal ONLY when the violated invariant NAMES that field,
// and the repaired frame must satisfy the invariant that failed PLUS
// every other invariant.
//
// A frame that fails NO invariant naming Kind still cannot have its Kind
// changed -- which preserves the original intent: repair may not talk
// itself into a DIFFERENT QUESTION, it may only correct the field the
// server has already proven inconsistent.
// THE BOUND IS CLOSED OVER EVERY AXIS, NOT ONLY GOALS AND KIND. §13.6 rule
// 2's first sentence is the general rule and the Goals/Kind clause is its
// most-cited instance: "The repair call may only supply or correct the
// FIELDS THE FAILED INVARIANT NAMES." A bound that pinned only Goals and
// Kind would leave the variant's own contents -- a named subject's terms, a
// scoped set's anchor -- rewritable on any failure whose invariant does not
// read them, and rewriting the anchor terms of a question is talking
// yourself into a different question just as surely as changing the Kind is.
// So every axis is compared, and an axis the invariant does not name may
// not move at all.
func CheckFrameRepairBound(proposed, repaired QuestionFrame, failure FrameValidationFailure) FrameRepairBoundViolation {
	reads := invariantReads(failure.Invariant)
	namesGoals := reads[FrameFieldGoals]

	// KIND. Only an invariant that reads the DISCRIMINATOR licenses a Kind
	// change -- reading the VARIANT does not.
	//
	// The distinction is load-bearing and an earlier revision of this
	// function got it wrong by treating the two as one. I6's condition is
	// about `GroupKind` and `MemberKind`, fields INSIDE the grouped
	// variant; it says nothing about which variant the frame is. Letting
	// an I6 failure license a wholesale Kind change would mean "you
	// grouped a kind by itself" could be repaired into an entirely
	// different topology -- the reinterpretation the bound exists to
	// forbid. The invariants that genuinely name the discriminator are I1
	// (the discriminator itself is inconsistent), I7 and I9 (whose
	// conditions are literally about which Kind a goal requires) and I17.
	if repaired.SubjectExpression.Kind != proposed.SubjectExpression.Kind && !reads[FrameFieldSubjectExpressionKind] {
		return FrameRepairBoundKindChanged
	}

	// VARIANT CONTENTS. Compared structurally, so a rewritten term, anchor
	// or member kind is caught rather than only a changed discriminator.
	//
	// Checked ONLY WHEN THE KIND DID NOT MOVE. A permitted Kind change
	// necessarily rewrites the variant -- you cannot switch the
	// discriminator without setting a different pointer -- so applying the
	// variant check to a legal Kind repair would make I7 and I9 repairs
	// unreachable again, which is precisely the too-tight bound round 2
	// (P2-1) rejected. When the Kind is unchanged there is no such excuse:
	// a repair for an invariant that does not read the variant may not
	// touch its fields.
	kindMoved := repaired.SubjectExpression.Kind != proposed.SubjectExpression.Kind
	if !kindMoved && !reads[FrameFieldSubjectExpressionVariant] &&
		!sameSubjectExpression(proposed.SubjectExpression, repaired.SubjectExpression) {
		return FrameRepairBoundUnnamedFieldChanged
	}

	// GOALS.
	proposedGoals := goalSet(proposed.Goals)
	repairedGoals := goalSet(repaired.Goals)
	named := invariantNamedGoals[failure.Invariant]
	for goal := range repairedGoals {
		if !proposedGoals[goal] && !namesGoals {
			return FrameRepairBoundGoalAdded
		}
	}
	for goal := range proposedGoals {
		if repairedGoals[goal] {
			continue
		}
		// A removal needs BOTH: the invariant reads the goal axis, and
		// it names this particular goal.
		if !namesGoals || !named[goal] {
			return FrameRepairBoundGoalRemoved
		}
	}

	// EMPHASIS and DIMENSIONS. Narrowing is refused UNCONDITIONALLY --
	// even for an invariant that reads the axis -- because narrowing is
	// the failure mode the whole bound exists to prevent, and there is no
	// invariant whose repair requires discarding something the user asked
	// for.
	//
	// R9's scenario is exactly this: a sampler emits Goals={assess_state}
	// with Emphasis=[negative,positive], I14 fails because no ranking
	// obligation derives, and under a drop-only repair the options were
	// "silently discard Emphasis" (narrowing the answer the 12:42 08-31
	// ruling says must be given) or "refuse" -- with WHICH one happening
	// depending on the sampler's goal pick, reintroducing exactly the
	// instability this design exists to remove. Because Goals is a SET the
	// repair instead ADDS rank_or_survey, a monotone widening under law L1
	// that satisfies I14 without discarding anything.
	if narrowed(emphasisSet(proposed.Emphasis), emphasisSet(repaired.Emphasis)) {
		return FrameRepairBoundEmphasisNarrowed
	}
	if narrowed(dimensionSet(proposed.Dimensions), dimensionSet(repaired.Dimensions)) {
		return FrameRepairBoundDimensionsNarrowed
	}
	// A WIDENING of either axis still needs the invariant to name it. A
	// repair that adds a dimension nobody asked about is not narrowing,
	// but it is still answering a question the user did not ask.
	if !reads[FrameFieldEmphasis] && len(emphasisSet(repaired.Emphasis)) != len(emphasisSet(proposed.Emphasis)) {
		return FrameRepairBoundUnnamedFieldChanged
	}
	if !reads[FrameFieldDimensions] && len(dimensionSet(repaired.Dimensions)) != len(dimensionSet(proposed.Dimensions)) {
		return FrameRepairBoundUnnamedFieldChanged
	}

	// TEMPORAL. Only I8 reads it.
	if repaired.Temporal != proposed.Temporal && !reads[FrameFieldTemporal] {
		return FrameRepairBoundUnnamedFieldChanged
	}

	return FrameRepairBoundNone
}

// sameSubjectExpression compares two expressions structurally, including
// every variant's own fields.
//
// Marshalled rather than hand-compared: a hand-written comparison has one
// branch per variant and grows a hole the day a variant gains a field,
// which is the same drift class law L6 bans at a larger scale. The types
// carry json tags precisely because the frame is persisted on the receipt,
// so the encoding already exists and is deterministic (Go marshals struct
// fields in declaration order, and every collection here is an ordered
// slice, never a map).
func sameSubjectExpression(a, b SubjectExpression) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	if leftErr != nil || rightErr != nil {
		// A marshalling failure must not read as "unchanged". Refusing
		// the repair is the conservative direction: the bound's job is to
		// prove a candidate is inside it, not to assume it.
		return false
	}
	return bytes.Equal(left, right)
}

func goalSet(in []InvestigationGoal) map[InvestigationGoal]bool {
	out := make(map[InvestigationGoal]bool, len(in))
	for _, member := range in {
		out[member] = true
	}
	return out
}

func emphasisSet(in []AnswerEmphasis) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, member := range in {
		out[string(member)] = true
	}
	return out
}

func dimensionSet(in []HealthDimension) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, member := range in {
		out[string(member)] = true
	}
	return out
}

// narrowed reports whether after lost a member before had.
func narrowed(before, after map[string]bool) bool {
	for member := range before {
		if !after[member] {
			return true
		}
	}
	return false
}

// ValidateAndRepairFrame runs the whole §13.6 sequence: phase A1, derive,
// phase A2, and -- only on a phase-A failure -- EXACTLY ONE bounded repair
// attempt, re-validated by the same invariants unrelaxed.
//
// THE BOUND, restated as the code enforces it:
//
//  1. EXACTLY ONE repair attempt. Not k. A second attempt is a refusal,
//     and there is deliberately no loop here for a later edit to raise a
//     bound in.
//  2. Goals and Kind are PINNED except where the violated invariant names
//     the field -- CheckFrameRepairBound.
//  3. The repair OUTPUT is validated by the SAME invariants, UNRELAXED. No
//     "repaired" bypass path exists.
//  4. Still invalid => unclassified, refuse to guess.
//  5. The repair runs inside the caller's context, which carries the
//     request's single timeout (timeoutMiddleware) -- the reserved
//     synthesis deadline is non-negotiable and repair draws from the same
//     reservation.
//  6. Repair is NEVER invoked for a phase-B failure. This function only
//     ever evaluates phases A1 and A2, so that rule holds by construction
//     rather than by discipline.
//
// modelObligations is whatever obligation list the model emitted; it is
// admitted as a WIDENING and every member becomes advisory (§13.2.4).
func ValidateAndRepairFrame(
	ctx context.Context,
	principal storage.Principal,
	repairer FrameRepairer,
	proposed QuestionFrame,
	modelObligations []AnswerObligation,
	emittedShape InvestigationShape,
	clock func() time.Time,
) FrameRepairResult {
	if clock == nil {
		clock = time.Now
	}

	validate := func(frame QuestionFrame) (QuestionFrame, FrameValidationFailure, bool) {
		// A1 -- emitted fields only, BEFORE normalization.
		if failure, bad := ValidateFramePhaseA1(frame); bad {
			return frame, failure, false
		}
		// Normalize and derive. This is the ONLY point at which the
		// frame is written to, and it sits between A1 and A2 exactly as
		// law L4 requires.
		normalized := NormalizeFrame(frame)
		normalized = DeriveFrameObligations(normalized, modelObligations)
		// A2 -- derived values.
		if failure, bad := ValidateFramePhaseA2(normalized, emittedShape); bad {
			return normalized, failure, false
		}
		return normalized, FrameValidationFailure{}, true
	}

	frame, failure, ok := validate(proposed)
	if ok {
		return FrameRepairResult{Frame: frame, Outcome: FrameValidationOutcomeValid}
	}

	if repairer == nil {
		// No repairer configured is not a silent pass-through: the frame
		// is invalid and stays refused. Recording it as
		// refused_invalid with the failing invariant keeps the
		// denominator honest.
		return FrameRepairResult{Outcome: FrameValidationOutcomeRefusedInvalid, Failure: failure}
	}

	started := clock()
	candidate, err := repairer.RepairFrame(ctx, principal, FrameRepairRequest{
		Proposed:     proposed,
		Failure:      failure,
		EmittedShape: emittedShape,
	})
	latency := clock().Sub(started)
	if err != nil {
		return FrameRepairResult{
			Outcome:         FrameValidationOutcomeRefusedInvalid,
			Failure:         failure,
			RepairAttempted: true,
			RepairLatency:   latency,
		}
	}

	if violation := CheckFrameRepairBound(proposed, candidate, failure); violation != FrameRepairBoundNone {
		outcome := FrameValidationOutcomeRefusedInvalid
		if violation == FrameRepairBoundKindChanged {
			outcome = FrameValidationOutcomeRefusedKindChange
		}
		return FrameRepairResult{
			Outcome:         outcome,
			Failure:         failure,
			RepairAttempted: true,
			RepairLatency:   latency,
			ViolatedBound:   violation,
		}
	}

	repaired, repairedFailure, repairedOK := validate(candidate)
	if !repairedOK {
		// Rule 4: still invalid => refuse. The failure REPORTED is the
		// ORIGINAL one, because that is the invariant the repair was
		// asked to fix and failed to; reporting the second failure would
		// make a repair that broke something else look like a different
		// defect class.
		return FrameRepairResult{
			Outcome:         FrameValidationOutcomeRefusedInvalid,
			Failure:         failure,
			RepairAttempted: true,
			RepairLatency:   latency,
		}
	}
	_ = repairedFailure
	return FrameRepairResult{
		Frame:           repaired,
		Outcome:         FrameValidationOutcomeRepaired,
		Failure:         failure,
		RepairAttempted: true,
		RepairLatency:   latency,
	}
}

// NormalizeFrame applies the derivations §13.2.1's authorship table calls
// for, and NOTHING ELSE. It runs strictly between phase A1 and phase A2.
//
// It is a function rather than a few inline assignments because the A1/A2
// boundary has to be a NAMED point in the flow -- an inline default would
// put a write to the frame inside the validator, which is how the design
// got into the phase confusion round 2 caught.
//
// THREE THINGS HAPPEN HERE, all of them §13.2.1's authorship table read
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
//  2. "Emphasis / Dimensions: closed enum set" -- each is re-sanitized
//     here, dropping any member outside its vocabulary, which is exactly
//     what §13.2.1 says the server does with an unknown member ("DROPPED
//     from the set, never an error"). Same invisibility argument: an
//     unknown dimension misses table 3 AND misses dimensionDischarge's
//     obligation branch, so it would quietly fall through to the
//     fact-kind-constraint discharge and look decided.
//
//     Goals are NOT sanitized here -- they are a phase-A1 FAILURE under
//     I15 instead. The asymmetry is the design's: an empty goal set is a
//     failure rather than a default, and goals are the one axis whose
//     values reach a log field, so they get named rather than silently
//     repaired. See checkI15.
//
//  3. Version is stamped, so every frame that survives validation carries
//     the derivation-table version that produced it -- which is what lets
//     a persisted frame be replayed against the right table.
func NormalizeFrame(frame QuestionFrame) QuestionFrame {
	if !ValidTemporalIntent(frame.Temporal) {
		frame.Temporal = TemporalIntentCurrent
	}
	frame.Emphasis = validEmphasisOnly(frame.Emphasis)
	frame.Dimensions = validDimensionsOnly(frame.Dimensions)
	if frame.Version == "" {
		frame.Version = QuestionFrameVersion
	}
	return frame
}

func validEmphasisOnly(in []AnswerEmphasis) []AnswerEmphasis {
	if len(in) == 0 {
		return in
	}
	out := make([]AnswerEmphasis, 0, len(in))
	for _, member := range in {
		if ValidAnswerEmphasis(member) {
			out = append(out, member)
		}
	}
	return out
}

func validDimensionsOnly(in []HealthDimension) []HealthDimension {
	if len(in) == 0 {
		return in
	}
	out := make([]HealthDimension, 0, len(in))
	for _, member := range in {
		if ValidHealthDimension(member) {
			out = append(out, member)
		}
	}
	return out
}
