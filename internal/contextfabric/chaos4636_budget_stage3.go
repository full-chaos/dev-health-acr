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
// CHAOS-6558 (chris, 2026-09-24, option b) added the BYTE-axis lever. The
// original design gave a byte overrun no reduction of its own ("the planned
// refusal stands by design"): only the cohort retry, which keeps every
// retained fact's row tables and so could not reduce them. Now the order on a
// byte overrun is: (1) cut claimed-fact rows to the largest per-table cap that
// fits, serve PARTIAL with the cut disclosed (fact_row_truncation.go);
// (2) only if that cannot fit, the cohort retry -- and NOT when the rows alone
// overran and the narrowed input keeps every fact, because that retry cannot
// reduce the axis (RetryDeclinedCannotReduceAxis); the refusal then advises a
// shorter evidence window; (3) the retry's document gets the row lever too.
// max_serialized_bytes is unchanged by that ruling.
//
// CHAOS-6743 added the ITEM-axis allocation rule, members before claims: on an
// item overrun over a cohort, per-member claims are cut to the largest
// per-member cap that fits (claim_depth_narrowing.go) before the cohort retry
// halves the members; the retried document gets that lever too.
//
// CHAOS-6558 (second byte-axis lever): when the row lever cannot fit because
// the non-row bytes alone overrun, relationship paths are dropped in a defined
// order (path_drop.go) before any cohort retry. A byte 413 is left only when
// the minimum answer -- every droppable path gone, every table at one row --
// still exceeds the budget.
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
	// NothingLeftToCut is true when the answer, measured as it is sent,
	// overran the byte ceiling and no row table held more than one row to cut:
	// the refusal is final for this answer.
	NothingLeftToCut bool
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
	// Sections names what the refused document was made of: every top-level
	// section's bytes, largest first, so the refusal log can say what filled
	// the answer. Stage is where the refusal was raised.
	Sections []contractsv1.ContextFabricSectionBytes
	Stage    InvestigationStage
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
// fitAssembledResult takes `consumed` -- the allocation the FIRST pass actually
// spent, as RETURNED by synthesizeAndAssemble -- rather than reading
// params.Allocation. Same invariant as the retry below: the guard measures what
// the producer returned, never the caller's own copy of what it was handed.
// cardinality is the FIRST pass's count, carried in because this stage can
// re-finalize the first pass's document (a candidate narrowing) without
// re-synthesizing it -- and a document that was not re-synthesized has the
// member set, and therefore the count, it already had. The retry path below
// computes its own, from its own pass.
func (e *Engine) fitAssembledResult(ctx context.Context, principal storage.Principal, plan *AnswerPlan, result InvestigationResult, consumed ItemAllocation, firstPass assemblyTelemetry, params synthesisAssemblyParams, cardinality MembershipCardinality) (InvestigationResult, assemblyTelemetry, error) {
	budget := ResponseBudget{MaxItems: plan.Budget.MaxItems, MaxSerializedBytes: plan.Budget.MaxSerializedBytes}
	if budget.MaxItems <= 0 && budget.MaxSerializedBytes <= 0 {
		// Nothing to measure against. An engine composed without either
		// ceiling behaves exactly as it did before this slice: it plans,
		// but it never narrows on a budget it was not told about.
		return result, firstPass, nil
	}
	// The ONE allocator, derived HERE from the plan every other spender
	// reads. The member ROWS are charged to it: they are counted by
	// CountContextFabricResultItems and debited by the ledger, so an
	// allocator that did not commit capacity for them would publish grants
	// that, fully respected, still overrun the ceiling by exactly the number
	// of rows -- which is the defect three rounds found in three shapes.
	// THE ALLOCATION SYNTHESIS RETURNED AS CONSUMED -- not params.Allocation,
	// and not a second derivation from the same inputs.
	//
	// This line was its own `AllocateItems` call once (keystone #3), then
	// `params.Allocation` (keystone #4's fix). Both were still the caller's own
	// object: `params` is BY VALUE, so a producer-local fault could never reach
	// this guard, which is how narration came to spend a re-copied local that
	// nothing validated (keystone #5). It now reads what the producer returned.
	allocation := consumed
	// measurementInput is a LOCAL COPY, measured here and nowhere else --
	// `result` itself, which this function returns and the caller carries
	// forward to finalizeServed, is never mutated by it. A work-item census
	// result may still be corrected by the completeness authority before it
	// is served, and this stage's fit-or-retry decision must weigh the bytes
	// that correction will actually add or remove; but finalizeServed is the
	// one place that correction is APPLIED to the served result and its own
	// observation DERIVED, and it must always derive from the model's own,
	// uncorrected status -- never from a status this stage already
	// rewrote. Measuring a copy, never the value itself, is what keeps both
	// true at once. InvestigationResult is passed by value throughout this
	// engine (measureAssembledAttempt included), so a derived copy here
	// costs nothing structurally and mutates nothing the caller holds.
	measurementInput := result
	if params.WorkItemCensus != nil {
		measurementInput = ApplyServerCompletenessAuthority(ApplyServedStatusFloors(result), e.serverCompletenessAuthorityEnabled, e.serverCompletenessAuthoritySymmetricEnabled, DeriveCompletenessAuthority(ApplyServedStatusFloors(result)))
	}
	measured, err := e.measureAssembledAttempt(ctx, principal, "assembled_result", allocation, measurementInput, budget)
	if err != nil {
		// A result that cannot be marshaled is a server defect, not an
		// over-budget answer. Conflating the two would let a serialization
		// bug present to the caller as "your question was too big". An
		// account that does not reconcile is the SAME kind of defect and
		// takes the same exit -- never the budget refusal.
		return InvestigationResult{}, firstPass, err
	}
	// THE CENSUS GIVES WAY FIRST. The composition-time row ceiling reserves
	// room for the smallest valid answer only, so at a small budget an answer
	// larger than that can overrun on the census's own bytes. The census is
	// diagnostics, not evidence: before any answer lever runs, its least
	// relevant rows are cut by the bytes of the overrun and the cut is
	// counted in fact_scope_census_omitted.
	// The test is the BYTES, not the overrun label: when items and bytes are
	// both exceeded the items axis takes the label, and an answer that is
	// also over on bytes still needs the census to give way.
	if budget.MaxSerializedBytes > 0 && measured.Measurement.Bytes > budget.MaxSerializedBytes {
		if shed, ok := shedFactScopeCensusToBytes(result, measured.Measurement.Bytes-budget.MaxSerializedBytes); ok {
			shedInput := shed
			if params.WorkItemCensus != nil {
				shedInput = ApplyServerCompletenessAuthority(ApplyServedStatusFloors(shed), e.serverCompletenessAuthorityEnabled, e.serverCompletenessAuthoritySymmetricEnabled, DeriveCompletenessAuthority(ApplyServedStatusFloors(shed)))
			}
			remeasured, remeasureErr := e.measureAssembledAttempt(ctx, principal, "assembled_result", allocation, shedInput, budget)
			if remeasureErr != nil {
				return InvestigationResult{}, firstPass, remeasureErr
			}
			result, measured = shed, remeasured
		}
	}
	overrun := measured.Overrun
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
		// The FIT arm measures its quota too, and that is the whole of
		// class B's fix. Exposure used to be computed only inside the
		// narrowing path, so on every fitting answer -- the majority path
		// -- the quota fields were zero because nothing had produced them,
		// not because nothing was over.
		fit.recordMeasurement(measured)
		// Predicted beside measured on the SAME line, for the cohort synthesis
		// actually ran against. A fit is where the rate is confirmed; a
		// refusal is where it has already failed, so recording it only on
		// refusals would show the rate exclusively when it was wrong.
		fit.PredictedItems = PredictedItemsForPlan(*plan, cohortMemberCount(params.Graph.Cohort))
		fit.DeadlineReserved = e.synthesisDeadlineReserve > 0
		// SERVED AS-IS: `result` is exactly what this call returns below, so
		// its own already-computed Completeness.State is what the served
		// document will carry -- never re-derived here, for the same
		// one-authority reason recordCandidateNarrowing reads
		// attempt.Result.Completeness.State rather than recomputing it.
		fit.OutcomeCompletenessState = result.Completeness.State
		e.recordPlanNarrowing(ctx, principal, fit)
		return result, firstPass, nil
	}

	grouped := params.Graph.Cohort != nil && len(params.Graph.Cohort.Groups) > 0
	// CHAOS-6558 LEVER ORDER, ruled by chris 2026-09-24 (option b): on a BYTE
	// overrun, cut claimed-fact rows FIRST -- before any cohort cut -- and
	// serve the answer partial, with the cut disclosed. Before this, a byte
	// overrun had exactly one lever, the cohort retry below, which keeps every
	// retained fact's rows and so could not reduce what overran: prod ran two
	// syntheses and refused (86,666 -> 122,402 bytes against 65,536).
	//
	// A census-bearing tuple is left to its own arms: its payload shape is
	// pinned by the census, and the lever is not taught it.
	// serveRows runs the fact-row truncation lever on doc (the assembled
	// result, or the document an items cut left over the byte ceiling) and,
	// when it serves, records the one decision event, which describes ONE
	// document: the assembled result this stage measured and its axis, exactly
	// as the refusal arms do. What the lever did to it -- after-bytes, rows
	// before/after -- is on the fact-row truncation line.
	serveRows := func(doc InvestigationResult, docMeasured MeasuredAttempt, reason RetryDeclinedReason) (factRowTruncationAttempt, bool, error) {
		lever, rowErr := e.planFactRowTruncation(ctx, principal, plan, params.Frame, doc, budget, docMeasured, params.Facts, &firstPass, answerPassSecond, cardinality)
		if rowErr != nil {
			return lever, false, rowErr
		}
		e.recordFactRowTruncation(ctx, principal, plan, answerPassFirst, docMeasured, lever, budget)
		if !lever.Served {
			return lever, false, nil
		}
		members := cohortMemberCount(params.Graph.Cohort)
		event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
			members, members, grouped, false, overrun, params.GroupedNarrowingBasis)
		event.recordMeasurement(measured)
		event.PredictedItems = PredictedItemsForPlan(*plan, members)
		event.DeadlineReserved = e.synthesisDeadlineReserve > 0
		event.RetryDeclined = reason
		event.OutcomeCompletenessState = lever.Result.Completeness.State
		e.recordPlanNarrowing(ctx, principal, event)
		return lever, true, nil
	}
	// servePathDrop runs the path-drop lever on doc (the assembled result, or
	// the document an items cut left over the byte ceiling) and, when it
	// serves, records the one decision event, which describes the ASSEMBLED
	// result this stage measured.
	servePathDrop := func(doc InvestigationResult, docMeasured MeasuredAttempt, reason RetryDeclinedReason) (InvestigationResult, bool, error) {
		pathLever, pathErr := e.planPathDrop(ctx, principal, plan, params.Frame, doc, budget, docMeasured, params.Facts, &firstPass, answerPassSecond, cardinality)
		if pathErr != nil {
			return InvestigationResult{}, false, pathErr
		}
		e.recordPathDrop(ctx, principal, plan, answerPassFirst, docMeasured, pathLever, budget)
		if !pathLever.Served {
			return InvestigationResult{}, false, nil
		}
		members := cohortMemberCount(params.Graph.Cohort)
		event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
			members, members, grouped, false, overrun, params.GroupedNarrowingBasis)
		event.recordMeasurement(measured)
		event.PredictedItems = PredictedItemsForPlan(*plan, members)
		event.DeadlineReserved = e.synthesisDeadlineReserve > 0
		event.RetryDeclined = reason
		event.OutcomeCompletenessState = pathLever.Result.Completeness.State
		e.recordPlanNarrowing(ctx, principal, event)
		return pathLever.Result, true, nil
	}
	var rowLever factRowTruncationAttempt
	if params.WorkItemCensus == nil {
		var rowErr error
		var rowServed bool
		rowLever, rowServed, rowErr = serveRows(result, measured, RetryDeclinedNotApplicable)
		if rowErr != nil {
			return InvestigationResult{}, firstPass, rowErr
		}
		if rowServed {
			return rowLever.Result, firstPass, nil
		}
		// CHAOS-6558 second lever: the rows could not be cut far enough,
		// because the NON-row bytes alone overran (prod: 68,244 of 79,376).
		// Drop relationship paths in their defined order before any cohort
		// retry; a 413 is left only for a budget below the minimum answer.
		if overrun == contractsv1.ContextFabricBudgetOverrunBytes {
			served, ok, pathErr := servePathDrop(result, measured, RetryDeclinedNotApplicable)
			if pathErr != nil {
				return InvestigationResult{}, firstPass, pathErr
			}
			if ok {
				return served, firstPass, nil
			}
		}
	}
	// A work-item walk list over the byte ceiling is cut by its own lever: the
	// trailing members nothing cites are dropped until the document fits, and
	// the N of M sentence is restated with the size limit as its cause. The
	// census-bearing tuple is left out of the row and path levers above; this
	// is the one lever it has on bytes, and a smaller true answer is served
	// instead of a refusal.
	if overrun == contractsv1.ContextFabricBudgetOverrunBytes && params.WorkItemCensus != nil {
		fitted, ok, fitErr := e.fitWalkListToBytes(ctx, principal, plan, params.Frame, result, budget, measured, params.Facts, &firstPass, cardinality)
		if fitErr != nil {
			return InvestigationResult{}, firstPass, fitErr
		}
		if ok {
			// The decision line carries the listed counts (before and after the
			// cut); the measurement stamps belong to the arms that fit a result by
			// cutting what the model wrote, and this lever cuts only the list.
			event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
				cohortMemberCount(result.Cohort), cohortMemberCount(fitted.Cohort), grouped, false, overrun, params.GroupedNarrowingBasis)
			event.PredictedItems = PredictedItemsForPlan(*plan, cohortMemberCount(params.Graph.Cohort))
			event.DeadlineReserved = e.synthesisDeadlineReserve > 0
			event.OutcomeCompletenessState = fitted.Completeness.State
			event.EvidencePinnedMembers = walkListEvidencePinnedOnly(fitted)
			e.recordPlanNarrowing(ctx, principal, event)
			return fitted, firstPass, nil
		}
	}
	// CHAOS-6743 ALLOCATION, members before claims: on an ITEM overrun over a
	// cohort, cut per-member claims (depth) to the largest per-member cap that
	// fits BEFORE the cohort retry halves the members (scope). Prod served 5
	// of 11 repositories to "which repositories does this organization have?"
	// because halving was the only item lever. Halving now runs only when one
	// claim per member still overruns.
	if overrun == contractsv1.ContextFabricBudgetOverrunItems && params.WorkItemCensus == nil {
		claimLever, claimErr := e.planClaimDepthNarrowing(ctx, principal, plan, params.Frame, result, budget, measured, params.Facts, &firstPass, answerPassSecond, cardinality)
		if claimErr != nil {
			return InvestigationResult{}, firstPass, claimErr
		}
		e.recordClaimDepthNarrowing(ctx, principal, plan, answerPassFirst, measured, claimLever, budget)
		if claimLever.Served {
			members := cohortMemberCount(params.Graph.Cohort)
			event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
				members, members, grouped, false, overrun, params.GroupedNarrowingBasis)
			event.recordMeasurement(measured)
			event.PredictedItems = PredictedItemsForPlan(*plan, members)
			event.DeadlineReserved = e.synthesisDeadlineReserve > 0
			event.OutcomeCompletenessState = claimLever.Result.Completeness.State
			e.recordPlanNarrowing(ctx, principal, event)
			return claimLever.Result, firstPass, nil
		}
	}
	narrowed := narrowSynthesisInput(params, plan)
	before, after, canNarrow := narrowed.Before, narrowed.After, narrowed.Narrow
	// Reported HERE because narrowSynthesisInput has no telemetry handle and
	// this function does. Emitted whether or not the narrowing is ultimately
	// used: the retention pass RAN, and a decision that ran and was then
	// discarded is still a decision an operator can be looking for.
	if canNarrow {
		e.recordFactRetention(ctx, principal, FactRetentionEvent{
			Family: plan.Family, GroupKind: plan.GroupKind,
			Stage:    contractsv1.ContextFabricPlanNarrowingAssembledResult,
			Decision: narrowed.Retention,
		})
	}
	// Name WHICH of the three reasons declined the retry. They have
	// completely different fixes -- reconfigure the deployment, accept that
	// this answer is genuinely slow, or accept that nothing was left to
	// narrow -- and a single unexplained 413 tells an operator none of them.
	declined := RetryDeclinedNotApplicable
	// CHAOS-6558 (a): never run a retry that cannot reduce the overrunning
	// axis. When the rows alone overran (removing them would have fit) and
	// the narrowed input keeps every fact, the retry re-synthesizes over the
	// same evidence, and claim rows are copied verbatim from that evidence --
	// so it cannot shrink them. It would only spend a second synthesis to
	// arrive at the same refusal, which is the prod shape exactly.
	retryCannotReduce := canNarrow && rowLever.RowsDominate &&
		narrowed.Retention.FactsAfter >= narrowed.Retention.FactsBefore
	switch {
	case !canNarrow:
		declined = RetryDeclinedNothingToNarrow
	case retryCannotReduce:
		declined = RetryDeclinedCannotReduceAxis
	case params.Request.SuppliedSynthesis != nil:
		declined = RetryDeclinedSuppliedSynthesis
	case e.synthesisDeadlineReserve <= 0:
		declined = RetryDeclinedNoReserve
	case !e.retryDeadlineAvailable(ctx):
		declined = RetryDeclinedInsufficientDeadline
	}
	if declined != RetryDeclinedNotApplicable {
		// Before refusing: the cohort lever declined, but the cohort is
		// not the only thing charged against the item budget. Narrow what
		// CAN be narrowed without orphaning prose, disclose it by name,
		// and serve.
		//
		// This is the arm the acceptance question lands on. A
		// single-subject investigation has no cohort, so `declined` is
		// always nothing_to_narrow here and the refusal was reached
		// without any content reduction ever being attempted.
		attempt, accountingErr := e.planCandidateNarrowing(ctx, principal, plan, params.Frame, result, budget, measured, params.Facts, &firstPass, answerPassSecond, cardinality, params.WorkItemCensus)
		if accountingErr != nil {
			return InvestigationResult{}, firstPass, accountingErr
		}
		if attempt.Served {
			e.recordCandidateNarrowing(ctx, principal, plan, attempt, overrun, grouped, narrowed.Basis, before, after, declined, false, false)
			return attempt.Result, firstPass, nil
		}
		trim, trimErr := e.planBudgetTrim(ctx, principal, plan, params.Frame, result, budget, measured, params.Facts, &firstPass, answerPassSecond, cardinality, params.WorkItemCensus == nil)
		if trimErr != nil {
			return InvestigationResult{}, firstPass, trimErr
		}
		e.recordClaimDepthNarrowing(ctx, principal, plan, answerPassFirst, measured, trim, budget)
		if trim.Served {
			members := cohortMemberCount(params.Graph.Cohort)
			event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
				members, members, grouped, false, overrun, params.GroupedNarrowingBasis)
			event.recordMeasurement(measured)
			event.PredictedItems = PredictedItemsForPlan(*plan, members)
			event.DeadlineReserved = e.synthesisDeadlineReserve > 0
			event.RetryDeclined = declined
			event.OutcomeCompletenessState = trim.Result.Completeness.State
			e.recordPlanNarrowing(ctx, principal, event)
			return trim.Result, firstPass, nil
		}
		// MEMBER CUT, assessed: the retry is the only other lever that removes
		// members and it was declined. The assessment names, on the trace, the
		// members the answer could have left out; the served cut is deferred
		// (member_cut.go), so the refusal below still stands.
		if overrun == contractsv1.ContextFabricBudgetOverrunItems && params.WorkItemCensus == nil && trim.Reduced.Status == "" {
			memberCut := e.planMemberCut(params, firstPass, result, budget, measured)
			e.recordMemberCut(ctx, principal, plan, measured, memberCut, budget)
		}
		// The items cut left a document that fits the item ceiling and not
		// the byte ceiling (items are reported first when both are over, so
		// the byte levers above did not run): continue the byte ladder from
		// the cut document.
		if trim.Reduced.Status != "" && trim.Measured.Overrun == contractsv1.ContextFabricBudgetOverrunBytes {
			rowLeverOnCut, rowsServed, rowsErr := serveRows(trim.Reduced, trim.Measured, declined)
			if rowsErr != nil {
				return InvestigationResult{}, firstPass, rowsErr
			}
			if rowsServed {
				return rowLeverOnCut.Result, firstPass, nil
			}
			served, ok, pathErr := servePathDrop(trim.Reduced, trim.Measured, declined)
			if pathErr != nil {
				return InvestigationResult{}, firstPass, pathErr
			}
			if ok {
				return served, firstPass, nil
			}
		}
		// CHAOS-4809 PATH 2: BOTH counts, as narrowSynthesisInput actually
		// computed them. This used to pass `before` for both, so a refusal
		// that had already run a real overlap-aware set-cover selection
		// published it as a no-op -- the basis field naming an order and the
		// count pair denying that anything happened, with no way for a
		// reader to tell which to believe.
		// THE REFUSAL DESCRIBES ONE DOCUMENT: the assembled result this
		// stage measured, `measured`. Not `attempt.Measured`.
		//
		// A review found the difference. On the `insufficient` exit --
		// the reduction ran and still did not fit -- `attempt.Measured` is
		// the REDUCED document, while the axis was the pre-reduction one,
		// so the refusal published post-reduction counts under a
		// pre-reduction axis: 30 items against a ceiling of 30 (not over)
		// and 9593 bytes against 9500 (over), while naming `items`. The
		// numbers and the axis described different documents.
		//
		// Naming the reduced document instead would ALSO be wrong, and
		// that is the part worth writing down: #422 fixed the meaning of
		// this refusal's axis, and its regime test reads `items` here as
		// "the reduction was never applied". Making the axis follow the
		// reduced document breaks that reading, which is a real invariant
		// and not merely a passing test.
		//
		// So both halves come from `measured`, and planRefusal derives the
		// axis FROM it rather than taking one beside it -- there is no
		// second value left to diverge. The reduced document's own numbers
		// are not lost; they are on the candidate_narrowing record, which
		// is where a reader looking for what the reduction did will go.
		// A refusal is still where a per-group breach matters most, and it
		// still carries real quota fields rather than the zeros both
		// refusal arms used to emit.
		// An items cut that left the document over the byte target only is the
		// document the byte target was refused on, so it is the one fitted
		// against the service ceiling.
		ceilingDoc, ceilingInput, ceilingAllocation, ceilingOverrun, ceilingFinalize := result, measurementInput, allocation, overrun, false
		if overrun != contractsv1.ContextFabricBudgetOverrunBytes && trim.Reduced.Status != "" && trim.Measured.Overrun == contractsv1.ContextFabricBudgetOverrunBytes {
			ceilingDoc, ceilingInput, ceilingAllocation, ceilingOverrun, ceilingFinalize = trim.Reduced, trim.Reduced, trim.Measured.Allocation, contractsv1.ContextFabricBudgetOverrunBytes, true
		}
		if served, ok, serveErr := e.serveAtServiceCeiling(ctx, principal, plan, params, "assembled_result", ceilingDoc, ceilingInput, ceilingFinalize, ceilingAllocation, params.Facts, &firstPass, answerPassSecond, answerPassFirst, cardinality, budget, ceilingOverrun); serveErr != nil {
			return InvestigationResult{}, firstPass, serveErr
		} else if ok {
			return served, firstPass, nil
		}
		return InvestigationResult{}, firstPass, e.planRefusal(ctx, principal, e.refusalPlan(plan, measured.Measurement), measured, false, grouped, narrowed.Basis, before, after, declined, attempt.Declined, continuationAxisForOverrun(*plan, rowLever))
	}

	// Selection records the first attempt's measured trigger. The retry has
	// not executed yet; its outcome remains on the final narrowing record.
	if e.telemetry != nil {
		selected := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
			before, after, grouped, false, overrun, narrowed.Basis)
		selected.recordMeasurement(measured)
		selected.PredictedItems = PredictedItemsForPlan(*plan, before)
		selected.DeadlineReserved = e.synthesisDeadlineReserve > 0
		e.telemetry.RecordSynthesisRetrySelection(ctx, principal, selected)
	}

	e.recordPlanNarrowingStep(plan, PlanNarrowing{
		Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Basis:   planStageBasis(contractsv1.ContextFabricPlanNarrowingAssembledResult, grouped, narrowed.Basis),
		Before:  before,
		After:   after,
		Groups:  false,
		Overrun: overrun,
	})

	// The retry's OWN allocation, derived HERE -- BEFORE synthesis -- and
	// carried into it, so the retried document is PRODUCED under exactly the
	// grants it is later MEASURED against.
	//
	// A DELIBERATE re-allocation, and NOT a second authority over the same
	// number: the retry runs against a NARROWED cohort, so this is a different
	// budget for a different document. What was wrong before was not that it
	// existed but WHEN it was computed -- after synthesis, so the producer had
	// already spent the first pass's grants and only the measurement saw these.
	retryAllocation := AllocateItems(*plan, groupCountOf(narrowed.Graph.Cohort), cohortMemberCount(narrowed.Graph.Cohort))
	retryParams := params.forRetry(narrowed.Graph, narrowed.Facts, retryAllocation)
	// REFRESH THE CARRIED PLAN FROM THE LIVE ONE.
	//
	// `params.Plan` is a VALUE copied when these params were built, which was
	// before this stage recorded its own narrowing step above. The retry pass
	// computes its cardinality from `params.Plan.Narrowing` before synthesis,
	// so without this it would read a narrowing list that predates the very
	// narrowing it is a retry OF -- and declare a population smaller than the
	// one the plan asked for. A carried copy that has since gone stale is the
	// defect class this file's allocator comments already record; this is the
	// same shape, on the plan.
	retryParams.Plan = *plan
	// The re-rank's citations MUST travel with the re-ranked cohort:
	// narrateCohortDriverJudgments resolves them per member, so citations
	// computed against the wider member set would narrate against members the
	// retry no longer carries.
	retryParams.CohortSignalCitations = narrowed.Citations
	// consumedRetryAllocation is what the retry pass ACTUALLY SPENT -- synthesis
	// and narration both read it inside the producer -- and it is what the guard
	// measures below. Measuring `retryParams.Allocation` instead would measure the
	// caller's copy: equal on every honest input, and blind to any fault the
	// producer applied to its own, which is the defect keystone #5 found in
	// narration.
	// This records execution, independently of whether the retry fit.
	firstPass.RetryAttempted = true
	retried, consumedRetryAllocation, retryPending, retryCardinality, retryErr := e.synthesizeAndAssemble(ctx, principal, retryParams)
	retryPending.RetryAttempted = firstPass.RetryAttempted
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
		event.recordMeasurement(measured)
		// The attempt here is the FIRST synthesis's, taken against the
		// pre-narrowing cohort, so the prediction pairs with `before`.
		event.PredictedItems = PredictedItemsForPlan(*plan, before)
		event.RetryAttempted = true
		event.RetryFit = false
		event.RetryFailed = true
		event.DeadlineReserved = e.synthesisDeadlineReserve > 0
		// The retry error propagates below, so nothing from this call is
		// served -- but `measurementInput` is the document this stage
		// measured before the retry ran, and its completeness is already
		// computed, so disclosing it here costs nothing and keeps this
		// field never-silently-empty on every assembled_result line, served
		// or not.
		event.OutcomeCompletenessState = measurementInput.Completeness.State
		e.recordPlanNarrowing(ctx, principal, event)
		// CHAOS-4726 codex round 1: the FIRST synthesis call's rejection is
		// caught at the Investigate call site (engine.go), but this retry
		// call is a SECOND, separate invocation that can itself be rejected
		// -- and plan.Narrowing already carries the assembled_result step
		// recorded just above, so this is its own accurate "state right
		// before this synthesis call" snapshot, not a stale reuse of the
		// first call's.
		return InvestigationResult{}, firstPass, withSynthesisNarrowingSnapshot(retryErr, *plan)
	}
	// CARRY THE FIRST PASS'S COVER EVENTS FORWARD, into the retry's OWN
	// assemblyTelemetry, before this pass appends its own. `retryPending` is a
	// FRESH struct from this second synthesizeAndAssemble call -- it does not
	// inherit `firstPass`'s fields -- and every return below this point
	// returns some form of `retryPending`, never `firstPass`, so without this
	// merge the first pass's discarded-answer cover events would simply be
	// lost rather than published as the deliberate exception they are. See
	// assemblyTelemetry.ObservationCover.
	merged := make([]ReadRequirementObservationCoverEvent, 0, len(firstPass.ObservationCover)+len(retryPending.ObservationCover))
	merged = append(merged, firstPass.ObservationCover...)
	merged = append(merged, retryPending.ObservationCover...)
	retryPending.ObservationCover = merged
	// Finalize the retry too, or the second pass repeats round 1 finding 1's
	// defect: measuring a pre-final shape and serving a larger one.
	// TWO BINDINGS AT ONE SITE, and they are not alternatives.
	//
	// The retry is a DIFFERENT DOCUMENT from the first pass. Its EVIDENCE is
	// `retryParams.Facts`, never `params.Facts`: this result was synthesized
	// from the NARROWED bundle, so evaluating its read populations against
	// the first pass's facts would report coverage for a document nobody
	// served. Its ALLOCATION is bound separately, by the other lane's fix
	// above -- measured from what the PRODUCER returned as consumed, never a
	// caller-side copy.
	//
	// They are the same "stale document at the retry" class on different
	// axes, found independently by two lanes. Taking one without the other
	// re-opens the half it did not fix.
	// params.Graph, NOT retryParams.Graph, and the two differ on purpose. The
	// retry re-synthesizes over a NARROWED cohort, so retryParams carries
	// fewer members -- but how many members retrieval SAW is a fact about the
	// pool, which a narrowing does not change. Reading the narrowed graph here
	// would let the declared population shrink every time the answer was cut
	// to fit, which is exactly backwards: the tighter the answer, the more
	// there was that it could not carry.
	// retryCardinality, NOT the first pass's: the retry re-synthesized over a
	// NARROWED cohort, and the number must describe the document that will be
	// served. This is the recomputation the pre-synthesis move preserves --
	// one per pass, each against its own member set.
	if params.WorkItemCensus != nil {
		retried = ServeWorkItemTupleCensus(retried, params.WorkItemCensus)
		// Measure the same display labels the final response will carry.
		applyCoverageDisplayLabels(&retried)
		retried = restrictWorkItemTupleEvidence(retried)
	}
	retried = e.finalizeResult(ctx, principal, retried, *plan, params.Frame, retryParams.Facts, &retryPending, answerPassSecond, retryCardinality)
	// The outcome-derivation completeness authority is applied exactly ONCE,
	// inside finalizeServed -- see engine.go's fresh-dispatch tupleCensus
	// branch for the full reasoning, identical here. `retried` itself
	// (returned below, and carried forward by the caller to finalizeServed)
	// is never corrected here; only the LOCAL measurement copy immediately
	// below is, matching the first-pass measurement's own approach.
	// READ BACK FROM THE PRODUCER, not from the params and not from the local
	// `retryAllocation`, and the difference is the entire lesson of this class.
	//
	// `ItemAllocation` is a VALUE type, so `forRetry(..., retryAllocation)`
	// hands the producer a COPY. Measuring the local variable would measure a
	// different object that merely happens to be equal -- which is exactly the
	// shape keystone review #3 caught in the first pass, where a re-derivation
	// was equal on every honest input and only a fault separated the two.
	// Binding the VALUE at the call site is not the same as binding the OBJECT,
	// and reading the params field is not enough either: `params` is BY VALUE, so
	// no fault inside the producer can ever reach a guard that reads the caller's
	// copy. The only formulation a value type admits across a function boundary is
	// that the producer RETURNS what it consumed and the guard measures that.
	retryMeasurementInput := retried
	if params.WorkItemCensus != nil {
		retryMeasurementInput = ApplyServerCompletenessAuthority(ApplyServedStatusFloors(retried), e.serverCompletenessAuthorityEnabled, e.serverCompletenessAuthoritySymmetricEnabled, DeriveCompletenessAuthority(ApplyServedStatusFloors(retried)))
	}
	retryMeasured, err := e.measureAssembledAttempt(ctx, principal, "re_synthesized_result", consumedRetryAllocation, retryMeasurementInput, budget)
	if err != nil {
		return InvestigationResult{}, retryPending, err
	}
	// The census gives way first on the retry pass too; see the first pass.
	if budget.MaxSerializedBytes > 0 && retryMeasured.Measurement.Bytes > budget.MaxSerializedBytes {
		if shed, ok := shedFactScopeCensusToBytes(retried, retryMeasured.Measurement.Bytes-budget.MaxSerializedBytes); ok {
			shedInput := shed
			if params.WorkItemCensus != nil {
				shedInput = ApplyServerCompletenessAuthority(ApplyServedStatusFloors(shed), e.serverCompletenessAuthorityEnabled, e.serverCompletenessAuthoritySymmetricEnabled, DeriveCompletenessAuthority(ApplyServedStatusFloors(shed)))
			}
			remeasured, remeasureErr := e.measureAssembledAttempt(ctx, principal, "re_synthesized_result", consumedRetryAllocation, shedInput, budget)
			if remeasureErr != nil {
				return InvestigationResult{}, retryPending, remeasureErr
			}
			retried, retryMeasured = shed, remeasured
		}
	}
	retryMeasurement := retryMeasured.Measurement
	retryOverrun := retryMeasured.Overrun
	// The retry narrowed what the model reads, and the served list was put back:
	// when its bytes still overrun, the walk list is cut to fit as it is on the
	// first pass, before the retry is declared a failure.
	if retryOverrun == contractsv1.ContextFabricBudgetOverrunBytes && params.WorkItemCensus != nil {
		fitted, ok, fitErr := e.fitWalkListToBytes(ctx, principal, plan, params.Frame, retried, budget, retryMeasured, retryParams.Facts, &retryPending, retryCardinality)
		if fitErr != nil {
			return InvestigationResult{}, retryPending, fitErr
		}
		if ok {
			retried = fitted
			fittedInput := ApplyServerCompletenessAuthority(ApplyServedStatusFloors(retried), e.serverCompletenessAuthorityEnabled, e.serverCompletenessAuthoritySymmetricEnabled, DeriveCompletenessAuthority(ApplyServedStatusFloors(retried)))
			remeasured, remeasureErr := e.measureAssembledAttempt(ctx, principal, "re_synthesized_result", consumedRetryAllocation, fittedInput, budget)
			if remeasureErr != nil {
				return InvestigationResult{}, retryPending, remeasureErr
			}
			retryMeasured, retryMeasurement, retryOverrun = remeasured, remeasured.Measurement, remeasured.Overrun
		}
	}
	// The outcome layer's attempt runs BEFORE this event is built, because
	// the event's own `refusal_planned` field is a claim about what happens
	// next. Emitting it first published refusal_planned=true for every
	// investigation the reduction went on to SERVE with a 200 -- a refusal
	// counter counting answers that were never refused, and the exact
	// telemetry-describes-a-different-artifact class the deferred emitters
	// in this file already fix elsewhere.
	//
	// The attempt starts as the RETRY'S OWN measured attempt, never as a zero
	// value. That zero value is round 3's finding in one line: a retry that
	// FITS skipped the branch below, and the event then read three quota
	// fields off a struct nobody had filled -- reporting no groups and no
	// quota for a two-group answer that had just been served.
	outcomeAttempt := outcomeNarrowingAttempt{Measured: retryMeasured}
	// CHAOS-6558: the retry ran because the rows alone could not account for
	// the overrun (or the narrowing dropped facts). Its document may now be
	// curable by the row lever, so the lever gets its turn before refusal.
	var retryRowLever factRowTruncationAttempt
	if retryOverrun == contractsv1.ContextFabricBudgetOverrunBytes && params.WorkItemCensus == nil {
		var rowErr error
		retryRowLever, rowErr = e.planFactRowTruncation(ctx, principal, plan, params.Frame, retried, budget, retryMeasured, retryParams.Facts, &retryPending, answerPassThird, retryCardinality)
		if rowErr != nil {
			return InvestigationResult{}, retryPending, rowErr
		}
		e.recordFactRowTruncation(ctx, principal, plan, answerPassSecond, retryMeasured, retryRowLever, budget)
		if retryRowLever.Served {
			event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, retryOverrun, narrowed.Basis)
			event.recordMeasurement(retryMeasured)
			event.PredictedItems = PredictedItemsForPlan(*plan, after)
			event.RetryAttempted = true
			event.DeadlineReserved = e.synthesisDeadlineReserve > 0
			event.OutcomeCompletenessState = retryRowLever.Result.Completeness.State
			e.recordPlanNarrowing(ctx, principal, event)
			retryRanked := narrowed.Ranked
			retryPending.CohortRanked = &retryRanked
			return retryRowLever.Result, retryPending, nil
		}
		retryPathLever, pathErr := e.planPathDrop(ctx, principal, plan, params.Frame, retried, budget, retryMeasured, retryParams.Facts, &retryPending, answerPassThird, retryCardinality)
		if pathErr != nil {
			return InvestigationResult{}, retryPending, pathErr
		}
		e.recordPathDrop(ctx, principal, plan, answerPassSecond, retryMeasured, retryPathLever, budget)
		if retryPathLever.Served {
			event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, retryOverrun, narrowed.Basis)
			event.recordMeasurement(retryMeasured)
			event.PredictedItems = PredictedItemsForPlan(*plan, after)
			event.RetryAttempted = true
			event.DeadlineReserved = e.synthesisDeadlineReserve > 0
			event.OutcomeCompletenessState = retryPathLever.Result.Completeness.State
			e.recordPlanNarrowing(ctx, principal, event)
			retryRanked := narrowed.Ranked
			retryPending.CohortRanked = &retryRanked
			return retryPathLever.Result, retryPending, nil
		}
	}
	// CHAOS-6743: the retried document gets the claim-depth lever too, before
	// the candidate reduction and any refusal.
	if retryOverrun == contractsv1.ContextFabricBudgetOverrunItems && params.WorkItemCensus == nil {
		retryClaimLever, claimErr := e.planClaimDepthNarrowing(ctx, principal, plan, params.Frame, retried, budget, retryMeasured, retryParams.Facts, &retryPending, answerPassThird, retryCardinality)
		if claimErr != nil {
			return InvestigationResult{}, retryPending, claimErr
		}
		e.recordClaimDepthNarrowing(ctx, principal, plan, answerPassSecond, retryMeasured, retryClaimLever, budget)
		if retryClaimLever.Served {
			event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, retryOverrun, narrowed.Basis)
			event.recordMeasurement(retryMeasured)
			event.PredictedItems = PredictedItemsForPlan(*plan, after)
			event.RetryAttempted = true
			event.DeadlineReserved = e.synthesisDeadlineReserve > 0
			event.OutcomeCompletenessState = retryClaimLever.Result.Completeness.State
			e.recordPlanNarrowing(ctx, principal, event)
			retryRanked := narrowed.Ranked
			retryPending.CohortRanked = &retryRanked
			return retryClaimLever.Result, retryPending, nil
		}
	}
	if retryOverrun != contractsv1.ContextFabricBudgetFits {
		var accountingErr error
		outcomeAttempt, accountingErr = e.planCandidateNarrowing(ctx, principal, plan, params.Frame, retried, budget, retryMeasured, retryParams.Facts, &retryPending, answerPassThird, retryCardinality, params.WorkItemCensus)
		if accountingErr != nil {
			return InvestigationResult{}, retryPending, accountingErr
		}
		if !outcomeAttempt.Served {
			trim, trimErr := e.planBudgetTrim(ctx, principal, plan, params.Frame, retried, budget, retryMeasured, retryParams.Facts, &retryPending, answerPassThird, retryCardinality, false)
			if trimErr != nil {
				return InvestigationResult{}, retryPending, trimErr
			}
			e.recordClaimDepthNarrowing(ctx, principal, plan, answerPassSecond, retryMeasured, trim, budget)
			if trim.Served {
				event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, retryOverrun, narrowed.Basis)
				event.recordMeasurement(retryMeasured)
				event.PredictedItems = PredictedItemsForPlan(*plan, after)
				event.RetryAttempted = true
				event.DeadlineReserved = e.synthesisDeadlineReserve > 0
				event.OutcomeCompletenessState = trim.Result.Completeness.State
				e.recordPlanNarrowing(ctx, principal, event)
				retryRanked := narrowed.Ranked
				if params.WorkItemCensus == nil {
					retryPending.CohortRanked = &retryRanked
				}
				return trim.Result, retryPending, nil
			}
		}
	}
	// ONE decision event per investigation. When the reduction rescues a
	// retry that did not fit, the event that describes the SERVED answer is
	// recordCandidateNarrowing's, so emitting this one too would count the
	// same investigation twice -- exactly the contract this function's own
	// header states, broken by the reordering above. Found by adversarial
	// review; the first version of that test counted only the narrowing
	// events and so could not see the extra one.
	if outcomeAttempt.Served {
		e.recordCandidateNarrowing(ctx, principal, plan, outcomeAttempt, retryOverrun, grouped, narrowed.Basis, before, after, RetryDeclinedNotApplicable, true, false)
		retryRanked := narrowed.Ranked
		if params.WorkItemCensus == nil {
			retryPending.CohortRanked = &retryRanked
		}
		return outcomeAttempt.Result, retryPending, nil
	}
	// ONE DOCUMENT: the RETRY's, which is what the refusal below reports.
	//
	// This event used to take its axis from `overrun` -- the FIRST pass's --
	// and its measurement from `outcomeAttempt.Measured`, which on the
	// insufficient exit is the REDUCED document, while the refusal at the end
	// of this arm reports `retryMeasurement`/`retryOverrun`. Three documents
	// for one decision. Measured by a keystone review: the refusal reported 29
	// items / 11,567 bytes while its sole assembled_result event reported 20
	// items / 9,918 bytes and kept `overrun=items` against a 20-item ceiling.
	//
	// It is the sibling of the same defect at the declined arm, and finding it
	// is why a defect class gets a SWEEP rather than a patch at the reported
	// line. The pair is now enumerated and pinned by
	// TestEveryRefusalSitePairsOneDocument.
	//
	// `outcomeAttempt` still supplies the reduction's own dimensions below --
	// Declined, and the served/declared counts on the served path -- because
	// WHY the reduction did not save this answer is a different fact from WHAT
	// was measured, and it is not a measurement.
	// Before the refusal event: a document the service ceiling admits is not a
	// planned refusal, and the artifacts must not say it was.
	if served, ok, serveErr := e.serveAtServiceCeiling(ctx, principal, plan, retryParams, "re_synthesized_result", retried, retryMeasurementInput, false, consumedRetryAllocation, retryParams.Facts, &retryPending, answerPassThird, answerPassSecond, retryCardinality, budget, retryOverrun); serveErr != nil {
		return InvestigationResult{}, retryPending, serveErr
	} else if ok {
		retryRanked := narrowed.Ranked
		if params.WorkItemCensus == nil {
			retryPending.CohortRanked = &retryRanked
		}
		return served, retryPending, nil
	}
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, retryMeasured.Overrun, narrowed.Basis)
	event.recordMeasurement(retryMeasured)
	// `after`, not `before`: this event measures the RE-synthesized answer,
	// which ran against the narrowed cohort. Predicting from `before` would
	// pair a measurement of one cohort with an expectation for a larger one.
	event.PredictedItems = PredictedItemsForPlan(*plan, after)
	event.RetryAttempted = true
	event.RetryFit = retryOverrun == contractsv1.ContextFabricBudgetFits
	event.DeadlineReserved = e.synthesisDeadlineReserve > 0
	// The reduction did not serve -- that path returned above -- so a retry
	// that did not fit is a planned refusal.
	event.RefusalPlanned = !event.RetryFit
	// WHY the outcome layer did not save it: "the lever did not apply" and
	// "the lever ran and was not enough" are different operator problems.
	event.OutcomeReductionDeclined = outcomeAttempt.Declined
	if event.RefusalPlanned {
		// Only when a refusal is actually planned. Recording the axis on a
		// fitting retry would make the counter say the caller was given
		// advice they never received.
		event.NarrowerContinuationAxis = continuationAxisForOverrun(*plan, retryRowLever)
	}
	// `retried`, not `measurementInput`/`measured`: when the retry FITS
	// without needing the candidate reduction (retryOverrun == Fits below),
	// `retried` is exactly what this function returns as the served answer,
	// and its Completeness.State is already computed by finalizeResult. On
	// the refusal exit this same value is still the last real document this
	// stage measured, so the field is never silently empty either way.
	event.OutcomeCompletenessState = retried.Completeness.State
	e.recordPlanNarrowing(ctx, principal, event)

	if retryOverrun != contractsv1.ContextFabricBudgetFits {
		// The rescued case returned above, before this event was emitted.
		// What is left is a retry that did not fit and a reduction that
		// could not save it.
		//
		// ONE bounded retry, never k of them. Decision D5 rejected further
		// retries explicitly: they inherit the same deadline problem and
		// merely move the terminal case, arriving at the same unanswered
		// question with more latency.
		return InvestigationResult{}, retryPending, e.refusalFromAxis(e.refusalPlan(plan, retryMeasurement), retryMeasurement, retryOverrun, true, continuationAxisForOverrun(*plan, retryRowLever))
	}
	// The SERVED answer is the retry's, so the ranking event that describes it
	// is the retry's too. Emitting the first pass's would report a ranking
	// computed over members the caller never received -- the same
	// "telemetry describes a different artifact than the one served" class the
	// deferred emitters fix.
	retryRanked := narrowed.Ranked
	if params.WorkItemCensus == nil {
		retryPending.CohortRanked = &retryRanked
	}
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
	// Retention is what the retention pass dropped, carried out to the
	// caller because THIS function has no telemetry handle and the caller
	// does. Recomputing it there would be a second authority for one pass.
	Retention FactRetentionDecision
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
	} else if params.WorkItemCensus != nil && params.WorkItemCensus.walkList != nil {
		// The model keeps reading the strongest links of what it read.
		kept, narrowed = params.WorkItemCensus.synthesisMembers(cohort, target)
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
	return narrowedInputOver(params, graph, facts, cohort, kept, before, basis)
}

// narrowedInputOver is the tail of every narrowing: the kept members become the
// cohort, the cohort is re-ranked among itself (RankCohort normalizes workload
// WITHIN the cohort, so scores computed against a wider member set do not
// describe it), and the facts are retained for what is left. The cohort retry
// and the member cut both end here, so a cut cohort is ranked, cited and
// evidenced exactly as a retried one is.
func narrowedInputOver(params synthesisAssemblyParams, graph GraphContext, facts CanonicalFactBundle, cohort *Cohort, kept []CohortMember, before int, basis contractsv1.ContextFabricNarrowingBasis) narrowedInput {
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
	rankedCohort := cohort
	var rankEvent CohortRankedEvent
	var citations cohortMemberSignalCitations
	if params.WorkItemCensus == nil {
		rankedCohort, rankEvent, citations = RankCohortWithReads(cohort, facts.Facts, facts.Coverage, facts.EvaluatedSubjects)
		applyCohortJudgmentMismatch(rankedCohort, params.Interpretation.RequestedJudgmentKind)
		// Same after-the-call capture as the primary rank site (engine.go):
		// RankCohort itself never sees the requested judgment kind.
		rankEvent.JudgmentMismatch = rankedCohort.JudgmentMismatch
		rankEvent.RequestedJudgmentKind = params.Interpretation.RequestedJudgmentKind
	}
	graph.Cohort = rankedCohort
	var retention FactRetentionDecision
	facts.Facts, retention = RetainFactsForCohortWithDecision(facts.Facts, rankedCohort, removed, params.Resolution.Committed)
	graph = graphWithoutRemovedMembers(graph, removed, facts.Facts)
	return narrowedInput{
		Graph: graph, Facts: facts, Citations: citations, Ranked: rankEvent,
		Before: before, After: len(kept), Narrow: true, Basis: basis, Retention: retention,
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
// It takes the MEASURED ATTEMPT rather than a bare measurement, and that is
// this seam's own history: a refusal is the case where a per-group breach
// matters MOST, and both refusal arms used to emit zero quota fields while the
// real exposure sat on an object one branch away. An enforcement layer told
// nothing on the one path that refuses has been told nothing.
//
// axis is the continuation advice, chosen by the caller (CHAOS-6558): the
// family's declared axis, except that a byte overrun the fact rows account
// for is narrowed by less TIME, not by fewer members -- see
// continuationAxisForOverrun.
func (e *Engine) planRefusal(ctx context.Context, principal storage.Principal, plan *AnswerPlan, measured MeasuredAttempt, retryAttempted, grouped bool, basis contractsv1.ContextFabricNarrowingBasis, members, selected int, declined RetryDeclinedReason, reductionDeclined OutcomeReductionDeclined, axis NarrowingContinuationAxis) error {
	// THE AXIS COMES FROM THE MEASURED ATTEMPT, not from a parameter.
	//
	// This function used to take `overrun` separately while taking the
	// measurement from `measured`, and the caller's value is the PRE-reduction
	// one: after the candidate reduction runs, the document is a different
	// document. A refusal therefore published post-reduction counts under a
	// pre-reduction axis -- measured at 30 items against a ceiling of 30 (not
	// over) and 9593 bytes against 9500 (over), while naming `items`. The
	// numbers and the axis described different documents, and a reader had no
	// way to tell which to believe.
	//
	// One value, one derivation. The same rule the allocator's Agreement()
	// applies to grants: if two things must agree, do not carry them
	// separately and hope.
	overrun := measured.Overrun
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, members, selected, grouped, false, overrun, basis)
	event.recordMeasurement(measured)
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
	// WHY the outcome layer did not save this answer. Without it the refusal
	// says only that one happened, and "the byte axis overran", "there were
	// no candidates", and "the candidate cut ran and was not enough" are one
	// undifferentiated absence.
	event.OutcomeReductionDeclined = reductionDeclined
	event.NarrowerContinuationAxis = axis
	e.recordPlanNarrowing(ctx, principal, event)
	return e.refusalFromAxis(plan, measured.Measurement, overrun, retryAttempted, axis)
}

// recordCeilingFit emits the one event that announces a document served between
// the request's target and the service ceiling. It is the ONE place that stamps
// that measurement: the stage-3 fit and the decisive fit both call it, so the
// trace rebuilds the decision (measured bytes and items, the ceiling it was
// fitted to, the step) from this event alone.
func (e *Engine) recordCeilingFit(ctx context.Context, principal storage.Principal, plan *AnswerPlan, members int, grouped bool, basis contractsv1.ContextFabricNarrowingBasis, measured MeasuredAttempt, served InvestigationResult, overrun contractsv1.ContextFabricBudgetOverrun, step CeilingFitStep) {
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
		members, members, grouped, false, overrun, basis)
	event.recordMeasurement(measured)
	event.PredictedItems = PredictedItemsForPlan(*plan, members)
	event.DeadlineReserved = e.synthesisDeadlineReserve > 0
	event.OutcomeCompletenessState = served.Completeness.State
	event.CeilingFit = step
	event.MaxSerializedBytes = e.maxSerializedBytes
	e.recordPlanNarrowing(ctx, principal, event)
}

// refusalPlan is the plan a byte refusal is built from. The plan's budget is the
// request's own target; when the document crossed the service ceiling as well,
// the line it crossed is the ceiling, and the refusal names that one (the plan
// itself is not changed: the next turn compares against the target).
func (e *Engine) refusalPlan(plan *AnswerPlan, measurement ResponseMeasurement) *AnswerPlan {
	if e.maxSerializedBytes <= 0 || measurement.Bytes <= e.maxSerializedBytes || plan.Budget.MaxSerializedBytes >= e.maxSerializedBytes {
		return plan
	}
	named := *plan
	named.Budget.MaxSerializedBytes = e.maxSerializedBytes
	return &named
}

// serveAtServiceCeiling is the last non-model step before a byte refusal. The
// caller's byte option is the target the levers shed toward; the service
// ceiling is the only refusal line. A document that overran only the target
// (and no item ceiling) is fitted once more against the service ceiling with
// the same model-free levers -- measure, row truncation, path drop -- so a
// target the caller never made a refusal line does not refuse an answer the
// service can serve. It never calls the model: it runs after every attempt
// that could, so the refusal it spares was the only thing left.
//
// ok is true when doc (or a lever's result) fits the ceiling; the plan then
// states the ceiling it was fitted to, and the later fit asserts against it.
func (e *Engine) serveAtServiceCeiling(ctx context.Context, principal storage.Principal, plan *AnswerPlan, params synthesisAssemblyParams, stage string, doc, measurementInput InvestigationResult, finalize bool, allocation ItemAllocation, facts CanonicalFactBundle, pending *assemblyTelemetry, planPass, recordPass int, cardinality MembershipCardinality, budget ResponseBudget, overrun contractsv1.ContextFabricBudgetOverrun) (served InvestigationResult, ok bool, err error) {
	// A zero service ceiling means unbounded, as everywhere else the engine
	// reads it; a ceiling at or below the target leaves nothing to widen to.
	if overrun != contractsv1.ContextFabricBudgetOverrunBytes || budget.MaxSerializedBytes <= 0 ||
		(e.maxSerializedBytes > 0 && e.maxSerializedBytes <= budget.MaxSerializedBytes) {
		return InvestigationResult{}, false, nil
	}
	ceilingBudget := ResponseBudget{MaxItems: budget.MaxItems, MaxSerializedBytes: e.maxSerializedBytes}
	measured, err := e.measureAssembledAttempt(ctx, principal, stage, allocation, measurementInput, ceilingBudget)
	if err != nil {
		return InvestigationResult{}, false, err
	}
	step := CeilingFitNone
	switch {
	case measured.Overrun == contractsv1.ContextFabricBudgetFits:
		// A document an items cut handed back is the disclosed cut, before the
		// derived fields (render shapes, completeness, plan stamps) were
		// recomputed over what it now holds: it is finalized exactly as every
		// lever's served document is.
		served, ok, step = doc, true, CeilingFitMeasured
		if finalize {
			served = e.finalizeResult(ctx, principal, doc, *plan, params.Frame, facts, pending, planPass, cardinality)
		}
	case measured.Overrun == contractsv1.ContextFabricBudgetOverrunBytes && params.WorkItemCensus != nil:
		fitted, fittedOK, fitErr := e.fitWalkListToBytes(ctx, principal, plan, params.Frame, doc, ceilingBudget, measured, facts, pending, cardinality)
		if fitErr != nil {
			return InvestigationResult{}, false, fitErr
		}
		if fittedOK {
			served, ok, step = fitted, true, CeilingFitWalkListCut
		}
	case measured.Overrun == contractsv1.ContextFabricBudgetOverrunBytes && params.WorkItemCensus == nil:
		rows, rowErr := e.planFactRowTruncation(ctx, principal, plan, params.Frame, doc, ceilingBudget, measured, facts, pending, planPass, cardinality)
		if rowErr != nil {
			return InvestigationResult{}, false, rowErr
		}
		e.recordFactRowTruncation(ctx, principal, plan, recordPass, measured, rows, ceilingBudget)
		if rows.Served {
			served, ok, step = rows.Result, true, CeilingFitRowTruncation
			break
		}
		paths, pathErr := e.planPathDrop(ctx, principal, plan, params.Frame, doc, ceilingBudget, measured, facts, pending, planPass, cardinality)
		if pathErr != nil {
			return InvestigationResult{}, false, pathErr
		}
		e.recordPathDrop(ctx, principal, plan, recordPass, measured, paths, ceilingBudget)
		if paths.Served {
			served, ok, step = paths.Result, true, CeilingFitPathDrop
		}
	}
	if ok {
		// The plan keeps the request's own target, which is what the next turn
		// compares against. The event below carries the document's measured
		// bytes and items (as assembled, before any lever) and the step that
		// fitted it; the decision rebuilds from this one event. The ceiling it
		// was fitted to is the service's own, and the decisive fit is told so.
		pending.CeilingFitted = true
		members := cohortMemberCount(params.Graph.Cohort)
		grouped := params.Graph.Cohort != nil && len(params.Graph.Cohort.Groups) > 0
		e.recordCeilingFit(ctx, principal, plan, members, grouped, params.GroupedNarrowingBasis, measured, served, overrun, step)
	}
	return served, ok, nil
}

func (e *Engine) refusalFrom(plan *AnswerPlan, measurement ResponseMeasurement, overrun contractsv1.ContextFabricBudgetOverrun, retryAttempted bool) error {
	return e.refusalFromAxis(plan, measurement, overrun, retryAttempted, narrowerContinuationAxisFor(*plan))
}

// continuationAxisForOverrun is the advice a refusal gives. The family's
// declared axis, EXCEPT when the byte overrun is one the fact rows alone
// account for: fewer members does not shrink rows the retained facts carry,
// so naming result_count there is advice that cannot work. Rows are the
// evidence window's volume, so a shorter window is the narrowing that fits.
func continuationAxisForOverrun(plan AnswerPlan, rowLever factRowTruncationAttempt) NarrowingContinuationAxis {
	if rowLever.RowsDominate {
		return NarrowingContinuationEvidenceWindow
	}
	return narrowerContinuationAxisFor(plan)
}

func (e *Engine) refusalFromAxis(plan *AnswerPlan, measurement ResponseMeasurement, overrun contractsv1.ContextFabricBudgetOverrun, retryAttempted bool, axis NarrowingContinuationAxis) error {
	return AnswerBudgetRefusal{
		Overrun:                  overrun,
		MeasuredItems:            measurement.Items.Budgeted(),
		MeasuredBytes:            measurement.Bytes,
		MaxItems:                 plan.Budget.MaxItems,
		MaxSerializedBytes:       plan.Budget.MaxSerializedBytes,
		Family:                   plan.Family,
		NarrowerContinuationAxis: axis,
		RetryAttempted:           retryAttempted,
		Sections:                 measurement.Sections(),
		Stage:                    StageSynthesis,
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
