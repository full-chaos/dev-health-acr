package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
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
	// FrameRepairBoundSubjectRetargeted: the candidate introduced a
	// retrieval pointer the proposal did not carry. A repair may re-type
	// the question's structure; it may never point it at a different
	// subject.
	FrameRepairBoundSubjectRetargeted FrameRepairBoundViolation = "subject_retargeted"
	// FrameRepairBoundSubjectPointerDropped: the candidate DELETED a
	// retrieval pointer the proposal carried. Distinct from retargeting
	// because it is the more dangerous direction -- a repair that empties
	// the subject entirely leaves a structurally valid frame pointing at
	// nothing, and a subset check alone accepts it.
	FrameRepairBoundSubjectPointerDropped FrameRepairBoundViolation = "subject_pointer_dropped"
	// FrameRepairBoundOperandRewritten: the candidate altered an operand
	// that was already well-formed. Distinct from retargeting because the
	// pointer text can be preserved while the operand's TOPOLOGY changes
	// -- "team a" as a named subject and "team a" as a scope anchor carry
	// the same string and ask different questions.
	FrameRepairBoundOperandRewritten FrameRepairBoundViolation = "operand_rewritten"
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
	constrains := invariantConstrains(failure.Invariant)

	// KIND. Only an invariant whose CONDITION is about the discriminator
	// licenses moving it -- I1 (the discriminator is itself inconsistent),
	// I7 and I9 (whose conditions are literally about which Kind a goal
	// requires).
	kindMoved := repaired.SubjectExpression.Kind != proposed.SubjectExpression.Kind
	if kindMoved && !constrains[FrameFieldSubjectExpressionKind] {
		return FrameRepairBoundKindChanged
	}

	// RETRIEVAL POINTERS -- terms and anchor terms, wherever they live.
	//
	// ONE RULE, and it replaces three per-instance patches that each
	// closed one hole and left the next:
	//
	//   a pointer may NEVER be removed, and may be ADDED only when the
	//   failed invariant constrains a field that carries pointers.
	//
	// Every instance review found falls out of it. Introducing
	// "platform team" on an I9 failure: I9 constrains neither terms nor
	// anchor terms, so the addition is refused. DELETING every pointer on
	// an I9 failure and returning a subject-less grouped set: removal is
	// refused outright. Replacing `team a` with `platform` on an I2
	// failure: I2 does constrain the operands, so the ADDITION is legal --
	// and the REMOVAL of `team a` is not, which is what makes a
	// "replacement" refusable while a genuine second operand stays
	// reachable. The legitimate I9 redistribution (a named subject's term
	// becoming a scoped set's anchor) moves a pointer between fields
	// without adding or removing one, so it passes untouched.
	proposedPointers := retrievalPointers(proposed.SubjectExpression)
	repairedPointers := retrievalPointers(repaired.SubjectExpression)
	for pointer := range proposedPointers {
		if !repairedPointers[pointer] {
			return FrameRepairBoundSubjectPointerDropped
		}
	}
	if !constrains[FrameFieldSubjectTerms] && !constrains[FrameFieldAnchorTerms] && !constrains[FrameFieldOperands] {
		for pointer := range repairedPointers {
			if !proposedPointers[pointer] {
				return FrameRepairBoundSubjectRetargeted
			}
		}
	}

	// KIND-VALUED VARIANT FIELDS. Each needs its own permission, EXCEPT on
	// a legitimate Kind move -- the variant being moved TO demands its own
	// member or group kind, and the old variant had nowhere to carry one.
	// That exception is scoped to the kind-valued fields alone; the
	// pointer rule above still applies across the move, which is what
	// stops "the Kind changed" from excusing the whole payload.
	if !kindMoved {
		if memberKindOf(repaired.SubjectExpression) != memberKindOf(proposed.SubjectExpression) && !constrains[FrameFieldMemberKind] {
			return FrameRepairBoundUnnamedFieldChanged
		}
		if groupKindOf(repaired.SubjectExpression) != groupKindOf(proposed.SubjectExpression) && !constrains[FrameFieldGroupKind] {
			return FrameRepairBoundUnnamedFieldChanged
		}
		if expectedKindOf(repaired.SubjectExpression) != expectedKindOf(proposed.SubjectExpression) && !constrains[FrameFieldExpectedKind] {
			return FrameRepairBoundUnnamedFieldChanged
		}
		if operandCount(repaired.SubjectExpression) != operandCount(proposed.SubjectExpression) && !constrains[FrameFieldOperands] {
			return FrameRepairBoundUnnamedFieldChanged
		}
	}

	// OPERANDS, one level DOWN from the outer variant -- and the level
	// round 4 found the bound was still too coarse at.
	//
	// `FrameFieldOperands` was the same blunt token the outer
	// `subject_expression_variant` had been: I2's condition is
	// `len(Operands) >= 2`, a COUNT, but the token also covered every
	// operand's discriminator, member kind and expected kind. Review's
	// executed repro changed an EXISTING, well-formed operand from
	// `named_subject("team a")` into `children_of_scope(anchor "team a",
	// member project)` -- the term string preserved, so the pointer rule
	// let it through, while the question turned from "how is team A doing"
	// into "how are team A's projects doing".
	//
	// The rule, and it is the same principle the whole bound rests on
	// applied one level down: A REPAIR MAY CORRECT AN OPERAND THE SERVER
	// PROVED INCONSISTENT, AND MAY ADD NEW ONES; AN OPERAND THAT WAS
	// ALREADY WELL-FORMED IS FROZEN. I19's own predicate decides
	// "well-formed", shared rather than restated. Both legitimate repairs
	// stay reachable: I2's adds an operand and leaves the existing one
	// alone, and I19's corrects precisely the operand that was malformed.
	if frozen, ok := frozenOperandViolation(proposed.SubjectExpression, repaired.SubjectExpression); !ok {
		return frozen
	}

	// GOALS.
	proposedGoals := goalSet(proposed.Goals)
	repairedGoals := goalSet(repaired.Goals)
	named := invariantNamedGoals[failure.Invariant]
	for goal := range repairedGoals {
		if !proposedGoals[goal] && !constrains[FrameFieldGoals] {
			return FrameRepairBoundGoalAdded
		}
	}
	for goal := range proposedGoals {
		if repairedGoals[goal] {
			continue
		}
		// A removal needs BOTH: the invariant constrains the goal axis,
		// and it names this particular goal.
		if !constrains[FrameFieldGoals] || !named[goal] {
			return FrameRepairBoundGoalRemoved
		}
	}

	// EMPHASIS and DIMENSIONS. Narrowing is refused UNCONDITIONALLY --
	// there is no invariant whose repair requires discarding something the
	// user asked for -- and a widening still needs the axis constrained.
	if narrowed(emphasisSet(proposed.Emphasis), emphasisSet(repaired.Emphasis)) {
		return FrameRepairBoundEmphasisNarrowed
	}
	if narrowed(dimensionSet(proposed.Dimensions), dimensionSet(repaired.Dimensions)) {
		return FrameRepairBoundDimensionsNarrowed
	}
	if !constrains[FrameFieldEmphasis] && len(emphasisSet(repaired.Emphasis)) != len(emphasisSet(proposed.Emphasis)) {
		return FrameRepairBoundUnnamedFieldChanged
	}
	if !constrains[FrameFieldDimensions] && len(dimensionSet(repaired.Dimensions)) != len(dimensionSet(proposed.Dimensions)) {
		return FrameRepairBoundUnnamedFieldChanged
	}

	// TEMPORAL.
	if repaired.Temporal != proposed.Temporal && !constrains[FrameFieldTemporal] {
		return FrameRepairBoundUnnamedFieldChanged
	}

	return FrameRepairBoundNone
}

// frozenOperandViolation reports whether every WELL-FORMED operand of the
// proposal survives structurally in the repair. It returns ok=false plus
// the violation when one does not.
//
// Structural comparison is by the operand's own JSON, for the reason
// sameSubjectExpression uses it: a hand-written per-variant comparison
// grows a hole the day an operand gains a field, which is the drift class
// law L6 bans and precisely how this bound went wrong at the outer level.
func frozenOperandViolation(proposed, repaired SubjectExpression) (FrameRepairBoundViolation, bool) {
	if proposed.Explicit == nil {
		return FrameRepairBoundNone, true
	}
	survivors := map[string]bool{}
	if repaired.Explicit != nil {
		for _, operand := range repaired.Explicit.Operands {
			if encoded, err := json.Marshal(operand); err == nil {
				survivors[string(encoded)] = true
			}
		}
	}
	for _, operand := range proposed.Explicit.Operands {
		if !subjectOperandWellFormed(operand) {
			// The malformed operand is exactly what a repair is for.
			continue
		}
		encoded, err := json.Marshal(operand)
		if err != nil || !survivors[string(encoded)] {
			return FrameRepairBoundOperandRewritten, false
		}
	}
	return FrameRepairBoundNone, true
}

// memberKindOf, groupKindOf, expectedKindOf and operandCount read the
// kind-valued parts of whichever variant is set, so the bound can compare
// them without a switch per call site.
func memberKindOf(e SubjectExpression) SubjectKind {
	kind, _ := e.MemberKind()
	return kind
}

func groupKindOf(e SubjectExpression) SubjectKind {
	kind, _ := e.GroupKind()
	return kind
}

func expectedKindOf(e SubjectExpression) SubjectKind {
	if e.Named == nil || e.Named.ExpectedKind == nil {
		return ""
	}
	return *e.Named.ExpectedKind
}

func operandCount(e SubjectExpression) int {
	if e.Explicit == nil {
		return 0
	}
	return len(e.Explicit.Operands)
}

// invariantConstrains returns the declared Constrains list for an
// invariant, as a set. Sourced from the SAME spec table the validator and
// law L4's property test use -- a second table here would be the parallel
// authority law L6 bans, and it is how this bound went wrong three times.
func invariantConstrains(id FrameInvariant) map[FrameField]bool {
	for _, spec := range frameInvariantSpecs {
		if spec.ID != id {
			continue
		}
		out := make(map[FrameField]bool, len(spec.Constrains))
		for _, field := range spec.Constrains {
			out[field] = true
		}
		return out
	}
	return nil
}

// retrievalPointers collects every free-string pointer an expression
// carries -- a named subject's terms, a scoped set's anchor terms, and
// both for every operand -- as a set, so a Kind move can be checked for
// re-targeting without caring HOW the pointers were redistributed between
// variants.
func retrievalPointers(expression SubjectExpression) map[string]bool {
	pointers := map[string]bool{}
	add := func(terms []string) {
		for _, term := range terms {
			trimmed := strings.TrimSpace(term)
			if trimmed != "" {
				pointers[trimmed] = true
			}
		}
	}
	if expression.Named != nil {
		add(expression.Named.Terms)
	}
	if expression.Scoped != nil {
		add(expression.Scoped.AnchorTerms)
	}
	if expression.Explicit != nil {
		for _, operand := range expression.Explicit.Operands {
			if operand.Named != nil {
				add(operand.Named.Terms)
			}
			if operand.Scoped != nil {
				add(operand.Scoped.AnchorTerms)
			}
		}
	}
	return pointers
}

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
	// Version is SERVER-DERIVED and is stamped UNCONDITIONALLY, never
	// merely defaulted when absent.
	//
	// Preserving a non-empty proposed version let a value the server did
	// not author survive into the receipt and into the `frame_version` log
	// field -- found by adversarial review, which put
	// "zzz-confidential-frame-version" through validation, the event and
	// the real slog line untouched. Two things wrong at once: free text
	// reached a closed-vocabulary telemetry field, and a model or a
	// repairer could FALSIFY which derivation table produced a frame,
	// which is the one claim the version exists to make.
	frame.Version = QuestionFrameVersion

	// Goals are canonicalized here for the same reason Emphasis and
	// Dimensions are: they are documented as a SET, normalized into
	// vocabulary order, and a frame built directly rather than through
	// SanitizeInvestigationGoals kept duplicates and emission order.
	// `[rank_or_survey, assess_state, rank_or_survey]` validated as `valid`
	// and was persisted and logged verbatim, so two semantically identical
	// frames produced different representations. That matters beyond
	// tidiness: the family derivation is specified as a PURE FUNCTION of
	// the frame, and a function whose input can differ by order is not one.
	frame.Goals = canonicalGoals(frame.Goals)
	return frame
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

// validEmphasisOnly filters AND canonicalizes: deduplicated, in
// vocabulary order. Filtering alone left `[positive, negative, positive]`
// validating as a set that is not one -- the same defect the goal axis
// had, one field over, which is why all three set-valued axes are
// canonicalized here rather than each where it happened to be noticed.
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
