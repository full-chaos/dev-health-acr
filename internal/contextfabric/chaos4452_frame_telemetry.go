package contextfabric

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4452 stage 2 (S7b-i), design §13.6's telemetry table:
// frame-validation telemetry.
//
// CLOSED ENUMS ONLY -- no question text, no subject identifier, no term,
// no anchor. Terms and anchor terms are free-form model output and NEVER
// appear in a telemetry field; only closed vocabulary members and counts
// do. This is the same rule chaos4632_question_family_telemetry.go holds
// itself to, and for the same reason: a telemetry field is a corpus leak
// vector, and the coverage-disclosure debug-log incident is the local
// precedent.
//
// FIRED ON EVERY FRAME REACHING VALIDATION, INCLUDING VALID ONES. The
// denominator has to be countable. An event that fires only on failure
// makes "the validator never rejects anything" indistinguishable from "the
// validator never ran", which is the lesson lane-4579 wrote up and §4.3
// already applies to family resolution.
//
// WHY THE INVARIANT VOCABULARY IS THE LOAD-BEARING PART. Round 2's P2-4
// found `failed_invariant` capped at i10, so I14 could fail with no value
// to record it -- "an invariant whose failure is unobservable is not
// enforced". The revision that fixed that reintroduced the same defect at
// i17 while I18 existed. The vocabulary here spans i1...i19 and the
// registry test asserts it against the invariant spec table, so a
// nineteenth-plus invariant added without a vocabulary member fails the
// build rather than going dark.

// FrameValidationOutcome is the closed outcome vocabulary of §13.6's
// telemetry table.
type FrameValidationOutcome string

const (
	// FrameValidationOutcomeValid: the frame passed A1, normalization
	// and A2 with no repair.
	FrameValidationOutcomeValid FrameValidationOutcome = "valid"
	// FrameValidationOutcomeRepaired: one bounded repair ran and the
	// repaired frame passed the SAME invariants, unrelaxed.
	FrameValidationOutcomeRepaired FrameValidationOutcome = "repaired"
	// FrameValidationOutcomeRefusedInvalid: still invalid after the one
	// permitted attempt (or the repairer errored, or none was
	// configured). The frame is refused and the family is unclassified --
	// refuse to guess.
	FrameValidationOutcomeRefusedInvalid FrameValidationOutcome = "refused_invalid"
	// FrameValidationOutcomeRefusedKindChange: the repair proposed a
	// SubjectExpression.Kind change the violated invariant does not name.
	//
	// A DISTINCT OUTCOME FROM refused_invalid ON PURPOSE. "The repair
	// could not fix it" and "the repair tried to answer a different
	// question" are different operational states, and collapsing them
	// would make a repairer drifting toward re-interpretation invisible
	// -- the same class of miss that made a split ensemble invisible
	// before the family telemetry split `model_plurality_rejected` from
	// `none`.
	FrameValidationOutcomeRefusedKindChange FrameValidationOutcome = "refused_kind_change"
)

var frameValidationOutcomes = [...]FrameValidationOutcome{
	FrameValidationOutcomeValid,
	FrameValidationOutcomeRepaired,
	FrameValidationOutcomeRefusedInvalid,
	FrameValidationOutcomeRefusedKindChange,
}

// FrameValidationOutcomeCount is four.
const FrameValidationOutcomeCount = len(frameValidationOutcomes)

// FrameValidationOutcomeVocabulary returns the closed vocabulary.
func FrameValidationOutcomeVocabulary() [FrameValidationOutcomeCount]FrameValidationOutcome {
	return frameValidationOutcomes
}

// ValidFrameValidationOutcome reports membership.
func ValidFrameValidationOutcome(value FrameValidationOutcome) bool {
	for _, member := range frameValidationOutcomes {
		if member == value {
			return true
		}
	}
	return false
}

// FrameValidationEvent is §13.6's whole telemetry row.
type FrameValidationEvent struct {
	// Outcome is the closed outcome vocabulary value.
	Outcome FrameValidationOutcome
	// FailedInvariant is the FIRST failure in table order, empty on a
	// valid frame.
	FailedInvariant FrameInvariant
	// FailedPhase is which phase that invariant belongs to. Recorded
	// alongside the invariant because "a1" vs "a2" is what tells an
	// operator whether the model emitted something malformed or the
	// server's own derivation left an axis undischarged -- two different
	// investigations from one invariant id.
	FailedPhase FrameValidationPhase
	// FailureDetail is the closed reason code within that invariant.
	FailureDetail FrameFailureDetail
	// RepairAttempted records whether the repairer was called.
	RepairAttempted bool
	// RepairLatencyMS is the repair call's wall time. Behaviour change
	// B4's gate is "inside the reserved deadline; measured repair rate +
	// latency", and an unmeasured extra model call is an unbounded one.
	RepairLatencyMS int64
	// RepairBoundViolation names why a repaired candidate was rejected,
	// empty when none was.
	RepairBoundViolation FrameRepairBoundViolation

	// ProposedKind / RepairedKind are the closed union discriminator as
	// PROPOSED and (if repaired) as REPAIRED. RepairedKind is empty when
	// no repair ran or the kind did not move.
	ProposedKind SubjectExpressionKind
	RepairedKind SubjectExpressionKind

	// ProposedGoals / RepairedGoals are the closed goal sets, as
	// proposed and as repaired. Sets, in vocabulary order, so two runs of
	// one frame produce identical rows.
	//
	// THE GOAL SET IS RECORDED PER FRAME BECAUSE §13.2.4 RULE 3 DEPENDS
	// ON IT. Goal-induced obligations are REQUIRED even though Goals are
	// model-proposed; the design accepts that and names the governance:
	// consensus over N samples, the shadow re-measure of goal
	// correctness, "and telemetry that records the goal set per sample so
	// a split is countable". Without this field that governance is a
	// sentence rather than a mechanism.
	ProposedGoals []InvestigationGoal
	RepairedGoals []InvestigationGoal

	// DerivedObligationCount / WidenedObligationCount are counts, not
	// lists: the obligation set is derivable from the goal set and the
	// other axes, so logging it whole would be redundant, while the
	// WIDENED count is the number §13.2.4 wants watched (a repairer or a
	// model steadily widening is a drift worth seeing).
	DerivedObligationCount int
	WidenedObligationCount int

	// ShapeDiverged records an I18 divergence: the sampler's Shape
	// disagreed with the frame's derived one and the DERIVED value won.
	// Not a rejection -- I18's contract is to record and let the derived
	// value win.
	ShapeDiverged bool
	// EmittedShape / DerivedShape are the closed shape enums, populated
	// only when they diverged.
	EmittedShape InvestigationShape
	DerivedShape InvestigationShape

	// FrameVersion is the derivation-table version, so an event can be
	// read against the table that produced it.
	FrameVersion string
}

// FrameValidationEventFrom projects a repair result into the telemetry
// event.
//
// proposed is the frame as the model proposed it, BEFORE normalization, so
// ProposedKind and ProposedGoals report what the model actually said
// rather than what the server made of it.
func FrameValidationEventFrom(proposed QuestionFrame, result FrameRepairResult, emittedShape InvestigationShape) FrameValidationEvent {
	event := FrameValidationEvent{
		Outcome:              result.Outcome,
		FailedInvariant:      result.Failure.Invariant,
		FailedPhase:          result.Failure.Phase,
		FailureDetail:        result.Failure.Detail,
		RepairAttempted:      result.RepairAttempted,
		RepairLatencyMS:      result.RepairLatency.Milliseconds(),
		RepairBoundViolation: result.ViolatedBound,
		ProposedKind:         vocabularyKindOnly(proposed.SubjectExpression.Kind),
		ProposedGoals:        vocabularyGoalsOnly(proposed.Goals),
		FrameVersion:         QuestionFrameVersion,
	}
	if result.Outcome == FrameValidationOutcomeRepaired {
		if result.Frame.SubjectExpression.Kind != proposed.SubjectExpression.Kind {
			event.RepairedKind = vocabularyKindOnly(result.Frame.SubjectExpression.Kind)
		}
		event.RepairedGoals = vocabularyGoalsOnly(result.Frame.Goals)
	}
	if result.Outcome == FrameValidationOutcomeValid || result.Outcome == FrameValidationOutcomeRepaired {
		event.DerivedObligationCount = len(result.Frame.Obligations)
		event.WidenedObligationCount = len(result.Frame.WidenedObligations)
		event.FrameVersion = result.Frame.Version
		if divergence, diverged := ShapeAgreement(emittedShape, result.Frame.SubjectExpression); diverged {
			event.ShapeDiverged = true
			event.EmittedShape = divergence.Emitted
			event.DerivedShape = divergence.Derived
		}
	}
	if event.FrameVersion == "" {
		event.FrameVersion = QuestionFrameVersion
	}
	return event
}

// FrameValidationTelemetry is the port frame validation reports through.
//
// A SEPARATE, EXPLICITLY-WIRED interface, never an optional one discovered
// by type assertion -- the same discipline QuestionFamilyTelemetry adopted
// and for the same recorded reason: CommitAffirmationTelemetry was
// optional, nothing in production implemented it, every event failed a
// type assertion, and the whole signal disappeared with tests passing
// throughout. SlogEngineTelemetry implements this directly, so a build in
// which nothing emits these events does not compile.
type FrameValidationTelemetry interface {
	// RecordFrameValidation reports ONE frame's validation. Fired on
	// EVERY frame reaching validation, including valid ones.
	RecordFrameValidation(ctx context.Context, principal storage.Principal, event FrameValidationEvent)
}

// vocabularyGoalsOnly and vocabularyKindOnly are the LAST LINE before a
// log field, and they exist because the first version of this projection
// did not have one.
//
// The event is built from the frame the model PROPOSED, which by
// construction is the one object here that has not yet been proven
// closed-vocabulary. A frame reaching this function with an unrecognized
// goal put ARBITRARY MODEL TEXT into `proposed_goals` and straight into
// the log line -- a corpus-leak vector and a breach of the closed-enum
// telemetry rule, found by adversarial review after this package's own
// leak test passed (that test planted its canary in the anchor TERMS and
// asserted the record was clean, so it covered the leak the author thought
// of rather than the leak class).
//
// Invariant I15 now REJECTS such a frame in phase A1, so in a correct flow
// nothing unrecognized ever reaches here. These filters stay anyway:
// telemetry is the one place where being wrong is silent and permanent,
// and a defence that is only correct while an upstream check holds is a
// defence that fails the day someone adds a second caller.
func vocabularyGoalsOnly(goals []InvestigationGoal) []InvestigationGoal {
	out := make([]InvestigationGoal, 0, len(goals))
	for _, goal := range goals {
		if ValidInvestigationGoal(goal) {
			out = append(out, goal)
		}
	}
	return out
}

func vocabularyKindOnly(kind SubjectExpressionKind) SubjectExpressionKind {
	if !ValidSubjectExpressionKind(kind) {
		return ""
	}
	return kind
}

// goalsLogValue renders a goal set as a flat slice of strings for slog.
// Flat scalars rather than a nested object for the reason
// sortedFamilyDistribution gives: slog's JSON handler renders a []any of
// scalars as a plain array an operator can read.
func goalsLogValue(goals []InvestigationGoal) []any {
	out := make([]any, 0, len(goals))
	for _, goal := range goals {
		out = append(out, string(goal))
	}
	return out
}
