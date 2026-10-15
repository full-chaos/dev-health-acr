package contextfabric

import (
	"context"
	"errors"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var errFactsOnlyNoSynthesisRoom = errors.New("no second synthesis could run; the facts are served without model prose")

// factsOnlyFailureClass is the ONE place that chooses the failure class a
// facts-only answer is composed under, from why the retry was declined.
func factsOnlyFailureClass(declined RetryDeclinedReason) SynthesisFailureClass {
	if declined == RetryDeclinedNothingToNarrow {
		return contractsv1.ContextFabricSynthesisFailureNotNarrowable
	}
	return contractsv1.ContextFabricSynthesisFailureDeadline
}

func factsOnlyFailure(declined RetryDeclinedReason) *SynthesisFailure {
	return &SynthesisFailure{Class: factsOnlyFailureClass(declined), DeferEvent: true, cause: errFactsOnlyNoSynthesisRoom}
}

// factsOnlyDeclinedReason: the retry reasons after which a facts-only answer
// is served in place of the refusal. The others say the retry could not help
// (cannot_reduce_axis) or the caller wrote the synthesis (supplied_synthesis).
func factsOnlyDeclinedReason(declined RetryDeclinedReason) bool {
	return declined == RetryDeclinedInsufficientDeadline || declined == RetryDeclinedNothingToNarrow
}

func (e *Engine) factsOnlyEligible(ctx context.Context, params synthesisAssemblyParams, first InvestigationResult) bool {
	if ctx.Err() != nil || params.WorkItemCensus != nil || params.Draft != nil || params.Degrade != nil || clientSynthesisRequested(params.Request) {
		return false
	}
	if _, ok := e.synthesizer.(DegradedSynthesizer); !ok {
		return false
	}
	return !slices.Contains(first.Limitations, contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation) &&
		!slices.Contains(first.Limitations, contractsv1.ContextFabricSynthesisDeadlineLimitation) &&
		!slices.Contains(first.Limitations, contractsv1.ContextFabricSynthesisNotNarrowableLimitation)
}

// serveFactsOnly assembles the question's facts, paths and coverage with the
// degraded composer over the pass-one input, calls no model, and serves it
// only when it fits the budget. ok is false on any other outcome and the
// refusal stands.
func (e *Engine) serveFactsOnly(ctx context.Context, principal storage.Principal, plan *AnswerPlan, params synthesisAssemblyParams, budget ResponseBudget, grouped bool, declined RetryDeclinedReason, members int) (InvestigationResult, assemblyTelemetry, bool) {
	allocation := AllocateItems(*plan, groupCountOf(params.Graph.Cohort), cohortMemberCount(params.Graph.Cohort))
	factsParams := params.forRetry(params.Graph, params.Facts, allocation)
	factsParams.Plan = *plan
	factsParams.Degrade = factsOnlyFailure(declined)
	composed, consumed, pending, cardinality, err := e.synthesizeAndAssemble(ctx, principal, factsParams)
	if err != nil {
		return InvestigationResult{}, assemblyTelemetry{}, false
	}
	composed = e.finalizeResult(ctx, principal, composed, *plan, params.Frame, factsParams.Facts, &pending, answerPassSecond, cardinality)
	measured, err := e.measureAssembledAttempt(ctx, principal, "facts_only_result", consumed, composed, budget)
	if err != nil || measured.Overrun != contractsv1.ContextFabricBudgetFits {
		return InvestigationResult{}, assemblyTelemetry{}, false
	}
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, members, members, grouped, false, measured.Overrun, params.GroupedNarrowingBasis)
	event.recordMeasurement(measured)
	event.PredictedItems = PredictedItemsForPlan(*plan, members)
	event.DeadlineReserved = e.synthesisDeadlineReserve > 0
	event.RetryDeclined = declined
	event.FactsOnlyAttempted, event.FactsOnlyServed = true, true
	event.OutcomeCompletenessState = composed.Completeness.State
	e.recordPlanNarrowing(ctx, principal, event)
	return composed, pending, true
}
