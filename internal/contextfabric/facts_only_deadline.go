package contextfabric

import (
	"context"
	"errors"
	"log/slog"
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
	case RetryDeclinedNothingToNarrow:
		return contractsv1.ContextFabricSynthesisFailureNotNarrowable
	case RetryDeclinedCannotReduceAxis:
		return contractsv1.ContextFabricSynthesisFailureRowsOverBudget
	case RetryDeclinedNotApplicable:
		return contractsv1.ContextFabricSynthesisFailureRetryOverBudget
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
		!slices.Contains(first.Limitations, contractsv1.ContextFabricSynthesisNotNarrowableLimitation) &&
		!slices.Contains(first.Limitations, contractsv1.ContextFabricSynthesisRowsOverBudgetLimitation) &&
		!slices.Contains(first.Limitations, contractsv1.ContextFabricSynthesisRetryRanLimitation)
}

// FactsOnlyDeclined is the CLOSED vocabulary of why a facts-only answer that was tried did not serve, on the plan
// narrowing event (facts_only_declined): empty when it served or was not tried.
type FactsOnlyDeclined string

const (
	// FactsOnlyServedOrUntried is the zero value: the facts-only answer served, or no facts-only answer was tried.
	FactsOnlyServedOrUntried FactsOnlyDeclined = ""
	// FactsOnlyComposeFailed: assembling the facts-only answer failed (a server fault, logged with its class).
	FactsOnlyComposeFailed FactsOnlyDeclined = "compose_failed"
	// FactsOnlyMeasureFailed: measuring the assembled answer failed (a server fault, logged with its class).
	FactsOnlyMeasureFailed FactsOnlyDeclined = "measure_failed"
	// FactsOnlyOverBudget: the facts-only answer was measured and did not fit the budget; the refusal stands.
	FactsOnlyOverBudget FactsOnlyDeclined = "over_budget"
)

var factsOnlyDeclinedVocabulary = [...]FactsOnlyDeclined{FactsOnlyServedOrUntried, FactsOnlyComposeFailed, FactsOnlyMeasureFailed, FactsOnlyOverBudget}

// FactsOnlyDeclinedVocabulary returns the closed vocabulary in published order.
func FactsOnlyDeclinedVocabulary() [len(factsOnlyDeclinedVocabulary)]FactsOnlyDeclined {
	return factsOnlyDeclinedVocabulary
}

// factsOnlyTry is what the refusal event says about a facts-only answer that was tried and did not serve.
type factsOnlyTry struct {
	attempted bool
	declined  FactsOnlyDeclined
}

func (e *Engine) logFactsOnlyFault(ctx context.Context, principal storage.Principal, declined FactsOnlyDeclined, err error) {
	slog.Default().WarnContext(ctx, "context fabric facts-only answer not served",
		"org_id", SanitizeLogAttr(principal.OrgID), "facts_only_declined", string(declined),
		"error_class", SanitizeLogAttr(factsOnlyErrorClass(err)))
}

// factsOnlyErrorClass names a fault without its text, which may carry query content.
func factsOnlyErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrInvalidResult):
		return "invalid_result"
	case errors.Is(err, ErrUnavailable):
		return "dependency_unavailable"
	default:
		return "internal"
	}
}

// serveFactsOnly assembles the question's facts, paths and coverage with the
// degraded composer over the pass-one input, calls no model, and serves it
// only when it fits the budget. The returned FactsOnlyDeclined is empty when it
// served; any other value is why it did not, and the refusal stands.
func (e *Engine) serveFactsOnly(ctx context.Context, principal storage.Principal, plan *AnswerPlan, params synthesisAssemblyParams, budget ResponseBudget, grouped bool, declined RetryDeclinedReason, members int) (InvestigationResult, assemblyTelemetry, FactsOnlyDeclined) {
	allocation := AllocateItems(*plan, groupCountOf(params.Graph.Cohort), cohortMemberCount(params.Graph.Cohort))
	factsParams := params.forRetry(params.Graph, params.Facts, allocation)
	factsParams.Plan = *plan
	factsParams.Recompose = factsOnlyFailure(declined)
	composed, consumed, pending, cardinality, err := e.synthesizeAndAssemble(ctx, principal, factsParams)
	if err != nil {
		e.logFactsOnlyFault(ctx, principal, FactsOnlyComposeFailed, err)
		return InvestigationResult{}, assemblyTelemetry{}, FactsOnlyComposeFailed
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
	if err != nil {
		e.logFactsOnlyFault(ctx, principal, FactsOnlyMeasureFailed, err)
		return InvestigationResult{}, assemblyTelemetry{}, FactsOnlyMeasureFailed
	}
	if measured.Overrun != contractsv1.ContextFabricBudgetFits {
		return InvestigationResult{}, assemblyTelemetry{}, FactsOnlyOverBudget
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
	return composed, pending, FactsOnlyServedOrUntried
}
