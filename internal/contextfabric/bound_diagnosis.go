package contextfabric

import (
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ClassifyInterpretationRejection wraps cause -- already produced by
// question.Validate() failing -- into ErrInterpretationRejected (which also
// wraps ErrModelOutput, so every existing errors.Is(err, ErrModelOutput)
// caller is unaffected), attaching a ModelBoundViolation when question's
// rejection is attributable to a specific contracts/v1 model-facing bound
// (see contractsv1.DiagnoseContextFabricInterpretedQuestionBound).
//
// question must be the ACTUAL (possibly invalid) value Validate() was
// called against, not a zero value -- CHAOS-3784 round-2 finding F1:
// genkitruntime.Runtime.InterpretQuestion's own toDomain() call is the
// production ModelRuntime's OWN Validate() call site (it self-validates
// before RuntimeQuestionInterpreter.Interpret ever sees the result), so
// this function must be reachable from there too, not only from
// RuntimeQuestionInterpreter.Interpret's defensive re-validation for a
// ModelRuntime that does not self-validate.
func ClassifyInterpretationRejection(question InterpretedQuestion, cause error) error {
	wrapped := fmt.Errorf("%w: %w: %v", ErrInterpretationRejected, ErrModelOutput, cause)
	bound, diagnosed := contractsv1.DiagnoseContextFabricInterpretedQuestionBound(question)
	return withBoundViolation(wrapped, bound, diagnosed)
}

// ClassifySynthesisRejection is ClassifyInterpretationRejection's
// synthesis-side counterpart: draft must be the actual (possibly invalid)
// value ValidateAgainst was called against. See its doc comment --
// genkitruntime.Runtime.SynthesizeAnswer calls draft.ValidateAgainst(input)
// itself (it has the SynthesisInput the check needs), so this must be
// reachable from there too, not only from
// RuntimeAnswerSynthesizer.Synthesize's defensive re-validation.
func ClassifySynthesisRejection(draft SynthesisDraft, cause error) error {
	wrapped := fmt.Errorf("%w: %w: %v", ErrSynthesisRejected, ErrModelOutput, cause)
	bound, diagnosed := diagnoseSynthesisDraftBound(draft)
	return withBoundViolation(wrapped, bound, diagnosed)
}

// diagnoseSynthesisDraftBound re-derives, for reporting purposes only,
// which model-facing length/count bound (if any) caused d to fail
// ValidateAgainst -- checked in the same order ValidateAgainst itself
// visits: claimed facts, then drivers, then the remaining_work/
// readiness_gaps/conflicts findings. It never decides accept/reject; it
// only explains a rejection ValidateAgainst already made (CHAOS-3784). See
// contractsv1's context_fabric_bound_diagnosis.go doc comment.
//
// Top-level synthesis collection caps (drivers.max_count,
// remaining_work.max_count, and siblings in
// contractsv1.ContextFabricModelFacingBounds) are deliberately NOT
// diagnosed here: ValidateAgainst itself never checks them -- only
// ContextFabricInvestigationResult.Validate() does, later and against the
// already-composed InvestigationResult, classified ErrInvalidResult (see
// engine.go), not ErrSynthesisRejected. A synthesis draft that violates one
// of those top-level caps therefore never reaches this function with a
// non-nil ValidateAgainst error in the first place; distinguishing THAT
// class of rejection is a separate, pre-existing gap (a violation there is
// misattributed to ACR as a 500, not to the model) outside CHAOS-3784's
// narrow scope.
func diagnoseSynthesisDraftBound(d SynthesisDraft) (bound string, ok bool) {
	for _, claim := range d.ClaimedFacts {
		if bound, ok := contractsv1.DiagnoseContextFabricClaimedFactBound(claim); ok {
			return bound, true
		}
	}
	for _, driver := range d.Drivers {
		if bound, ok := contractsv1.DiagnoseContextFabricDriverJudgmentBound(driver); ok {
			return bound, true
		}
	}
	for _, findings := range [][]Finding{d.RemainingWork, d.ReadinessGaps, d.Conflicts} {
		for _, finding := range findings {
			if bound, ok := contractsv1.DiagnoseContextFabricFindingBound(finding); ok {
				return bound, true
			}
		}
	}
	return "", false
}
