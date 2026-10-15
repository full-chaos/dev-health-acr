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
// facts-only answer is composed under, from why no second synthesis served:
// no time for one (deadline), or one that could not make the answer smaller
// (nothing to narrow, the rows alone overran, or the retry ran and still
// overran: RetryDeclinedNotApplicable at the post-retry refusal).
func factsOnlyFailureClass(declined RetryDeclinedReason) SynthesisFailureClass {
	switch declined {
	case RetryDeclinedNothingToNarrow, RetryDeclinedCannotReduceAxis, RetryDeclinedNotApplicable:
		return contractsv1.ContextFabricSynthesisFailureNotNarrowable
	}
	return contractsv1.ContextFabricSynthesisFailureDeadline
}

func factsOnlyFailure(declined RetryDeclinedReason) *SynthesisFailure {
	return &SynthesisFailure{Class: factsOnlyFailureClass(declined), DeferEvent: true, cause: errFactsOnlyNoSynthesisRoom}
}

// factsOnlyDeclinedReason: the retry reasons after which a facts-only answer
// is served in place of the refusal: no second synthesis could run for want of
// time (a deadline too short, or no reserve configured to hold one) or of
// members to cut, or a second synthesis could not shrink the rows that
// overran (the facts-only answer carries scalar values, not tables).
// supplied_synthesis stays a refusal: the caller wrote the answer, and the
// facts are not theirs to replace.
func factsOnlyDeclinedReason(declined RetryDeclinedReason) bool {
	switch declined {
	case RetryDeclinedInsufficientDeadline, RetryDeclinedNoReserve, RetryDeclinedNothingToNarrow, RetryDeclinedCannotReduceAxis:
		return true
	}
	return false
}

func (e *Engine) factsOnlyEligible(ctx context.Context, params synthesisAssemblyParams, first InvestigationResult) bool {
	if ctx.Err() != nil || params.Recompose != nil || clientSynthesisRequested(params.Request) {
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
	factsParams.Recompose = factsOnlyFailure(declined)
	composed, consumed, pending, cardinality, err := e.synthesizeAndAssemble(ctx, principal, factsParams)
	if err != nil {
		return InvestigationResult{}, assemblyTelemetry{}, false
	}
	if params.WorkItemCensus != nil {
		composed = ServeWorkItemTupleCensus(composed, params.WorkItemCensus)
		applyCoverageDisplayLabels(&composed)
		composed = restrictWorkItemTupleEvidence(composed)
	}
	composed = e.finalizeResult(ctx, principal, composed, *plan, params.Frame, factsParams.Facts, &pending, answerPassSecond, cardinality)
	measurementInput := composed
	if params.WorkItemCensus != nil {
		measurementInput = ApplyServerCompletenessAuthority(ApplyServedStatusFloors(composed), e.serverCompletenessAuthorityEnabled, e.serverCompletenessAuthoritySymmetricEnabled, DeriveCompletenessAuthority(ApplyServedStatusFloors(composed)))
	}
	measured, err := e.measureAssembledAttempt(ctx, principal, "facts_only_result", consumed, measurementInput, budget)
	if err != nil || measured.Overrun != contractsv1.ContextFabricBudgetFits {
		return InvestigationResult{}, assemblyTelemetry{}, false
	}
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, members, members, grouped, false, measured.Overrun, params.GroupedNarrowingBasis)
	event.recordMeasurement(measured)
	event.PredictedItems = PredictedItemsForPlan(*plan, members)
	event.DeadlineReserved = e.synthesisDeadlineReserve > 0
	event.RetryDeclined = declined
	event.RetryAttempted = declined == RetryDeclinedNotApplicable
	event.FactsOnlyAttempted, event.FactsOnlyServed = true, true
	event.OutcomeCompletenessState = composed.Completeness.State
	e.recordPlanNarrowing(ctx, principal, event)
	return composed, pending, true
}
