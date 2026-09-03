package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4636 stage 3: measure the assembled result, re-synthesize once if it
// does not fit, and if it still does not fit, refuse in a way that says why.
//
// Decision D5, ruled by the orchestrator on 2026-08-30: option C now, option
// A ticketed separately. The design's original promise -- "always serve,
// never acr_rejected_request" -- was over-determined and unsound. It required
// three things that cannot all be true at once: exactly one bounded retry,
// always serve, and keep the existing route gate. Re-synthesis reduces
// members and facts but NOT SubjectResolution.Candidates, which the route
// counts and the contract permits up to 50 -- so on a 30-item budget a valid
// result with 30 candidates cannot fit once it carries a single driver,
// finding, claim or member, however small the synthesis input was.
//
// So this ships the PLANNED, EXPLAINED refusal instead: it names what was too
// large and the axis along which a narrower question would fit. That is
// strictly better than the status quo, which is the same refusal with no
// explanation at all.
//
// CHAOS-4735 corrected HOW it names that axis. The first version returned a
// fixed English sentence per family; it now returns a closed token that the
// family registry declares, because the engine does not author user language.

// ErrAnswerExceedsBudget is the planned refusal. It is a distinct sentinel
// rather than a generic failure so the route can classify it as the DESIGNED
// outcome it is, and so it can never be confused with a serialization defect
// or an upstream error.
var ErrAnswerExceedsBudget = errors.New("context fabric answer exceeds the response budget")

// AnswerBudgetRefusal carries what a caller needs in order to ask a question
// that would fit. It is the whole point of decision D5: today's 413 is
// reachable and unexplained, so an explained one is a strict improvement even
// though it is still a refusal.
type AnswerBudgetRefusal struct {
	// Overrun names which ceiling was exceeded.
	Overrun contractsv1.ContextFabricBudgetOverrun
	// MeasuredItems/MeasuredBytes and the two ceilings are the numbers, so
	// the refusal is diagnosable from itself.
	MeasuredItems      int
	MeasuredBytes      int64
	MaxItems           int
	MaxSerializedBytes int64
	// Family is what was being answered.
	Family QuestionFamily
	// NarrowerContinuationAxis names the structural dimension a caller
	// could reduce, as a CLOSED TOKEN.
	//
	// CHAOS-4735 replaced a `NarrowerQuestion string` here. That field held
	// one of five fixed English sentences chosen by a switch on Family, and
	// the route served it verbatim as error.details.narrower_question --
	// engine-authored user language from a vocabulary table, on a
	// user-facing wire, which chris's rulings of 2026-08-31 13:35 and 13:40
	// ban outright rather than deprecate.
	//
	// The token is the half of that sentence the engine actually knows. It
	// is LOOKED UP from the family registry, never decided here, so this
	// file no longer reads a family constant at all -- see
	// chaos4735_family_language_sweep_test.go, which fails if it starts
	// again.
	//
	// NarrowingContinuationNone means no axis could be named; the route
	// omits the continuation entirely rather than serving "none" as if it
	// were advice.
	NarrowerContinuationAxis NarrowingContinuationAxis
	// RetryAttempted records whether the one bounded re-synthesis ran. It
	// is false when there was nothing left to narrow, and false when the
	// remaining deadline could not safely hold a second model call -- two
	// different situations that a caller and an operator both need told
	// apart.
	RetryAttempted bool
}

func (r AnswerBudgetRefusal) Error() string {
	return fmt.Sprintf("%s: measured %d items and %d bytes against a %d-item, %d-byte budget", ErrAnswerExceedsBudget.Error(), r.MeasuredItems, r.MeasuredBytes, r.MaxItems, r.MaxSerializedBytes)
}

func (r AnswerBudgetRefusal) Unwrap() error { return ErrAnswerExceedsBudget }

// fitAssembledResult is stage 3.
// It returns the held telemetry belonging to the result it ACTUALLY SERVES,
// so the caller emits each per-investigation decision event exactly once. A
// retry runs the assembly twice and discards the first pass's answer, so
// emitting from inside the assembly double-counted every event in it -- see
// assemblyTelemetry.
func (e *Engine) fitAssembledResult(ctx context.Context, principal storage.Principal, plan *AnswerPlan, result InvestigationResult, firstPass assemblyTelemetry, params synthesisAssemblyParams) (InvestigationResult, assemblyTelemetry, error) {
	budget := ResponseBudget{MaxItems: plan.Budget.MaxItems, MaxSerializedBytes: plan.Budget.MaxSerializedBytes}
	if budget.MaxItems <= 0 && budget.MaxSerializedBytes <= 0 {
		// Nothing to measure against. An engine composed without either
		// ceiling behaves exactly as it did before this slice: it plans,
		// but it never narrows on a budget it was not told about.
		return result, firstPass, nil
	}
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		// A result that cannot be marshaled is a server defect, not an
		// over-budget answer. Conflating the two would let a serialization
		// bug present to the caller as "your question was too big".
		return InvestigationResult{}, assemblyTelemetry{}, stageError(StageValidation, fmt.Errorf("measure assembled result: %w", err))
	}
	overrun := measurement.Overrun(budget)
	if overrun == contractsv1.ContextFabricBudgetFits {
		// A FIT is a decision, and this event's own doc comment calls it
		// "one narrowing decision, or one measured fit". Emitting nothing
		// made the fit outcome uncountable, so "how often does an answer fit
		// first time" -- the denominator for every narrowing rate an
		// operator would want -- could not be answered from the artifacts
		// (codex round 1, finding 5).
		fit := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
			cohortMemberCount(params.Graph.Cohort), cohortMemberCount(params.Graph.Cohort),
			params.Graph.Cohort != nil && len(params.Graph.Cohort.Groups) > 0, false,
			contractsv1.ContextFabricBudgetFits, params.GroupedNarrowingBasis)
		fit.MeasuredItems = measurement.Items.Budgeted()
		fit.MeasuredBytes = measurement.Bytes
		// Predicted beside measured on the SAME line, for the cohort synthesis
		// actually ran against. A fit is where the rate is confirmed; a
		// refusal is where it has already failed, so recording it only on
		// refusals would show the rate exclusively when it was wrong.
		fit.PredictedItems = PredictedItemsForPlan(*plan, cohortMemberCount(params.Graph.Cohort))
		fit.DeadlineReserved = e.synthesisDeadlineReserve > 0
		e.recordPlanNarrowing(ctx, principal, fit)
		return result, firstPass, nil
	}

	grouped := params.Graph.Cohort != nil && len(params.Graph.Cohort.Groups) > 0
	narrowed := narrowSynthesisInput(params, plan)
	before, after, canNarrow := narrowed.Before, narrowed.After, narrowed.Narrow
	// Name WHICH of the three reasons declined the retry. They have
	// completely different fixes -- reconfigure the deployment, accept that
	// this answer is genuinely slow, or accept that nothing was left to
	// narrow -- and a single unexplained 413 tells an operator none of them.
	declined := RetryDeclinedNotApplicable
	switch {
	case !canNarrow:
		declined = RetryDeclinedNothingToNarrow
	case e.synthesisDeadlineReserve <= 0:
		declined = RetryDeclinedNoReserve
	case !e.retryDeadlineAvailable(ctx):
		declined = RetryDeclinedInsufficientDeadline
	}
	if declined != RetryDeclinedNotApplicable {
		// CHAOS-4809 PATH 2: BOTH counts, as narrowSynthesisInput actually
		// computed them. This used to pass `before` for both, so a refusal
		// that had already run a real overlap-aware set-cover selection
		// published it as a no-op -- the basis field naming an order and the
		// count pair denying that anything happened, with no way for a
		// reader to tell which to believe.
		return InvestigationResult{}, assemblyTelemetry{}, e.planRefusal(ctx, principal, plan, measurement, overrun, false, grouped, narrowed.Basis, before, after, declined)
	}

	e.recordPlanNarrowingStep(plan, PlanNarrowing{
		Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Basis:   planStageBasis(contractsv1.ContextFabricPlanNarrowingAssembledResult, grouped, narrowed.Basis),
		Before:  before,
		After:   after,
		Groups:  false,
		Overrun: overrun,
	})

	retryParams := params.forRetry(narrowed.Graph, narrowed.Facts)
	// The re-rank's citations MUST travel with the re-ranked cohort:
	// narrateCohortDriverJudgments resolves them per member, so citations
	// computed against the wider member set would narrate against members the
	// retry no longer carries.
	retryParams.CohortSignalCitations = narrowed.Citations
	retried, retryPending, retryErr := e.synthesizeAndAssemble(ctx, principal, retryParams)
	if retryErr != nil {
		// PROPAGATE the retry's own error. An earlier revision discarded it
		// and returned a budget refusal, so a transient ErrModelUnavailable
		// on pass two reached the caller as a 413 saying "ask a narrower
		// question" -- a deterministic, non-retryable answer to a
		// transient, retryable fault, with no telemetry naming what actually
		// happened (codex round 2, finding 2). That is the silent-failure
		// class this repository's own conventions forbid: the caller acts on
		// the diagnosis they are given, and rewording their question would
		// not have helped.
		//
		// The over-budget measurement is not lost -- it is recorded on the
		// event below -- but the ERROR the caller sees is the one that
		// actually stopped the answer.
		// CHAOS-4809 PATH 3: `before, after`, not `before, before`. This is
		// the path where the narrowed cohort was actually USED -- synthesis
		// ran against the selected members and then the model call failed --
		// so it was the one path publishing a selection as a no-op while a
		// real answer had genuinely been attempted over the narrowed set.
		event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, overrun, narrowed.Basis)
		event.MeasuredItems = measurement.Items.Budgeted()
		event.MeasuredBytes = measurement.Bytes
		// The measurement here is the FIRST synthesis's, taken against the
		// pre-narrowing cohort, so the prediction pairs with `before`.
		event.PredictedItems = PredictedItemsForPlan(*plan, before)
		event.RetryAttempted = true
		event.RetryFit = false
		event.RetryFailed = true
		event.DeadlineReserved = e.synthesisDeadlineReserve > 0
		e.recordPlanNarrowing(ctx, principal, event)
		// CHAOS-4726 codex round 1: the FIRST synthesis call's rejection is
		// caught at the Investigate call site (engine.go), but this retry
		// call is a SECOND, separate invocation that can itself be rejected
		// -- and plan.Narrowing already carries the assembled_result step
		// recorded just above, so this is its own accurate "state right
		// before this synthesis call" snapshot, not a stale reuse of the
		// first call's.
		return InvestigationResult{}, assemblyTelemetry{}, withSynthesisNarrowingSnapshot(retryErr, *plan)
	}
	// Finalize the retry too, or the second pass repeats round 1 finding 1's
	// defect: measuring a pre-final shape and serving a larger one.
	retried = e.finalizeResult(retried, *plan, params.Frame)
	retryMeasurement, err := contractsv1.MeasureContextFabricResponse(retried)
	if err != nil {
		return InvestigationResult{}, assemblyTelemetry{}, stageError(StageValidation, fmt.Errorf("measure re-synthesized result: %w", err))
	}
	retryOverrun := retryMeasurement.Overrun(budget)
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, overrun, narrowed.Basis)
	event.MeasuredItems = retryMeasurement.Items.Budgeted()
	event.MeasuredBytes = retryMeasurement.Bytes
	// `after`, not `before`: this event measures the RE-synthesized answer,
	// which ran against the narrowed cohort. Predicting from `before` would
	// pair a measurement of one cohort with an expectation for a larger one.
	event.PredictedItems = PredictedItemsForPlan(*plan, after)
	event.RetryAttempted = true
	event.RetryFit = retryOverrun == contractsv1.ContextFabricBudgetFits
	event.DeadlineReserved = e.synthesisDeadlineReserve > 0
	event.RefusalPlanned = !event.RetryFit
	if event.RefusalPlanned {
		// Only when a refusal is actually planned. Recording the axis on a
		// fitting retry would make the counter say the caller was given
		// advice they never received.
		event.NarrowerContinuationAxis = narrowerContinuationAxisFor(*plan)
	}
	e.recordPlanNarrowing(ctx, principal, event)

	if retryOverrun != contractsv1.ContextFabricBudgetFits {
		// ONE bounded retry, never k of them. Decision D5 rejected further
		// retries explicitly: they inherit the same deadline problem and
		// merely move the terminal case, arriving at the same unanswered
		// question with more latency.
		return InvestigationResult{}, assemblyTelemetry{}, e.refusalFrom(plan, retryMeasurement, retryOverrun, true)
	}
	// The SERVED answer is the retry's, so the ranking event that describes it
	// is the retry's too. Emitting the first pass's would report a ranking
	// computed over members the caller never received -- the same
	// "telemetry describes a different artifact than the one served" class the
	// deferred emitters fix.
	retryRanked := narrowed.Ranked
	retryPending.CohortRanked = &retryRanked
	return retried, retryPending, nil
}

// cohortMemberCount is nil-safe: a fitting answer with no cohort reports zero
// rather than being unreportable.
func cohortMemberCount(cohort *Cohort) int {
	if cohort == nil {
		return 0
	}
	return len(cohort.Members)
}

// narrowSynthesisInput halves the cohort the retry is given, member-first.
//
// HALVES, rather than aiming at a computed target. The relationship between
// members and the items synthesis will produce from them is not a function
// this code knows -- drivers-per-member is decided by synthesis -- so a
// target derived from the overrun would be a guess dressed as arithmetic.
// Halving is a bounded, declared reduction that makes progress without
// pretending to a precision it does not have. §6.3 says so in as many words:
// "the exact clamp is not derivable on paper".
// narrowedInput is what a narrowing produced. It is a STRUCT because the
// previous signature returned five positional values and the re-rank's
// citations were assigned back onto `params` -- a VALUE parameter, so the
// assignment was silently discarded and the retry ran with citations computed
// against the PRE-narrowing member set. Found by the systematic sweep, not by
// review: the line looked correct and did nothing.
type narrowedInput struct {
	Graph     GraphContext
	Facts     CanonicalFactBundle
	Citations cohortMemberSignalCitations
	// Ranked is the re-rank's own event. RankCohort normalizes within the
	// cohort, so a narrowed cohort's ranking is genuinely different -- and
	// the event describing the SERVED answer must be that one, not the
	// pre-narrowing one the engine already holds.
	Ranked CohortRankedEvent
	Before int
	After  int
	Narrow bool
	// Basis is which grouped order NarrowGroupedCohort actually ran
	// (CHAOS-4678), zero for a flat cohort or when nothing narrowed.
	Basis contractsv1.ContextFabricNarrowingBasis
}

func narrowSynthesisInput(params synthesisAssemblyParams, plan *AnswerPlan) narrowedInput {
	graph := params.Graph
	facts := params.Facts
	if graph.Cohort == nil || len(graph.Cohort.Members) <= 1 {
		// Nothing left to narrow. A cohort of one is the smallest answer
		// that is still an answer to the question asked; zero members is a
		// different question.
		//
		// Before/After carry the REAL count, not zero. Nothing narrowed, so
		// they are equal -- but they still describe a cohort that exists and
		// that synthesis measured an answer against. Returning the zero value
		// published `before:0, after:0` on every singleton refusal, and any
		// per-member quantity derived from that count inherits the error.
		trivial := cohortMemberCount(graph.Cohort)
		return narrowedInput{Graph: graph, Facts: facts, Before: trivial, After: trivial}
	}
	before := len(graph.Cohort.Members)
	target := before / 2
	if target < 1 {
		target = 1
	}
	cohort := copyCohortForRetry(graph.Cohort)
	var kept []CohortMember
	var narrowed bool
	var basis contractsv1.ContextFabricNarrowingBasis
	if len(cohort.Groups) > 0 {
		var groups []contractsv1.ContextFabricCohortGroup
		kept, groups, narrowed, basis = NarrowGroupedCohort(cohort, target)
		if narrowed {
			cohort.Groups = groups
		}
	} else {
		kept, narrowed = NarrowFlatCohort(cohort, target)
	}
	if !narrowed {
		// A grouped cohort in which every group already holds exactly one
		// member cannot narrow further without DROPPING a group, which
		// decision D2 forbids: "for each team" is the question's own words.
		// The planned refusal is the correct terminal case here, not a
		// silent group drop.
		//
		// Basis travels even here (codex round 1, finding 4): the grouped
		// selection still RAN and still has a real basis to report even
		// though it produced no change -- dropping it here made the
		// refusal telemetry fall back to a stale default that named an
		// order that did not execute.
		return narrowedInput{Graph: graph, Facts: facts, Before: before, After: before, Basis: basis}
	}
	removed := RemovedCohortMembers(cohort.Members, kept)
	cohort.Members = kept
	if len(cohort.Groups) > 0 {
		ApplyGroupedCohortCompleteness(cohort)
	} else {
		cohort.Complete = false
		cohort.Truncated = true
	}
	// Re-rank: RankCohort min-max normalizes workload WITHIN the cohort, so
	// scores computed against the wider member set do not describe this one.
	rankedCohort, rankEvent, citations := RankCohort(cohort, facts.Facts, facts.Coverage)
	graph.Cohort = rankedCohort
	facts.Facts = RetainFactsForCohort(facts.Facts, rankedCohort, removed)
	return narrowedInput{
		Graph: graph, Facts: facts, Citations: citations, Ranked: rankEvent,
		Before: before, After: len(kept), Narrow: true, Basis: basis,
	}
}

// retryDeadlineAvailable reports whether enough of the request deadline
// remains to run a second synthesis.
//
// This is decision D5's second, independent hole, and closing it is
// non-negotiable under ANY of the three rulings: the whole request shares one
// timeout (internal/api/app.go's timeoutMiddleware) and the pre-read budget
// bounds only fan-out, so a first synthesis that finishes near the deadline
// turns the retry into a 504 rather than a partial answer.
//
// A context with NO deadline always admits the retry -- there is nothing to
// run out of. A reserve of zero always REFUSES it: an engine that was not
// told how long it may spend does not gamble the caller's deadline on a
// second model call.
func (e *Engine) retryDeadlineAvailable(ctx context.Context) bool {
	if e.synthesisDeadlineReserve <= 0 {
		return false
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return true
	}
	return time.Until(deadline) >= e.synthesisDeadlineReserve
}

// planRefusal emits the telemetry and builds the refusal.
//
// members is the cohort the refusal was given, and selected is what the
// grouped selection actually admitted out of it. They were ONE parameter
// until CHAOS-4809, emitted as (members, members), which is where this path
// lost the selection's effect: narrowSynthesisInput has already run by the
// time this is called, so on a declined retry a real set-cover selection had
// computed a genuinely smaller cohort and the event reported it as a no-op.
// Two parameters make that state unrepresentable, the same way
// PlanNarrowingEventFrom's groupAxis/groupsNarrowed split does.
//
// They are EQUAL, legitimately, when the retry was declined because there
// was nothing left to narrow -- and that case must stay equal. Publishing a
// narrowing that did not happen is the same defect as suppressing one that
// did.
//
// Whether the selected cohort was ever SERVED is a separate fact and is not
// folded in here: RetryAttempted already carries it. On this path it is
// false -- the selection was computed and then discarded -- and a reader
// needs both halves, because "we would have narrowed to four" and "we
// answered over four" are different statements about the run.
func (e *Engine) planRefusal(ctx context.Context, principal storage.Principal, plan *AnswerPlan, measurement ResponseMeasurement, overrun contractsv1.ContextFabricBudgetOverrun, retryAttempted, grouped bool, basis contractsv1.ContextFabricNarrowingBasis, members, selected int, declined RetryDeclinedReason) error {
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, members, selected, grouped, false, overrun, basis)
	event.MeasuredItems = measurement.Items.Budgeted()
	event.MeasuredBytes = measurement.Bytes
	// `members` is the cohort synthesis ran against, which is the count the
	// measurement describes -- NOT `selected`, which is what the declined
	// retry would have narrowed to. Predicting from `selected` would publish a
	// number for an answer that was never assembled.
	event.PredictedItems = PredictedItemsForPlan(*plan, members)
	event.RetryAttempted = retryAttempted
	event.RetryFit = false
	event.RefusalPlanned = true
	event.DeadlineReserved = e.synthesisDeadlineReserve > 0
	event.RetryDeclined = declined
	event.NarrowerContinuationAxis = narrowerContinuationAxisFor(*plan)
	e.recordPlanNarrowing(ctx, principal, event)
	return e.refusalFrom(plan, measurement, overrun, retryAttempted)
}

func (e *Engine) refusalFrom(plan *AnswerPlan, measurement ResponseMeasurement, overrun contractsv1.ContextFabricBudgetOverrun, retryAttempted bool) error {
	return AnswerBudgetRefusal{
		Overrun:                  overrun,
		MeasuredItems:            measurement.Items.Budgeted(),
		MeasuredBytes:            measurement.Bytes,
		MaxItems:                 plan.Budget.MaxItems,
		MaxSerializedBytes:       plan.Budget.MaxSerializedBytes,
		Family:                   plan.Family,
		NarrowerContinuationAxis: narrowerContinuationAxisFor(*plan),
		RetryAttempted:           retryAttempted,
	}
}

// narrowerContinuationAxisFor LOOKS UP the family's declared narrowing axis.
//
// CHAOS-4735 replaced `narrowerQuestionFor` -- a switch on plan.Family
// returning one of five fixed English sentences -- with this lookup. Read the
// difference carefully, because it is the whole point of the ticket and not a
// rename: there is no switch and no family constant here. The axis is a
// COLUMN on the family registry row (chaos4632_question_family_registry.go),
// so adding a family means declaring its axis in the table the registry test
// already enumerates, rather than adding an arm to a function nobody re-reads.
//
// A switch returning a closed token instead of a sentence would have passed
// the letter of the ticket and failed its purpose: it is one edit away from
// being the phrase table again, and it would still be a fifth family-read
// site outside the closed four-purpose list (design §13.4.3). The sweep in
// chaos4735_family_language_sweep_test.go enforces both halves.
//
// An unknown family -- which SanitizeQuestionFamily and plan construction should
// already have turned into `unclassified`, but this function does not get to
// assume -- yields `none`, and the route then omits the continuation
// altogether. Failing to `none` rather than to some default axis matters:
// naming the wrong axis is worse advice than naming none, because the caller
// acts on it.
func narrowerContinuationAxisFor(plan AnswerPlan) NarrowingContinuationAxis {
	definition, found := LookupQuestionFamily(plan.Family)
	if !found {
		return NarrowingContinuationNone
	}
	return definition.NarrowerContinuationAxis
}
