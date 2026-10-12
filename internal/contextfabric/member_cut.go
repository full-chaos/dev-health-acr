package contextfabric

import (
	"context"
	"errors"
	"slices"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The member cut is the model-free item lever for a flat cohort whose answer
// overran the item budget on members the answer does not name. Every other
// lever that removes members re-synthesizes (the cohort retry) and so needs a
// model call and the deadline for it.
//
// It does NOT patch the document. A cut produces a new cohort, and everything
// derived from the cohort -- the cardinality claim and sentence, the count,
// read and ranking rows, the narrated drivers, the render shapes, the
// coverage and the evidence closure -- comes from ONE assembly over it: the
// pass-1 raw draft is assembled again over the cut graph through
// synthesizeAndAssemble with no model call, then finalized like any pass.
//
// What is not regenerated is the model's prose (the draft's own judgment,
// pressures and driver summaries): the cut removes only members the draft never
// names as a subject, and the disclosure says the answer was cut.

// MemberCutDeclined is the CLOSED vocabulary of why the member cut did not
// serve an answer.
type MemberCutDeclined string

const (
	MemberCutNotApplicable MemberCutDeclined = ""
	// MemberCutNotItemsAxis: the answer did not overrun on items, or had no
	// item ceiling.
	MemberCutNotItemsAxis MemberCutDeclined = "not_items_axis"
	// MemberCutNoCohort: no flat cohort of two or more members (none, grouped,
	// a work-item census, or a single member).
	MemberCutNoCohort MemberCutDeclined = "no_cohort"
	// MemberCutNoDraft: the pass kept no draft to assemble again (a
	// client-written synthesis, or a pass that did not synthesize).
	MemberCutNoDraft MemberCutDeclined = "no_draft"
	// MemberCutDraftCarriesCohort: the draft brings a cohort of its own, which
	// assembly would prefer to the cut one.
	MemberCutDraftCarriesCohort MemberCutDeclined = "draft_carries_cohort"
	// MemberCutNoStatedOrder: the list carries no order the plan can state
	// (neither attention rank nor canonical id order).
	MemberCutNoStatedOrder MemberCutDeclined = "no_stated_order"
	// MemberCutNothingCuttable: every member is named by the answer.
	MemberCutNothingCuttable MemberCutDeclined = "nothing_cuttable"
	// MemberCutWouldRemoveAll: fitting would remove every listed member.
	MemberCutWouldRemoveAll MemberCutDeclined = "would_remove_all"
	// MemberCutInsufficient: with every cuttable member gone the answer still
	// overruns on items.
	MemberCutInsufficient MemberCutDeclined = "insufficient"
	// MemberCutBytesRemain: the cut fits the items and the document still
	// overruns on bytes at the ceiling.
	MemberCutBytesRemain MemberCutDeclined = "bytes_remain"
	// MemberCutUnmeasurable: a candidate could not be assembled or marshaled.
	MemberCutUnmeasurable MemberCutDeclined = "unmeasurable"
	// MemberCutInvalidResult: the fitting document failed contract validation.
	MemberCutInvalidResult MemberCutDeclined = "invalid_result"
)

type memberCutAttempt struct {
	Result    InvestigationResult
	Pending   assemblyTelemetry
	Served    bool
	Declined  MemberCutDeclined
	Measured  MeasuredAttempt
	Listed    int
	Kept      int
	Uncited   int
	Basis     contractsv1.ContextFabricNarrowingBasis
	StepKept  bool
	ItemsHeld int
}

// MemberCutEvent is the operator record of ONE member-cut decision. Counts
// and closed values only.
type MemberCutEvent struct {
	Family      QuestionFamily
	Stage       contractsv1.ContextFabricPlanNarrowingStage
	Pass        int
	Overrun     contractsv1.ContextFabricBudgetOverrun
	MaxItems    int
	ItemsBefore int
	ItemsAfter  int
	Listed      int
	Kept        int
	Uncited     int
	Basis       contractsv1.ContextFabricNarrowingBasis
	// ChainRecorded says the plan step was recorded, which is only true when
	// the list the cut started from was the whole population.
	ChainRecorded bool
	Served        bool
	Declined      MemberCutDeclined
}

// memberCutBasis names the order the list is cut in, or false when the list
// carries no order the plan can state truthfully.
func memberCutBasis(cohort *Cohort) (contractsv1.ContextFabricNarrowingBasis, bool) {
	if cohort == nil || len(cohort.Groups) > 0 || contractsv1.ContextFabricCohortIsWalkList(cohort) {
		return "", false
	}
	if cohort.ScoreMeaning != "" {
		return contractsv1.ContextFabricNarrowingBasisAttentionRank, true
	}
	ids := make([]string, 0, len(cohort.Members))
	for _, member := range cohort.Members {
		ids = append(ids, member.Subject.CanonicalID)
	}
	if sort.StringsAreSorted(ids) {
		return contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, true
	}
	return "", false
}

// memberCutOrder returns the order members are cut in, worst first, over the
// members nothing in the answer names: for a ranked list the highest
// AttentionRank first (a member ranking did not run for counts as worst), as
// the Members array stays in pool order and says nothing of attention; for an
// unranked list sorted by canonical id, from the end of the list.
func memberCutOrder(result InvestigationResult) (contractsv1.ContextFabricNarrowingBasis, []int, bool) {
	basis, ok := memberCutBasis(result.Cohort)
	if !ok {
		return "", nil, false
	}
	cited := walkListSubjectCited(result)
	order := make([]int, 0, len(result.Cohort.Members))
	for index, member := range result.Cohort.Members {
		if !cited[member.Subject.CanonicalID] {
			order = append(order, index)
		}
	}
	members := result.Cohort.Members
	sort.SliceStable(order, func(a, b int) bool {
		left, right := members[order[a]], members[order[b]]
		if basis == contractsv1.ContextFabricNarrowingBasisAttentionRank {
			if left.RankingComputed != right.RankingComputed {
				return !left.RankingComputed
			}
			if left.AttentionRank != right.AttentionRank {
				return left.AttentionRank > right.AttentionRank
			}
		}
		return order[a] > order[b]
	})
	return basis, order, true
}

// memberCutOutcomeRow: NARROWED in SCOPE (fewer members listed; each that
// remains is unchanged) because of the ITEM budget, observed. It carries no
// requirement identity: it is the lever's own disclosure, not a derived row.
func memberCutOutcomeRow(served, declared int) RequirementOutcomeRow {
	return contractsv1.ContextFabricWithReductionRefinement(RequirementOutcomeRow{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        contractsv1.ContextFabricAnswerImpactScope,
		CauseOverrun:  contractsv1.ContextFabricBudgetOverrunItems,
		CauseObserved: true,
		Served:        served,
		Declared:      declared,
	})
}

// draftForCutCohort is the draft as the cut cohort's assembly receives it:
// the result-level evidence refs only a cut member carried are removed, as the
// model would not have listed them for a cohort without that member. Every
// other site of the draft is untouched; none of them names a cut member.
func draftForCutCohort(draft InvestigationResult, cut []CohortMember) InvestigationResult {
	if len(draft.EvidenceRefIDs) == 0 || len(cut) == 0 {
		return draft
	}
	only := map[string]bool{}
	for _, member := range cut {
		for _, ref := range member.EvidenceRefIDs {
			only[ref] = true
		}
	}
	probe := draft
	probe.EvidenceRefIDs = nil
	probe.Cohort = nil
	elsewhere := contractsv1.ContextFabricEvidenceRefClosure(probe)
	kept := make([]string, 0, len(draft.EvidenceRefIDs))
	for _, ref := range draft.EvidenceRefIDs {
		if _, stays := elsewhere[ref]; only[ref] && !stays {
			continue
		}
		kept = append(kept, ref)
	}
	draft.EvidenceRefIDs = kept
	return draft
}

// planMemberCut assembles the pass-1 draft over the cohort with the fewest
// uncited members removed that fit the item budget. firstPass is the pending
// of the pass whose document `result` is; params are that pass's params.
func (e *Engine) planMemberCut(
	ctx context.Context,
	principal storage.Principal,
	plan *AnswerPlan,
	params synthesisAssemblyParams,
	firstPass assemblyTelemetry,
	result InvestigationResult,
	budget ResponseBudget,
	measured MeasuredAttempt,
) (memberCutAttempt, error) {
	attempt := memberCutAttempt{Measured: measured}
	if measured.Overrun != contractsv1.ContextFabricBudgetOverrunItems || budget.MaxItems <= 0 {
		attempt.Declined = MemberCutNotItemsAxis
		return attempt, nil
	}
	graphCohort := params.Graph.Cohort
	if params.WorkItemCensus != nil || graphCohort == nil || len(graphCohort.Groups) > 0 || len(graphCohort.Members) < 2 ||
		result.Cohort == nil || len(result.Cohort.Members) != len(graphCohort.Members) {
		attempt.Declined = MemberCutNoCohort
		return attempt, nil
	}
	if firstPass.Draft == nil || clientSynthesisRequested(params.Request) {
		attempt.Declined = MemberCutNoDraft
		return attempt, nil
	}
	if firstPass.Draft.Cohort != nil {
		attempt.Declined = MemberCutDraftCarriesCohort
		return attempt, nil
	}
	basis, order, ok := memberCutOrder(result)
	if !ok {
		attempt.Declined = MemberCutNoStatedOrder
		return attempt, nil
	}
	listed := len(graphCohort.Members)
	attempt.Listed, attempt.Basis, attempt.Uncited = listed, basis, len(order)
	if len(order) == 0 {
		attempt.Declined = MemberCutNothingCuttable
		return attempt, nil
	}
	over := measured.Measurement.Items.Budgeted() - budget.MaxItems
	maxCut := min(len(order), listed-1)
	if over <= 0 {
		attempt.Declined = MemberCutNotItemsAxis
		return attempt, nil
	}
	if over > maxCut {
		if over >= listed && len(order) >= listed {
			attempt.Declined = MemberCutWouldRemoveAll
		} else {
			attempt.Declined = MemberCutInsufficient
		}
		return attempt, nil
	}
	// The plan's member chain runs from the population retrieval found to the
	// members served and every step in it states its own cause. When the list
	// this cut starts from was already shorter than that population (a
	// caller's member limit cut it before planning, a loss no step records),
	// a step recorded here would be read as the cause of the whole drop.
	attempt.StepKept = params.Graph.CohortPopulation <= listed

	// assemble builds the document for a cut of n members: the cut graph, the
	// draft for it, one assembly pass, the lever's own disclosure, finalization.
	assemble := func(n int, pending *assemblyTelemetry) (InvestigationResult, ItemAllocation, assemblyTelemetry, MembershipCardinality, synthesisAssemblyParams, narrowedInput, AnswerPlan, error) {
		drop := map[int]bool{}
		cutMembers := make([]CohortMember, 0, n)
		for _, index := range order[:n] {
			drop[index] = true
			cutMembers = append(cutMembers, graphCohort.Members[index])
		}
		kept := make([]CohortMember, 0, listed-n)
		for index, member := range graphCohort.Members {
			if !drop[index] {
				kept = append(kept, member)
			}
		}
		narrowed := narrowedInputOver(params, params.Graph, params.Facts, copyCohortForRetry(graphCohort), kept, listed, basis)
		stepped := *plan
		stepped.Narrowing = slices.Clone(plan.Narrowing)
		if attempt.StepKept {
			e.recordPlanNarrowingStep(&stepped, PlanNarrowing{
				Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
				Basis:   basis,
				Before:  listed,
				After:   len(kept),
				Overrun: contractsv1.ContextFabricBudgetOverrunItems,
			})
		}
		allocation := AllocateItems(stepped, groupCountOf(narrowed.Graph.Cohort), cohortMemberCount(narrowed.Graph.Cohort))
		cutParams := params.forRetry(narrowed.Graph, narrowed.Facts, allocation)
		cutParams.Plan = stepped
		cutParams.CohortSignalCitations = narrowed.Citations
		draft := draftForCutCohort(*firstPass.Draft, cutMembers)
		cutParams.Draft = &draft
		assembled, consumed, assembledPending, cardinality, err := e.synthesizeAndAssemble(ctx, principal, cutParams)
		if err != nil {
			return InvestigationResult{}, ItemAllocation{}, assemblyTelemetry{}, MembershipCardinality{}, cutParams, narrowed, stepped, err
		}
		// The lever's own disclosure: one sentence naming the rule, one scope
		// row, and partial. None of it is derived from the cohort.
		if sentence, ok := contractsv1.ContextFabricMemberCutLimitation(n, listed, basis == contractsv1.ContextFabricNarrowingBasisAttentionRank); ok {
			composed, displaced := appendBoundedLimitations(assembled.Limitations, []string{sentence})
			assembled.Limitations = composed
			assembled.LimitationsDisplaced += displaced
		}
		assembled.Completeness.Outcomes = appendOutcomeRows(assembled.Completeness.Outcomes, memberCutOutcomeRow(listed-n, listed))
		assembled.Coverage.Partial = true
		if assembled.Status == InvestigationComplete {
			assembled.Status = InvestigationPartial
		}
		target := &assembledPending
		if pending == nil {
			target = nil
		}
		assembled = e.finalizeResult(ctx, principal, assembled, stepped, params.Frame, narrowed.Facts, target, answerPassSecond, cardinality)
		return assembled, consumed, assembledPending, cardinality, cutParams, narrowed, stepped, nil
	}
	fitsItems := func(n int) (bool, error) {
		doc, consumed, _, _, _, _, _, err := assemble(n, nil)
		if err != nil {
			return false, err
		}
		got, err := e.measureAssembledAttempt(ctx, principal, "member_cut", consumed, doc, budget)
		if err != nil {
			return false, err
		}
		return got.Overrun != contractsv1.ContextFabricBudgetOverrunItems, nil
	}
	if ok, err := fitsItems(maxCut); err != nil {
		if errors.Is(err, ErrItemAccounting) {
			return attempt, err
		}
		attempt.Declined = MemberCutUnmeasurable
		return attempt, nil
	} else if !ok {
		attempt.Declined = MemberCutInsufficient
		return attempt, nil
	}
	low, high, best := over, maxCut, maxCut
	for low <= high {
		mid := low + (high-low)/2
		ok, err := fitsItems(mid)
		if err != nil {
			attempt.Declined = MemberCutUnmeasurable
			return attempt, nil
		}
		if ok {
			best, high = mid, mid-1
		} else {
			low = mid + 1
		}
	}

	doc, consumed, pending, cardinality, cutParams, narrowed, stepped, err := assemble(best, &assemblyTelemetry{})
	if err != nil {
		attempt.Declined = MemberCutUnmeasurable
		return attempt, nil
	}
	got, err := e.measureAssembledAttempt(ctx, principal, "member_cut", consumed, doc, budget)
	if err != nil {
		if errors.Is(err, ErrItemAccounting) {
			return attempt, err
		}
		attempt.Declined = MemberCutUnmeasurable
		return attempt, nil
	}
	attempt.Measured, attempt.Kept = got, listed-best
	// This pass is pass two of the investigation: its pending carries the
	// first pass's observation-cover events, published as not served.
	pending.RetryAttempted = firstPass.RetryAttempted
	merged := make([]ReadRequirementObservationCoverEvent, 0, len(firstPass.ObservationCover)+len(pending.ObservationCover))
	merged = append(merged, firstPass.ObservationCover...)
	merged = append(merged, pending.ObservationCover...)
	pending.ObservationCover = merged
	if params.WorkItemCensus == nil {
		ranked := narrowed.Ranked
		pending.CohortRanked = &ranked
	}

	switch {
	case got.Overrun == contractsv1.ContextFabricBudgetFits && got.CertifiedFit():
		doc = e.finalizeResult(ctx, principal, doc, stepped, params.Frame, narrowed.Facts, &pending, answerPassSecond, cardinality)
		if !servableLeverResult(doc) {
			attempt.Declined = MemberCutInvalidResult
			return attempt, nil
		}
		attempt.Result, attempt.Pending, attempt.Served = doc, pending, true
	case got.Overrun == contractsv1.ContextFabricBudgetOverrunBytes:
		// Items fit; bytes overrun the caller's target. The service ceiling
		// decides, over the cut pass's own params, facts and cardinality.
		served, ok, serveErr := e.serveAtServiceCeiling(ctx, principal, &stepped, cutParams, "member_cut", doc, doc, false, consumed, narrowed.Facts, &pending, answerPassThird, answerPassSecond, cardinality, budget, contractsv1.ContextFabricBudgetOverrunBytes)
		if serveErr != nil {
			return attempt, serveErr
		}
		if !ok {
			attempt.Declined = MemberCutBytesRemain
			return attempt, nil
		}
		if !servableLeverResult(served) {
			attempt.Declined = MemberCutInvalidResult
			return attempt, nil
		}
		attempt.Result, attempt.Pending, attempt.Served = served, pending, true
	default:
		attempt.Declined = MemberCutInsufficient
		return attempt, nil
	}
	// The step reaches the plan only for a document that is used.
	if attempt.StepKept {
		e.recordPlanNarrowingStep(plan, PlanNarrowing{
			Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
			Basis:   basis,
			Before:  listed,
			After:   attempt.Kept,
			Overrun: contractsv1.ContextFabricBudgetOverrunItems,
		})
	}
	return attempt, nil
}

// recordMemberCut emits the one decision line of a member-cut application,
// served or declined.
func (e *Engine) recordMemberCut(ctx context.Context, principal storage.Principal, plan *AnswerPlan, before MeasuredAttempt, attempt memberCutAttempt, budget ResponseBudget) {
	if e.telemetry == nil || attempt.Declined == MemberCutNotItemsAxis {
		return
	}
	e.telemetry.RecordMemberCut(ctx, principal, MemberCutEvent{
		Family:        plan.Family,
		Stage:         contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Pass:          answerPassFirst,
		Overrun:       before.Overrun,
		MaxItems:      budget.MaxItems,
		ItemsBefore:   before.Measurement.Items.Budgeted(),
		ItemsAfter:    attempt.Measured.Measurement.Items.Budgeted(),
		Listed:        attempt.Listed,
		Kept:          attempt.Kept,
		Uncited:       attempt.Uncited,
		Basis:         attempt.Basis,
		ChainRecorded: attempt.StepKept && attempt.Served,
		Served:        attempt.Served,
		Declined:      attempt.Declined,
	})
}
