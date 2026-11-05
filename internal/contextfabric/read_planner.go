package contextfabric

import (
	"context"
	"errors"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ReadPlanningInput is the read planner's request (CHAOS-13002).
type ReadPlanningInput struct {
	Question       string
	Interpretation InterpretedQuestion
	Reason         ReadFallbackReason
	MemberKind     SubjectKind
	// OfferedKinds is the only kind vocabulary the model sees:
	// ReadPlanOfferedKinds for MemberKind.
	OfferedKinds []FactKind
	// Capabilities is what the plan is validated against. It is never sent
	// to the model.
	Capabilities []FactCapability
	RequestID    string
}

// ReadPlanningModelRuntime is the raw model-call boundary of the read planner.
// genkitruntime.Runtime and modelruntimeresolver.Resolver implement it; the
// output is unguarded.
type ReadPlanningModelRuntime interface {
	PlanReads(ctx context.Context, principal storage.Principal, input ReadPlanningInput) (ReadPlan, ModelExecutionReceipt, error)
}

// ReadPlanningResult is a ReadPlanner's classified answer. Err is the model
// call's failure; Rejection is the validator's refusal of a plan the model
// did return. Kinds is set only when both are empty.
type ReadPlanningResult struct {
	Kinds     []FactKind
	Rejection ReadPlanRejection
	Err       error
}

// ReadPlanner is the engine's port for the read planner. It never returns an
// error by signature: a failure is a field of the result, and the engine then
// runs the family path unchanged.
type ReadPlanner interface {
	PlanReads(ctx context.Context, principal storage.Principal, input ReadPlanningInput) ReadPlanningResult
}

// RuntimeReadPlanner records the receipt of every attempted call, then
// validates the plan the model returned.
type RuntimeReadPlanner struct {
	Runtime ReadPlanningModelRuntime
	Sink    ModelReceiptSink
	Logger  *slog.Logger
}

func (r RuntimeReadPlanner) PlanReads(ctx context.Context, principal storage.Principal, input ReadPlanningInput) ReadPlanningResult {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if r.Runtime == nil {
		r.logDecision(ctx, logger, principal, input, ModelExecutionReceipt{}, readPlannerNoRuntime, "", 0)
		return ReadPlanningResult{Err: ErrModelUnavailable}
	}
	plan, receipt, err := r.Runtime.PlanReads(ctx, principal, input)
	if sinkErr := recordModelReceipt(ctx, principal, r.Sink, receipt); sinkErr != nil {
		// The error text is never logged: a storage error can carry driver detail.
		logger.WarnContext(ctx, "context fabric read planner receipt sink failed",
			"org_id", SanitizeLogAttr(principal.OrgID), "request_id", SanitizeLogAttr(input.RequestID),
			"operation", SanitizeLogAttr(string(receipt.Operation)), "outcome", SanitizeLogAttr(receipt.Outcome))
	}
	if err != nil {
		r.logDecision(ctx, logger, principal, input, receipt, readPlannerErrorClass(err), "", 0)
		return ReadPlanningResult{Err: err}
	}
	kinds, rejection := ValidateReadPlan(plan, input.MemberKind, input.Capabilities)
	r.logDecision(ctx, logger, principal, input, receipt, "", rejection, len(kinds))
	return ReadPlanningResult{Kinds: kinds, Rejection: rejection}
}

const readPlannerNoRuntime = "no_runtime"

// readPlannerErrorClass names a planner call failure without its text.
func readPlannerErrorClass(err error) string {
	switch {
	case errors.Is(err, ErrPlatformModelWithheld):
		return "platform_model_withheld"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrModelTimeout):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled), errors.Is(err, ErrModelCancelled):
		return "canceled"
	case errors.Is(err, ErrModelRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrModelInputTooLarge):
		return "input_too_large"
	case errors.Is(err, ErrModelOutput):
		return "model_output"
	case errors.Is(err, ErrModelUnavailable):
		return "model_unavailable"
	default:
		return "internal"
	}
}

// ReadPlannerErrorClassVocabulary is readPlannerErrorClass's closed range,
// plus the no-runtime class.
func ReadPlannerErrorClassVocabulary() []string {
	return []string{"platform_model_withheld", "deadline_exceeded", "canceled", "rate_limited", "input_too_large", "model_output", "model_unavailable", "internal", readPlannerNoRuntime}
}

func (r RuntimeReadPlanner) logDecision(ctx context.Context, logger *slog.Logger, principal storage.Principal, input ReadPlanningInput, receipt ModelExecutionReceipt, errorClass string, rejection ReadPlanRejection, kinds int) {
	logger.InfoContext(ctx, "context fabric read planner decision",
		"org_id", SanitizeLogAttr(principal.OrgID),
		"request_id", SanitizeLogAttr(input.RequestID),
		"operation", SanitizeLogAttr(string(receipt.Operation)),
		"model_outcome", SanitizeLogAttr(receipt.Outcome),
		"error_class", SanitizeLogAttr(errorClass),
		"rejection", SanitizeLogAttr(string(rejection)),
		"offered_kinds", len(input.OfferedKinds),
		"planned_kinds", kinds,
	)
}
