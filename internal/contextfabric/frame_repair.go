package contextfabric

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4452 stage 2 (S7b-i), design §13.6: bounded repair-on-invalid.
//
// SHADOW ONLY -- see frame_vocab.go's package-level note.
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
	spec, ok := frameInvariantSpec(failure.Invariant)
	if !ok {
		// An unknown invariant constrains nothing, so every change is out
		// of bounds. Refusing is the safe direction and keeps the bound
		// total over a vocabulary that may grow.
		spec = FrameInvariantSpec{}
	}

	// THE SPECIFIC RULES RUN FIRST, and the ordering is a diagnosis
	// decision rather than a logical one: every rule below is a special
	// case of "a repair may change only what the invariant constrains",
	// but each names a sharper failure. Reporting `unnamed_field_changed`
	// for a dropped subject pointer would be true and useless.

	changed := changedFramePaths(proposed, repaired)
	kindMoved := changed["subject_expression.kind"]
	kindConstrained := FrameInvariantConstrainsPath(spec, "subject_expression.kind")

	// RULE 0 -- an unlicensed discriminator move is reported FIRST, ahead
	// of its own consequences. Re-typing the subject necessarily disturbs
	// pointers and payload, so without this the operator would be told
	// "a pointer appeared" about a repair that actually changed the
	// question's whole topology.
	if kindMoved && !kindConstrained {
		return FrameRepairBoundKindChanged
	}

	// RULE A -- RETRIEVAL POINTERS: never removed; added only when the
	// failed invariant constrains a pointer-carrying path.
	//
	// Global over every pointer path in the tree rather than per path,
	// because a legitimate Kind move REDISTRIBUTES pointers between paths
	// (a named subject's terms become a scoped set's anchor) without
	// adding or removing one.
	proposedPointers := framePointerSet(proposed)
	repairedPointers := framePointerSet(repaired)
	for pointer := range proposedPointers {
		if !repairedPointers[pointer] {
			return FrameRepairBoundSubjectPointerDropped
		}
	}
	if !constrainsAnyPointerPath(spec) {
		for pointer := range repairedPointers {
			if !proposedPointers[pointer] {
				return FrameRepairBoundSubjectRetargeted
			}
		}
	}

	// RULE B -- any SUB-STRUCTURE that was already well-formed under its
	// own invariant is FROZEN.
	//
	// Quantified over every slice-of-struct path the reflection finds, so
	// a second nested structure is covered without being noticed. An
	// unregistered structure defaults to frozen: the safe direction, and
	// it forces a decision about what "well-formed" means there rather
	// than letting the new level inherit whichever hand-written rule sat
	// nearest.
	if violation, clean := frozenStructureViolation(proposed, repaired, spec); !clean {
		return violation
	}

	// RULE C -- NARROWING is refused unconditionally on the set-valued
	// axes, even where the invariant constrains them: no invariant's
	// repair requires discarding something the user asked for, and
	// narrowing is the failure direction the whole bound exists to
	// prevent. Goal removal is the one exception, and it is narrower
	// still: permitted only when the invariant NAMES that goal.
	if narrowed(emphasisSet(proposed.Emphasis), emphasisSet(repaired.Emphasis)) {
		return FrameRepairBoundEmphasisNarrowed
	}
	if narrowed(dimensionSet(proposed.Dimensions), dimensionSet(repaired.Dimensions)) {
		return FrameRepairBoundDimensionsNarrowed
	}
	named := invariantNamedGoals[failure.Invariant]
	for goal := range goalSet(proposed.Goals) {
		if goalSet(repaired.Goals)[goal] {
			continue
		}
		if !named[goal] {
			return FrameRepairBoundGoalRemoved
		}
	}

	// RULE D -- the GENERAL rule, and the one the others are special
	// cases of: a repair may change ONLY paths the failed invariant
	// constrains.
	//
	// Stated ONCE, over the whole DERIVED path tree, which is the point.
	// Four review rounds found the same defect at four depths because the
	// rule was written per level and each level had to be hand-listed.
	// Here depth is not an axis anyone enumerates: `changedFramePaths`
	// walks the type tree by reflection, so a new field or a new level of
	// nesting is covered the moment it exists.
	for path := range changed {
		if FrameInvariantConstrainsPath(spec, path) {
			continue
		}
		if derivedFramePath(path) {
			// Obligations and the version are SERVER-DERIVED; they are
			// recomputed after every repair rather than carried from the
			// candidate, so a difference here is the server's own work,
			// not the repairer's.
			continue
		}
		// THE ONE EXCEPTION, and it is scoped to the variant subtree: a
		// permitted discriminator move necessarily rewrites the variant it
		// discriminates -- you cannot switch to `children_of_scope`
		// without setting a scoped payload the old variant had nowhere to
		// carry. Without this, every legal I7/I9 repair would be
		// unreachable, which is the too-tight failure mode round 2
		// rejected. Rules A and B still applied ACROSS the move above,
		// which is what stops "the Kind changed" from excusing the payload.
		if kindMoved && kindConstrained && strings.HasPrefix(string(path), "subject_expression.") {
			continue
		}
		return violationForPath(path)
	}

	return FrameRepairBoundNone
}

// violationForPath maps a changed-but-unconstrained path onto the closed
// violation vocabulary, so telemetry keeps naming WHICH kind of overreach
// happened rather than collapsing to one code.
func violationForPath(path FramePath) FrameRepairBoundViolation {
	switch {
	case path == "subject_expression.kind":
		return FrameRepairBoundKindChanged
	case path == "goals":
		return FrameRepairBoundGoalAdded
	case strings.HasSuffix(string(path), framePathElementMarker) ||
		strings.Contains(string(path), framePathElementMarker+"."):
		return FrameRepairBoundOperandRewritten
	default:
		return FrameRepairBoundUnnamedFieldChanged
	}
}

// derivedFramePath reports whether a path holds a SERVER-DERIVED value,
// which the bound must ignore: the server recomputes obligations and
// stamps the version after every repair, so a difference there is its own
// work rather than the repairer's.
func derivedFramePath(path FramePath) bool {
	switch path {
	case "obligations", "widened_obligations", "version":
		return true
	}
	return false
}

// constrainsAnyPointerPath reports whether the invariant constrains any
// path that carries retrieval pointers.
// constrainsAnyPointerPath reports whether the invariant licenses NEW
// pointers appearing.
//
// It asks CONTAINMENT rather than constraint, which is the one place the
// two relations must differ: an invariant that constrains the operand LIST
// may add an operand, and an added operand arrives carrying its own terms.
// Asking `PathConstrainedBy` here would refuse every legitimate
// add-an-operand repair, which is the too-tight failure mode this bound
// has already hit once. Editing an EXISTING element is still refused --
// by the frozen-structure rule, which is a different question.
func constrainsAnyPointerPath(spec FrameInvariantSpec) bool {
	for path := range pointerCarryingPaths() {
		for _, constrained := range spec.Constrains {
			if PathContains(constrained, path) {
				return true
			}
		}
	}
	return false
}

// framePointerSet collects every retrieval pointer the frame carries, at
// any depth, derived from the pointer-carrying paths rather than from a
// per-variant walk.
func framePointerSet(frame QuestionFrame) map[string]bool {
	out := map[string]bool{}
	for _, term := range frame.SubjectExpression.SubjectTerms() {
		if trimmed := strings.TrimSpace(term); trimmed != "" {
			out[trimmed] = true
		}
	}
	if frame.SubjectExpression.Scoped != nil {
		for _, term := range frame.SubjectExpression.Scoped.AnchorTerms {
			if trimmed := strings.TrimSpace(term); trimmed != "" {
				out[trimmed] = true
			}
		}
	}
	if frame.SubjectExpression.Explicit != nil {
		for _, operand := range frame.SubjectExpression.Explicit.Operands {
			for _, term := range operand.Terms() {
				if trimmed := strings.TrimSpace(term); trimmed != "" {
					out[trimmed] = true
				}
			}
		}
	}
	return out
}

// frozenStructureViolation applies rule 3 over every slice-of-struct path
// the type tree declares.
func frozenStructureViolation(proposed, repaired QuestionFrame, spec FrameInvariantSpec) (FrameRepairBoundViolation, bool) {
	for _, path := range FrameStructurePaths() {
		if FrameInvariantConstrainsPath(spec, path) {
			// The invariant constrains the ELEMENTS themselves (I19), so
			// correcting one is exactly what this repair is for.
			continue
		}
		predicate, registered := wellFormedPredicates[path]
		before := structureElements(proposed, path)
		after := structureElements(repaired, path)
		survivors := map[string]bool{}
		for _, encoded := range after {
			survivors[encoded.key] = true
		}
		for _, element := range before {
			// An element that was NOT well-formed is exactly what a
			// repair is for. An UNREGISTERED structure is treated as
			// always-well-formed, i.e. frozen -- the safe default.
			if registered && !predicate(element.value) {
				continue
			}
			if !survivors[element.key] {
				return FrameRepairBoundOperandRewritten, false
			}
		}
	}
	return FrameRepairBoundNone, true
}

type structureElement struct {
	key   string
	value reflect.Value
}

// structureElements returns the elements at a slice-of-struct path, each
// with a canonical key for identity comparison.
func structureElements(frame QuestionFrame, path FramePath) []structureElement {
	segments := strings.Split(strings.TrimSuffix(string(path), framePathElementMarker), ".")
	v := reflect.ValueOf(frame)
	for _, segment := range segments {
		v = fieldByJSONName(v, segment)
		if !v.IsValid() {
			return nil
		}
	}
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Slice {
		return nil
	}
	out := make([]structureElement, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		element := v.Index(i)
		encoded, err := json.Marshal(element.Interface())
		if err != nil {
			continue
		}
		out = append(out, structureElement{key: string(encoded), value: element})
	}
	return out
}

func fieldByJSONName(v reflect.Value, name string) reflect.Value {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if jsonFieldName(t.Field(i)) == name {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

func frameInvariantSpec(id FrameInvariant) (FrameInvariantSpec, bool) {
	for _, spec := range frameInvariantSpecs {
		if spec.ID == id {
			return spec, true
		}
	}
	return FrameInvariantSpec{}, false
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

// NormalizeFrame, canonicalGoals, validEmphasisOnly and validDimensionsOnly
// moved to frame_validation.go when the bound was carved out (this branch
// predates that split): the shipped surface owns normalization, and the
// repair path here calls the shared copies rather than shadowing them.
