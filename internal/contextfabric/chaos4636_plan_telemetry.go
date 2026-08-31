package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4636: plan-narrowing telemetry.
//
// Every narrowing step is an outcome-affecting decision branch, so under
// AGENTS.md's CANONICAL ARCHITECTURE rule it must emit decision-basis
// telemetry in the SAME change that adds the branch: a defect here has to be
// diagnosable from the run's own artifacts, never by re-reading source or
// re-running with instrumentation added afterward.
//
// The plan itself is persisted ON the result, so a caller can see what was
// narrowed. This event is the OPERATOR's half of the same fact, and it
// carries the two things the wire deliberately does not: which budget axis
// was measured to be over, and what the measured numbers actually were.
//
// CLOSED ENUMS AND COUNTS ONLY. No question text, no subject identifier, no
// group label -- the same discipline the family-resolution event holds, for
// the same reason.

// PlanNarrowingEvent is one narrowing decision, or one measured fit.
type PlanNarrowingEvent struct {
	// Family and FamilyVersion identify WHICH plan narrowed, so a
	// regression can be attributed to a family-table row.
	Family        QuestionFamily
	FamilyVersion string
	// Stage is which of the three moments acted. The three are not
	// interchangeable: a stage-1 clamp is precautionary (nothing has been
	// measured yet), a stage-2 clamp bounds what synthesis is given, and a
	// stage-3 clamp is reactive to a measurement that already failed.
	Stage contractsv1.ContextFabricPlanNarrowingStage
	// Basis is the declared order members were taken in.
	Basis contractsv1.ContextFabricNarrowingBasis
	// Before and After are counts of what was narrowed.
	Before int
	After  int
	// Groups reports whether GROUPS were narrowed rather than members.
	// Decision D2 ruled member-first, so this being true is the rare case
	// and worth being able to count.
	Groups bool
	// Overrun names which budget axis forced the step, empty when nothing
	// was measured (stage 1) or nothing was over (a recorded fit).
	Overrun contractsv1.ContextFabricBudgetOverrun
	// MeasuredItems and MeasuredBytes are what stage 3 actually measured,
	// with the ceilings it measured against. Zero on the earlier stages,
	// where the quantities do not exist yet -- which is the whole reason
	// there are three stages.
	MeasuredItems      int
	MeasuredBytes      int64
	MaxItems           int
	MaxSerializedBytes int64
	// RetryAttempted and RetryFit are the re-synthesis outcome. Both false
	// outside stage 3.
	RetryAttempted bool
	RetryFit       bool
	// RefusalPlanned is decision D5's terminal case: the retry did not fit
	// (or could not be run) and the answer became a PLANNED, EXPLAINED
	// refusal rather than an unexplained 413. Counting it is how "how often
	// does the terminal case actually fire" stops being a guess.
	RefusalPlanned bool
	// DeadlineReserved reports whether a synthesis deadline was reserved.
	// D5 calls the reservation non-negotiable, so an event saying it was
	// absent is the signal that a deployment is one slow synthesis away
	// from a 504 instead of a partial answer.
	DeadlineReserved bool
	// RetryDeclined names WHY the one bounded retry did not run.
	//
	// It exists because DeadlineReserved alone was ambiguous between two
	// situations with completely different fixes -- "this deployment
	// reserves nothing" (change the configuration) and "this request had
	// already spent too much of its deadline" (the answer is genuinely
	// slow) -- and a third, "there was nothing left to narrow", which is
	// neither. Found on the rig: a live refusal reported
	// deadline_reserved=false and an operator could not tell which of the
	// three it was without re-reading source, which is exactly the bar
	// AGENTS.md's diagnosis-in-artifacts rule sets.
	RetryDeclined RetryDeclinedReason
}

// RetryDeclinedReason is the CLOSED vocabulary of why the re-synthesis did
// not run.
type RetryDeclinedReason string

const (
	// RetryDeclinedNotApplicable is the zero value: either the answer fit,
	// or the retry did run.
	RetryDeclinedNotApplicable RetryDeclinedReason = ""
	// RetryDeclinedNoReserve: this engine reserves no synthesis deadline,
	// so it will not gamble the caller's remaining time on a second model
	// call. A deployment-configuration signal.
	RetryDeclinedNoReserve RetryDeclinedReason = "no_reserve"
	// RetryDeclinedInsufficientDeadline: a reserve is configured, but this
	// request had already spent enough of its deadline that a retry would
	// have timed out. A slow-answer signal, not a configuration one.
	RetryDeclinedInsufficientDeadline RetryDeclinedReason = "insufficient_deadline"
	// RetryDeclinedNothingToNarrow: the cohort was already at one member,
	// or every group was down to its last member and narrowing further
	// would drop a group, which decision D2 forbids.
	RetryDeclinedNothingToNarrow RetryDeclinedReason = "nothing_to_narrow"
)

// PlanNarrowingEventFrom builds the event for the two pre-measurement stages,
// where there is nothing measured to report.
func PlanNarrowingEventFrom(plan AnswerPlan, stage contractsv1.ContextFabricPlanNarrowingStage, before, after int, groups bool, overrun contractsv1.ContextFabricBudgetOverrun) PlanNarrowingEvent {
	return PlanNarrowingEvent{
		Family:             plan.Family,
		FamilyVersion:      plan.FamilyVersion,
		Stage:              stage,
		Basis:              planStageBasis(stage, groups),
		Before:             before,
		After:              after,
		Groups:             groups,
		Overrun:            overrun,
		MaxItems:           plan.Budget.MaxItems,
		MaxSerializedBytes: plan.Budget.MaxSerializedBytes,
	}
}

// planStageBasis names the order a stage takes members in. It is derived
// rather than passed so that a caller cannot record a basis its stage could
// not have used -- the mistake §6.3a records every earlier revision making,
// in the form of narrowing by an order that did not exist yet.
//
// grouped says whether the cohort ACTUALLY carries a group axis, not merely
// whether the stage runs late enough for one to exist. Reporting
// largest_group_round_robin for a flat cohort would name a basis with no
// groups to round-robin over -- found on the rig, where a live
// discovered_cohort_ranking refusal logged exactly that.
func planStageBasis(stage contractsv1.ContextFabricPlanNarrowingStage, grouped bool) contractsv1.ContextFabricNarrowingBasis {
	if grouped {
		// Round-robin across groups, largest first, so EVERY group
		// survives: decision D2, member-first, ruled 2026-08-30.
		return contractsv1.ContextFabricNarrowingBasisLargestGroupRoundRobin
	}
	return contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical
}

// PlanTelemetry is the required sink for plan-narrowing events.
//
// It is a REQUIRED interface embedded in EngineTelemetry, never an optional
// side interface discovered by type assertion. CHAOS-4085 paid for that
// lesson once already: an optional telemetry interface that a sink silently
// stopped implementing made an entire event class disappear with every test
// still passing. A build in which nothing emits these events must not
// compile.
type PlanTelemetry interface {
	// RecordPlanNarrowing reports ONE narrowing decision. It fires per
	// step, not per investigation, because the three stages are separately
	// diagnosable and a single summary field could not represent two stages
	// acting for different reasons.
	RecordPlanNarrowing(ctx context.Context, principal storage.Principal, event PlanNarrowingEvent)
}

// recordPlanNarrowing is the engine's nil-safe emitter.
func (e *Engine) recordPlanNarrowing(ctx context.Context, principal storage.Principal, event PlanNarrowingEvent) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordPlanNarrowing(ctx, principal, event)
}

// effectiveResponseBudget is the ceiling this request will actually be
// measured against: the service configuration narrowed by whatever the caller
// asked for.
//
// It mirrors the route's own `min(config.MaxSerializedBytes,
// request.Options.MaxSerializedBytes)` exactly, because a fit decided in the
// engine that the route then disagrees with is worse than no engine-side
// decision at all. A zero on either axis means unbounded on that axis, which
// is what an engine composed without these options means -- and that engine
// behaves precisely as it did before this slice.
func (e *Engine) effectiveResponseBudget(request InvestigationRequest) ResponseBudget {
	budget := ResponseBudget{MaxItems: e.maxItems, MaxSerializedBytes: e.maxSerializedBytes}
	if requested := request.Options.MaxSerializedBytes; requested > 0 {
		if budget.MaxSerializedBytes <= 0 || int64(requested) < budget.MaxSerializedBytes {
			budget.MaxSerializedBytes = int64(requested)
		}
	}
	return budget
}
