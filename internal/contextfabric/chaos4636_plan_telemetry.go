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
	// BasisObserved says whether Basis is an order the selection REPORTED
	// or one planStageBasis defaulted to on the caller's behalf.
	//
	// CHAOS-4809. planStageBasis returns largest_group_round_robin for ANY
	// grouped axis whose caller ran no selection -- a measured fit, for
	// instance -- and canonical_id_lexical for every flat one. Both are
	// declared defaults, and neither is distinguishable on the wire from an
	// order a selection actually chose members by. So a reader who saw
	// `basis: overlap_aware_set_cover` had to ASSUME a set-cover selection
	// ran, and that ticket is precisely a demonstration that the assumption
	// is unsafe: it is the field that stayed truthful while before/after
	// went false, and a reader had no way to know which to trust.
	//
	// This encodes the PROVENANCE of the value rather than the value again.
	// It is DERIVED in PlanNarrowingEventFrom from the same inputs
	// planStageBasis uses, never passed, so a caller cannot claim an
	// observation its stage did not make.
	BasisObserved bool
	// Before and After are counts of what was narrowed.
	Before int
	After  int
	// Groups reports whether GROUPS were narrowed rather than members.
	// Decision D2 ruled member-first, so this being true is the rare case
	// and worth being able to count.
	//
	// It is NOT "the selection used a group axis" -- that is what Basis
	// reports, and conflating the two into one input made a grouped
	// member-narrowing either mislabel its order or falsely increment this
	// counter. See PlanNarrowingEventFrom's two separate parameters.
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
	// PredictedItems is what the plan EXPECTED this cohort to cost: one item
	// per cohort member plus the SynthesisHeadroom the profile reserved for
	// what synthesis adds. It is the plan's OWN arithmetic, beside what stage 3
	// actually measured, and the pair is the point -- the plan's expectation is
	// the thing that turned out to be wrong on the rig, and an operator can
	// only see that by reading predicted against measured on one line.
	//
	// NOT a per-member rate. An earlier revision predicted from a measured
	// items-per-member ratio and clamped the cohort with it; the rig showed the
	// ratio RISES as the cohort shrinks, so totals are largely insensitive to
	// member count and the rate was not a property of the system. This comment
	// described that refuted model for two commits after the code stopped
	// implementing it (codex round 3) -- if the arithmetic changes again, this
	// sentence changes with it.
	//
	// Zero only when there is no cohort to predict for. An absent prediction is
	// honest; a guessed one would be a number that looks like evidence.
	PredictedItems int
	// RetryAttempted and RetryFit are the re-synthesis outcome. Both false
	// outside stage 3.
	RetryAttempted bool
	RetryFit       bool
	// RetryFailed distinguishes "the retry ran and its answer still did not
	// fit" from "the retry could not run at all because synthesis errored".
	// Without it both read as RetryFit=false, and they are different
	// diagnoses: the first is a genuinely oversized answer, the second is an
	// upstream fault that says nothing about the question's size.
	RetryFailed bool
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
	// NarrowerContinuationAxis is the closed token the refusal offered the
	// caller, empty when no refusal was planned.
	//
	// CHAOS-4735 criterion 6. The refusal telemetry kept question_family and
	// the overrun numbers but said nothing about the continuation, so "what
	// did we actually tell callers to do, and does that advice track the
	// families that refuse most" was unanswerable from the artifacts -- and
	// the field it would have had to record was free English, which is not a
	// telemetry value at all. A closed token is countable: it can be a
	// dimension on the refusal counter without becoming high-cardinality
	// text, which is the same rule this repository applies to every other
	// decision-basis field.
	NarrowerContinuationAxis NarrowingContinuationAxis
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

// PlanNarrowingEventFrom builds the event.
//
// groupAxis and groupsNarrowed are SEPARATE parameters because they are
// separate facts, and one boolean serving both made a wrong state
// representable. "The selection used the group axis" decides which ORDER is
// reported; "groups were narrowed rather than members" is decision D2's own
// counter, which must stay rare. A grouped cohort whose MEMBERS were trimmed
// is groupAxis=true, groupsNarrowed=false -- the ordinary D2 case, and the one
// the single boolean could not express. Setting that boolean true to fix the
// basis would have corrupted the D2 counter; leaving it false reported an
// order that was not used. Two parameters make the bad state unrepresentable.
func PlanNarrowingEventFrom(plan AnswerPlan, stage contractsv1.ContextFabricPlanNarrowingStage, before, after int, groupAxis, groupsNarrowed bool, overrun contractsv1.ContextFabricBudgetOverrun, groupedBasis contractsv1.ContextFabricNarrowingBasis) PlanNarrowingEvent {
	return PlanNarrowingEvent{
		Family:        plan.Family,
		FamilyVersion: plan.FamilyVersion,
		Stage:         stage,
		Basis:         planStageBasis(stage, groupAxis, groupedBasis),
		// Exactly the condition under which planStageBasis returns the
		// caller's own value rather than one of its two declared defaults.
		// Derived from the same two inputs, in the same file, so the two
		// cannot drift apart into a claim of observation over a default.
		BasisObserved:      groupAxis && contractsv1.ValidContextFabricNarrowingBasis(groupedBasis),
		Before:             before,
		After:              after,
		Groups:             groupsNarrowed,
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
// groupAxis says whether the selection ACTUALLY ran over a group axis, not
// merely whether the stage runs late enough for one to exist. groupedBasis is
// which grouped order NarrowGroupedCohort actually ran (CHAOS-4678:
// overlap_aware_set_cover or, beyond the guard, largest_group_round_robin);
// it is ignored when groupAxis is false, and a caller that has no narrowing
// to report (e.g. a measured FIT, where nothing ran) passes the zero value,
// which falls back to largest_group_round_robin -- the same basis this
// function always named for a grouped axis before CHAOS-4678.
//
// Both directions of getting this wrong have now been observed live and are
// pinned: reporting largest_group_round_robin for a FLAT cohort names an order
// with no groups to round-robin over, and reporting canonical_id_lexical for a
// GROUPED narrowing names an order that was not used. The caller must pass
// what the selection actually did.
func planStageBasis(stage contractsv1.ContextFabricPlanNarrowingStage, groupAxis bool, groupedBasis contractsv1.ContextFabricNarrowingBasis) contractsv1.ContextFabricNarrowingBasis {
	if groupAxis {
		if contractsv1.ValidContextFabricNarrowingBasis(groupedBasis) {
			return groupedBasis
		}
		// Round-robin across groups, largest first, so EVERY group
		// survives: decision D2, member-first, ruled 2026-08-30. The
		// pre-CHAOS-4678 default for a caller that ran no selection
		// (nothing to narrow) and so has no actual basis to report.
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
	// RecordGroupedCohortCompleteness (CHAOS-4733) reports the outcome of
	// folding a grouped cohort's completeness at BuildCohortGroups +
	// ApplyGroupedCohortCompleteness time: whether the pre-grouping cohort
	// was itself truncated at discovery, and how many of the groups built
	// from it came out marked incomplete. Before this fix, a served
	// Complete=true grouped cohort over a capped discovery was invisible in
	// every artifact; this is what makes it countable. Closed
	// enums/counts only, fired once per grouped answer.
	RecordGroupedCohortCompleteness(ctx context.Context, principal storage.Principal, event GroupedCohortCompletenessEvent)
	// RecordBudgetAssertion reports the FINAL budget assertion at one fresh
	// result exit -- the measurement taken on the document the route will
	// actually serialize, after every composer. It fires on a FIT as well as
	// an overrun, because an event that only fired on failure would leave
	// "how often does the final document fit" unanswerable from the
	// artifacts; that is the same finding a prior round made against the
	// stage-3 fit, applied to this event before it can be made again.
	//
	// Embedded here rather than offered as an optional interface, for the
	// CHAOS-4085 reason PlanTelemetry's own doc comment gives: a telemetry
	// implementation that cannot report this must be a COMPILE ERROR, never
	// a silently empty stream.
	RecordBudgetAssertion(ctx context.Context, principal storage.Principal, event BudgetAssertionEvent)
}

// GroupedCohortCompletenessEvent (CHAOS-4733) is CLOSED ENUMS AND COUNTS
// ONLY -- no question text, no subject identifier, no group label -- the
// same discipline PlanNarrowingEvent holds, for the same reason.
type GroupedCohortCompletenessEvent struct {
	// Family identifies which plan grouped, so a regression can be
	// attributed to a family-table row.
	Family QuestionFamily
	// PreGroupingComplete/PreGroupingTruncated are the cohort's own
	// discovery-level flags AS THEY STOOD before BuildCohortGroups ran --
	// what DiscoveredCohort (or a truncated census) set. This is the
	// signal that used to have no surviving representation once grouped.
	PreGroupingComplete  bool
	PreGroupingTruncated bool
	// GroupCount is how many groups BuildCohortGroups produced.
	GroupCount int
	// GroupsMarkedIncomplete counts groups whose own Complete came out
	// false -- under this fix, that is every group whenever
	// PreGroupingComplete is false, so the count is mostly diagnostic of
	// group count rather than of anything the groups decided on their own,
	// but it is what makes "how many groups a truncated discovery actually
	// touched" countable without re-deriving it from PreGroupingComplete
	// and GroupCount at query time.
	GroupsMarkedIncomplete int
	// Refusal names why grouping built NOTHING, when it refused. Empty on
	// every ordinary emission. Emitting the refusal on this same event,
	// rather than minting a second one, keeps "what happened to the group
	// axis this request planned" answerable from one line: group_count 0
	// with a refusal is a refusal, and group_count 0 without one is the
	// pre-existing nothing-could-be-placed degrade.
	Refusal CohortGroupingRefusal
	// PlannedGroupKind is the kind the PLAN asked for, carried only with a
	// refusal so a reader can see the disagreement without joining lines. It
	// is a closed-vocabulary subject kind, never model text.
	PlannedGroupKind SubjectKind
	// Complete/Truncated are the FINAL cohort-level flags after
	// ApplyGroupedCohortCompleteness folded the pre-grouping state and the
	// groups together.
	Complete  bool
	Truncated bool
}

// recordPlanNarrowing is the engine's nil-safe emitter.
func (e *Engine) recordPlanNarrowing(ctx context.Context, principal storage.Principal, event PlanNarrowingEvent) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordPlanNarrowing(ctx, principal, event)
}

// recordGroupedCohortCompleteness is the engine's nil-safe emitter for
// GroupedCohortCompletenessEvent.
func (e *Engine) recordGroupedCohortCompleteness(ctx context.Context, principal storage.Principal, event GroupedCohortCompletenessEvent) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordGroupedCohortCompleteness(ctx, principal, event)
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
