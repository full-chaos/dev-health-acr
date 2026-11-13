package contextfabric

import (
	"context"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AnswerPlanFallback is the wire record of a read fallback, aliased.
type AnswerPlanFallback = contractsv1.ContextFabricAnswerPlanFallback

// ReadFallbackOutcome is the closed outcome of one read fallback decision.
// Only applied reaches the served plan; the others are telemetry.
type ReadFallbackOutcome string

const (
	ReadFallbackOutcomeApplied           ReadFallbackOutcome = ReadFallbackOutcome(contractsv1.ContextFabricReadFallbackOutcomeApplied)
	ReadFallbackOutcomeSuppressedFlagOff ReadFallbackOutcome = "suppressed_flag_off"
	ReadFallbackOutcomePlannerFailed     ReadFallbackOutcome = "planner_failed"
	ReadFallbackOutcomePlanInvalid       ReadFallbackOutcome = "plan_invalid"
	ReadFallbackOutcomeNotApplicable     ReadFallbackOutcome = "not_applicable"
)

// ReadFallbackOutcomeVocabulary is the closed outcome vocabulary in order.
func ReadFallbackOutcomeVocabulary() []ReadFallbackOutcome {
	return []ReadFallbackOutcome{
		ReadFallbackOutcomeApplied, ReadFallbackOutcomeSuppressedFlagOff, ReadFallbackOutcomePlannerFailed,
		ReadFallbackOutcomePlanInvalid, ReadFallbackOutcomeNotApplicable,
	}
}

// ReadFallbackDecisionEvent is the one Info line of a turn's read fallback
// decision: closed labels and a count, never an id, a kind or a value.
type ReadFallbackDecisionEvent struct {
	Reason       ReadFallbackReason
	Outcome      ReadFallbackOutcome
	PlannedKinds int
	// Trigger names the test that fired; empty when the decision did not apply.
	Trigger ReadFallbackTrigger
	// ErrorClass names a planner failure; empty on every other outcome.
	ErrorClass string
	// Rejection names the validator's refusal; empty on every other outcome.
	Rejection ReadPlanRejection
}

// readPlannerDeadlineCap is the most time the planner call may take.
const readPlannerDeadlineCap = 15 * time.Second

type readFallbackDecision struct {
	reason  ReadFallbackReason
	outcome ReadFallbackOutcome
	kinds   []FactKind
}

// planRecord is the served plan's fallback record, nil unless applied.
func (d readFallbackDecision) planRecord() *AnswerPlanFallback {
	if d.outcome != ReadFallbackOutcomeApplied {
		return nil
	}
	return &AnswerPlanFallback{
		Reason:       d.reason,
		PlannedKinds: append([]FactKind(nil), d.kinds...),
		Outcome:      contractsv1.ContextFabricReadFallbackOutcomeApplied,
	}
}

// decideReadFallback runs the family-fit test and, when it fires and the
// deployment switch is on, the read planner. Every failure leaves the family
// plan unchanged: only an applied outcome returns kinds.
func (e *Engine) decideReadFallback(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpretation InterpretedQuestion, family QuestionFamilyOutcome, workItemList bool) readFallbackDecision {
	definition, _ := LookupQuestionFamily(family.Family)
	var memberKind SubjectKind
	if family.Frame != nil {
		memberKind, _ = family.Frame.SubjectExpression.MemberKind()
	}
	capabilities := e.readFallbackCapabilities()
	interpretedKinds := make([]FactKind, 0, len(interpretation.FactRequirements))
	for _, requirement := range interpretation.FactRequirements {
		interpretedKinds = append(interpretedKinds, requirement.Kind)
	}
	reason, trigger := DecideReadFallbackWithTrigger(ReadFallbackDecisionInput{
		Definition: definition, JudgmentKind: interpretation.RequestedJudgmentKind, InterpretedKinds: interpretedKinds,
		MemberKind: memberKind, Capabilities: capabilities, WorkItemList: workItemList,
	})
	decision := readFallbackDecision{reason: reason}
	event := ReadFallbackDecisionEvent{Reason: decision.reason, Trigger: trigger}
	switch {
	case decision.reason == "":
		decision.outcome = ReadFallbackOutcomeNotApplicable
	case !e.readFallbackEnabled:
		decision.outcome = ReadFallbackOutcomeSuppressedFlagOff
	case e.readPlanner == nil:
		decision.outcome = ReadFallbackOutcomePlannerFailed
		event.ErrorClass = readPlannerNoRuntime
	default:
		planCtx, cancel := context.WithTimeout(ctx, e.readPlannerDeadline(ctx))
		result := e.readPlanner.PlanReads(planCtx, principal, ReadPlanningInput{
			Question: request.Question, Interpretation: interpretation, Reason: decision.reason, MemberKind: memberKind,
			OfferedKinds: ReadPlanOfferedKinds(capabilities, memberKind), Capabilities: capabilities, RequestID: request.RequestID,
		})
		cancel()
		switch {
		case result.Err != nil:
			decision.outcome = ReadFallbackOutcomePlannerFailed
			event.ErrorClass = readPlannerErrorClass(result.Err)
		case result.Rejection != "" || len(result.Kinds) == 0:
			decision.outcome = ReadFallbackOutcomePlanInvalid
			event.Rejection = result.Rejection
		default:
			decision.outcome = ReadFallbackOutcomeApplied
			decision.kinds = result.Kinds
		}
	}
	event.Outcome = decision.outcome
	event.PlannedKinds = len(decision.kinds)
	if e.telemetry != nil {
		e.telemetry.RecordReadFallbackDecision(ctx, principal, event)
	}
	return decision
}

// readPlannerDeadline is the planner's share of the request: what is left
// after the synthesis reserve, capped.
func (e *Engine) readPlannerDeadline(ctx context.Context) time.Duration {
	budget := readPlannerDeadlineCap
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline) - e.synthesisDeadlineReserve; left < budget {
			budget = max(left, 0)
		}
	}
	return budget
}

func (e *Engine) readFallbackCapabilities() []FactCapability {
	if source, ok := e.facts.(capabilitySource); ok {
		return source.Capabilities()
	}
	return nil
}

// withReadFallbackLimitation states, on an answer whose reads the planner
// chose, that the model chose them.
func withReadFallbackLimitation(result InvestigationResult, plan AnswerPlan) InvestigationResult {
	if plan.Fallback == nil || plan.Fallback.Outcome != contractsv1.ContextFabricReadFallbackOutcomeApplied {
		return result
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{contractsv1.ContextFabricReadFallbackLimitation})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	return result
}
