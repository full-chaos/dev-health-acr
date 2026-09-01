package contextfabric

import (
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SynthesisNarrowingSnapshot (CHAOS-4726) carries the narrowing-plan state
// that existed at the moment synthesis was invoked, wrapped around whatever
// error synthesis returned.
//
// RecordPlanNarrowing's "context fabric plan narrowing" line only reaches
// the assembled_result stage (stage 3) once a result actually assembles --
// which a rejected synthesis by definition never does. 40/40 live 422s
// proved that stage 3's basis is therefore structurally unobservable for
// every synthesis_rejected outcome, even though stage 1 (cardinality) and
// stage 2 (synthesis_input) already ran and already narrowed the cohort the
// rejected draft was built from. This type is the pre-synthesis half of
// that same fact, captured at the one call site that both has the plan in
// scope and sits before the outcome is known.
//
// CLOSED ENUMS ONLY -- DeclaredBasis and LastBasis are members of
// contractsv1.ContextFabricNarrowingBasis, LastStage is a member of
// contractsv1.ContextFabricPlanNarrowingStage. No question text, no subject
// identifier, no group label.
type SynthesisNarrowingSnapshot struct {
	// DeclaredBasis is stage 1's always-declared order (AnswerPlanBudget.
	// NarrowingBasis) -- present even when stage 1 never needed to act.
	DeclaredBasis contractsv1.ContextFabricNarrowingBasis
	// LastStage and LastBasis describe the most recent narrowing step that
	// actually ran before synthesis was invoked -- stage 1 or stage 2, since
	// stage 3 cannot have run yet. Both are the zero value when neither
	// stage narrowed anything, which is itself a diagnosable fact: the
	// cohort synthesis received was never trimmed.
	LastStage contractsv1.ContextFabricPlanNarrowingStage
	LastBasis contractsv1.ContextFabricNarrowingBasis

	err error
}

func (s *SynthesisNarrowingSnapshot) Error() string { return s.err.Error() }
func (s *SynthesisNarrowingSnapshot) Unwrap() error { return s.err }

// NewSynthesisNarrowingSnapshot constructs a *SynthesisNarrowingSnapshot
// wrapping err. Exported so a caller that already knows the pre-synthesis
// narrowing state -- or a test simulating one -- can attach it without
// reaching into an unexported field, matching NewModelBoundViolation's own
// shape.
func NewSynthesisNarrowingSnapshot(declaredBasis contractsv1.ContextFabricNarrowingBasis, lastStage contractsv1.ContextFabricPlanNarrowingStage, lastBasis contractsv1.ContextFabricNarrowingBasis, err error) *SynthesisNarrowingSnapshot {
	return &SynthesisNarrowingSnapshot{DeclaredBasis: declaredBasis, LastStage: lastStage, LastBasis: lastBasis, err: err}
}

// withSynthesisNarrowingSnapshot wraps err with plan's narrowing state as of
// the call site, or returns err unchanged when err is nil. plan.Narrowing
// holds every stage-1/stage-2 step recorded so far, in order -- its last
// entry (if any) is exactly "the most recent narrowing before synthesis
// ran".
func withSynthesisNarrowingSnapshot(err error, plan AnswerPlan) error {
	if err == nil {
		return nil
	}
	var lastStage contractsv1.ContextFabricPlanNarrowingStage
	var lastBasis contractsv1.ContextFabricNarrowingBasis
	if n := len(plan.Narrowing); n > 0 {
		last := plan.Narrowing[n-1]
		lastStage, lastBasis = last.Stage, last.Basis
	}
	return NewSynthesisNarrowingSnapshot(plan.Budget.NarrowingBasis, lastStage, lastBasis, err)
}

// SynthesisNarrowingSnapshotOf reports the pre-synthesis narrowing state
// attached to err, if any. Mirrors SynthesisRejectionReasonOf's shape: ok is
// false when err carries no snapshot, so a caller never confuses "narrowing
// was declared canonical_id_lexical and nothing else acted" (a real,
// disclosed answer) with "no snapshot was attached at all".
func SynthesisNarrowingSnapshotOf(err error) (SynthesisNarrowingSnapshot, bool) {
	var snapshot *SynthesisNarrowingSnapshot
	if errors.As(err, &snapshot) && snapshot != nil {
		return *snapshot, true
	}
	return SynthesisNarrowingSnapshot{}, false
}
