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

// cutTrailingMembers drops the last n uncited members. It returns false when
// fewer than n are uncited.
func cutTrailingMembers(result InvestigationResult, uncited []int, n int) (InvestigationResult, bool) {
	if n <= 0 || n > len(uncited) {
		return result, false
	}
	drop := map[int]bool{}
	for _, index := range uncited[len(uncited)-n:] {
		drop[index] = true
	}
	cohort := *result.Cohort
	cohort.Members = make([]CohortMember, 0, len(result.Cohort.Members)-n)
	for index, member := range result.Cohort.Members {
		if !drop[index] {
			cohort.Members = append(cohort.Members, member)
		}
	}
	// Both orders stay dense, as the cohort validator requires of a served
	// list: Rank is the list position; AttentionRank is renumbered over the
	// ranked members that remain, in the order they already held, so no
	// member's place relative to another changes and no score is rewritten.
	for index := range cohort.Members {
		cohort.Members[index].Rank = index + 1
	}
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
	result.Cohort = &cohort
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
	basis, ok := memberCutBasis(result.Cohort)
	if !ok {
		attempt.Declined = ClaimDepthNoCohort
		return attempt, nil
	}
	attempt.Before, attempt.Basis = len(result.Cohort.Members), basis
	cited := walkListSubjectCited(result)
	uncited := make([]int, 0, len(result.Cohort.Members))
	for index, member := range result.Cohort.Members {
		if !cited[member.Subject.CanonicalID] {
			uncited = append(uncited, index)
		}
	}
	over := measured.Measurement.Items.Budgeted() - budget.MaxItems
	if over <= 0 || len(uncited) == 0 {
		attempt.Declined = ClaimDepthNothingCuttable
		return attempt, nil
	}

	build := func(n int, telemetry *assemblyTelemetry) (InvestigationResult, bool) {
		cut, ok := cutTrailingMembers(result, uncited, n)
		if !ok {
			return InvestigationResult{}, false
		}
		stepped := *plan
		stepped.Narrowing = slices.Clone(plan.Narrowing)
		e.recordPlanNarrowingStep(&stepped, PlanNarrowing{
			Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
			Basis:   basis,
			Before:  attempt.Before,
			After:   attempt.Before - n,
			Overrun: contractsv1.ContextFabricBudgetOverrunItems,
		})
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
	cut, ok := build(best, pending)
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
		attempt.Result, attempt.Served = cut, true
	case servedMeasured.Overrun == contractsv1.ContextFabricBudgetOverrunBytes:
		attempt.Reduced = cut
	default:
		attempt.Declined = ClaimDepthInsufficient
		return attempt, nil
	}
	// The step reaches the plan only for a document that is used: served, or
	// carried on to the byte levers, which finalize against this plan.
	e.recordPlanNarrowingStep(plan, PlanNarrowing{
		Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Basis:   basis,
		Before:  attempt.Before,
		After:   attempt.After,
		Overrun: contractsv1.ContextFabricBudgetOverrunItems,
	})
	return attempt, nil
}
