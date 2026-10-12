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
// overran the item budget on members the answer does not cite. Every other
// lever that removes members re-synthesizes (the cohort retry) and so needs a
// model call and the deadline for it; when that is declined, claims were the
// only thing that could be cut and an answer whose items are members and
// findings was refused for want of a lever.
//
// The trailing members of the served list that no claim, driver or finding
// names are dropped, the fewest that fit. The cut is recorded as a plan
// narrowing step, so the existing cohort-narrowing disclosure, the membership
// cardinality and the member read rows state it exactly as they state a retry.
// A member the answer cites is never dropped, and nothing but members is cut.

type memberCutAttempt struct {
	Result   InvestigationResult
	Served   bool
	Measured MeasuredAttempt
	Declined ClaimDepthDeclined
	Before   int
	After    int
	Basis    contractsv1.ContextFabricNarrowingBasis
	// Reduced is the cut document when the item axis fits after the cut and
	// the byte axis does not: the byte levers continue from it.
	Reduced InvestigationResult
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
// unranked list sorted by canonical id, from the end of the list. The basis
// states exactly that order.
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

// cutMembers drops the first n members of order. It returns false when order
// holds fewer than n.
func cutMembers(result InvestigationResult, order []int, n int, byRank bool) (InvestigationResult, bool) {
	if n <= 0 || n > len(order) {
		return result, false
	}
	drop := map[int]bool{}
	dropped := []CohortMember{}
	for _, index := range order[:n] {
		drop[index] = true
		dropped = append(dropped, result.Cohort.Members[index])
	}
	cohort := *result.Cohort
	cohort.Members = make([]CohortMember, 0, len(result.Cohort.Members)-n)
	for index, member := range result.Cohort.Members {
		if !drop[index] {
			cohort.Members = append(cohort.Members, member)
		}
	}
	// Rank stays the pool-order value each member held (the validator needs it
	// only to increase). AttentionRank is renumbered dense over the ranked
	// members that remain, in the order they already held, so no member's place
	// relative to another changes and no score is rewritten.
	ranked := make([]int, 0, len(cohort.Members))
	for index, member := range cohort.Members {
		if member.RankingComputed {
			ranked = append(ranked, index)
		}
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		return cohort.Members[ranked[a]].AttentionRank < cohort.Members[ranked[b]].AttentionRank
	})
	for position, index := range ranked {
		cohort.Members[index].AttentionRank = position + 1
	}
	cohort.Complete, cohort.Truncated = false, true
	listed := len(result.Cohort.Members)
	result.Cohort = &cohort
	result.EvidenceRefIDs = withoutCutMemberRefs(result, dropped)
	// The cut states the rule it followed, once, and an outcome row says what
	// kind of loss it is: fewer members, nothing else changed.
	if sentence, ok := contractsv1.ContextFabricMemberCutLimitation(n, listed, byRank); ok {
		composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
	}
	result.Completeness.Outcomes = appendOutcomeRows(result.Completeness.Outcomes, memberCutOutcomeRow(listed-n, listed))
	// A cut answer is partial, as every other model-free lever serves it.
	result.Coverage.Partial = true
	if result.Status == InvestigationComplete {
		result.Status = InvestigationPartial
	}
	return result, true
}

func (e *Engine) planMemberCut(
	ctx context.Context,
	principal storage.Principal,
	plan *AnswerPlan,
	frame *QuestionFrame,
	result InvestigationResult,
	budget ResponseBudget,
	measured MeasuredAttempt,
	facts CanonicalFactBundle,
	pending *assemblyTelemetry,
	pass int,
	population int,
) (memberCutAttempt, error) {
	attempt := memberCutAttempt{Measured: measured}
	if measured.Overrun != contractsv1.ContextFabricBudgetOverrunItems || budget.MaxItems <= 0 {
		attempt.Declined = ClaimDepthNotItemsAxis
		return attempt, nil
	}
	if result.Cohort == nil || len(result.Cohort.Members) < 2 {
		attempt.Declined = ClaimDepthNoCohort
		return attempt, nil
	}
	basis, uncited, ok := memberCutOrder(result)
	if !ok {
		attempt.Declined = ClaimDepthNoCohort
		return attempt, nil
	}
	attempt.Before, attempt.Basis = len(result.Cohort.Members), basis
	// The plan's member chain runs from the population retrieval found to the
	// members served, and every step in it states its own cause. When the list
	// this cut starts from was already shorter than that population (a caller's
	// member limit cut it before the answer was planned, a loss no step
	// records), a step recorded here would be read as the cause of the whole
	// drop. The cut's own sentence states it exactly; the chain is left
	// unrecorded and the cohort sentence says nothing it cannot back.
	recordStep := population <= attempt.Before
	over := measured.Measurement.Items.Budgeted() - budget.MaxItems
	if over <= 0 || len(uncited) == 0 {
		attempt.Declined = ClaimDepthNothingCuttable
		return attempt, nil
	}

	build := func(n int, telemetry *assemblyTelemetry) (InvestigationResult, bool) {
		cut, ok := cutMembers(result, uncited, n, basis == contractsv1.ContextFabricNarrowingBasisAttentionRank)
		if !ok {
			return InvestigationResult{}, false
		}
		cut.Completeness.Outcomes = withoutDerivedAssembledRows(cut.Completeness.Outcomes)
		stepped := *plan
		stepped.Narrowing = slices.Clone(plan.Narrowing)
		if recordStep {
			e.recordPlanNarrowingStep(&stepped, PlanNarrowing{
				Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
				Basis:   basis,
				Before:  attempt.Before,
				After:   attempt.Before - n,
				Overrun: contractsv1.ContextFabricBudgetOverrunItems,
			})
		}
		cardinality, _ := ComputeMembershipCardinality(cut.Cohort, population, stepped.Narrowing)
		return e.finalizeResult(ctx, principal, cut, stepped, frame, facts, telemetry, pass, cardinality), true
	}
	fits := func(n int) (bool, error) {
		doc, ok := build(n, nil)
		if !ok {
			return false, nil
		}
		measurement, err := contractsv1.MeasureContextFabricResponse(e.servedMeasurementShape(ctx, doc))
		if err != nil {
			return false, err
		}
		return measurement.Items.Budgeted() <= budget.MaxItems, nil
	}
	if ok, err := fits(len(uncited)); err != nil || !ok {
		if err != nil {
			attempt.Declined = ClaimDepthUnmeasurable
		} else {
			attempt.Declined = ClaimDepthInsufficient
		}
		return attempt, nil
	}
	low, high, best := min(over, len(uncited)), len(uncited), len(uncited)
	for low <= high {
		mid := low + (high-low)/2
		ok, err := fits(mid)
		if err != nil {
			attempt.Declined = ClaimDepthUnmeasurable
			return attempt, nil
		}
		if ok {
			best, high = mid, mid-1
		} else {
			low = mid + 1
		}
	}
	// Measured WITHOUT the pending telemetry: a candidate the byte ladder may
	// still replace must not leave cover events behind that publication would
	// mark as served. The document that is used is finalized with it once.
	cut, ok := build(best, nil)
	if !ok {
		attempt.Declined = ClaimDepthInsufficient
		return attempt, nil
	}
	attempt.After = attempt.Before - best
	servedMeasured, err := e.measureAssembledAttempt(ctx, principal, "member_cut", measured.Allocation, cut, budget)
	if err != nil {
		if errors.Is(err, ErrItemAccounting) {
			return attempt, err
		}
		attempt.Declined = ClaimDepthUnmeasurable
		return attempt, nil
	}
	attempt.Measured = servedMeasured
	switch {
	case servedMeasured.Overrun == contractsv1.ContextFabricBudgetFits && servedMeasured.CertifiedFit():
		if !servableLeverResult(cut) {
			attempt.Declined = ClaimDepthInvalidResult
			return attempt, nil
		}
		final, built := build(best, pending)
		if !built || !servableLeverResult(final) {
			attempt.Declined = ClaimDepthInvalidResult
			return attempt, nil
		}
		attempt.Result, attempt.Served = final, true
	case servedMeasured.Overrun == contractsv1.ContextFabricBudgetOverrunBytes:
		attempt.Reduced = cut
	default:
		attempt.Declined = ClaimDepthInsufficient
		return attempt, nil
	}
	// The step reaches the plan only for a document that is used: served, or
	// carried on to the byte levers, which finalize against this plan.
	if recordStep {
		e.recordPlanNarrowingStep(plan, PlanNarrowing{
			Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
			Basis:   basis,
			Before:  attempt.Before,
			After:   attempt.After,
			Overrun: contractsv1.ContextFabricBudgetOverrunItems,
		})
	}
	return attempt, nil
}

// memberCutOutcomeRow: NARROWED in SCOPE (fewer members listed; each that
// remains is unchanged) because of the ITEM budget, observed.
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

// withoutCutMemberRefs removes from the result-level evidence refs the refs
// only a cut member carried. A ref some kept site still cites -- a driver, a
// finding, a path or its edges, a candidate, a kept member -- stays, as a
// dropped path's ids leave the drivers that cited them and nothing else.
func withoutCutMemberRefs(result InvestigationResult, dropped []CohortMember) []string {
	if len(result.EvidenceRefIDs) == 0 || len(dropped) == 0 {
		return result.EvidenceRefIDs
	}
	cut := map[string]bool{}
	for _, member := range dropped {
		for _, ref := range member.EvidenceRefIDs {
			cut[ref] = true
		}
	}
	probe := result
	probe.EvidenceRefIDs = nil
	kept := contractsv1.ContextFabricEvidenceRefClosure(probe)
	out := make([]string, 0, len(result.EvidenceRefIDs))
	for _, ref := range result.EvidenceRefIDs {
		if _, stays := kept[ref]; cut[ref] && !stays {
			continue
		}
		out = append(out, ref)
	}
	return out
}

// withoutDerivedAssembledRows removes the assembled-result rows the finalizer
// derives from the member set (the count, the read populations, the ranking):
// each carries a requirement identity and is written once, keyed on that
// identity, so a row derived over the list before the cut would otherwise
// survive it and state the cut list's old size. Rows without a requirement
// identity (a lever's own disclosure) are not derived and stay.
func withoutDerivedAssembledRows(rows []RequirementOutcomeRow) []RequirementOutcomeRow {
	kept := make([]RequirementOutcomeRow, 0, len(rows))
	for _, row := range rows {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult && row.Requirement != "" {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}
