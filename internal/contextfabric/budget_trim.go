package contextfabric

import (
	"context"
	"errors"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The last item-axis lever. It runs where stage 3 would otherwise refuse an
// items overrun because no re-synthesis is possible in the time left (or the
// re-synthesis still overran): the answer is shortened deterministically and
// served partial, with one fixed disclosure, instead of a 413.
//
// Order, fixed: (1) the claims about cohort members that nothing cites; (2) if
// that is not enough, the cited ones too, with their ids removed from the
// drivers and findings that cited them. Members, drivers, findings and the
// claims about anything but a member (the census count) are never cut. The
// cut keeps the list's own order and reads no map order, so one input gives
// one answer. A refusal remains only when the answer without those claims
// still does not fit or does not validate.

type budgetTrimStep int

const (
	budgetTrimUncited budgetTrimStep = iota
	budgetTrimCited
)

var budgetTrimSteps = [...]budgetTrimStep{budgetTrimUncited, budgetTrimCited}

func budgetTrimCut(result InvestigationResult, step budgetTrimStep) (InvestigationResult, int, int, bool) {
	depth := newClaimDepthPlan(result)
	if depth.declared == 0 {
		return result, 0, 0, false
	}
	kept := make([]ClaimedFact, 0, len(result.ClaimedFacts))
	dropped := map[string]bool{}
	var anchors map[string]bool
	if step == budgetTrimCited {
		anchors = budgetTrimCitationAnchors(result)
	}
	for index, claim := range result.ClaimedFacts {
		_, isMember := depth.member[index]
		if isMember && !anchors[claim.ClaimID] && (step == budgetTrimCited || !depth.cited[claim.ClaimID]) {
			dropped[claim.ClaimID] = true
			continue
		}
		kept = append(kept, claim)
	}
	if len(dropped) == 0 {
		return result, depth.declared, depth.declared, false
	}
	result.ClaimedFacts = kept
	if step == budgetTrimCited {
		result = budgetTrimStripCitations(result, dropped)
	}
	return result, depth.declared - len(dropped), depth.declared, true
}

// budgetTrimCitationAnchors is the first claim each driver, finding and member
// driver cites, in the list's own order. It stays when cited claims are cut, so
// a judgment that must cite a claim still cites one and nothing is orphaned.
func budgetTrimCitationAnchors(result InvestigationResult) map[string]bool {
	anchors := map[string]bool{}
	first := func(ids []string) {
		if len(ids) > 0 {
			anchors[ids[0]] = true
		}
	}
	for _, driver := range result.Drivers {
		first(driver.ClaimedFactIDs)
	}
	for _, findings := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			first(finding.ClaimedFactIDs)
		}
	}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			for _, driver := range member.Drivers {
				first(driver.SourceClaimedFactIDs)
			}
		}
	}
	return anchors
}

func budgetTrimStripCitations(result InvestigationResult, dropped map[string]bool) InvestigationResult {
	strip := func(ids []string) []string {
		if ids == nil {
			return nil
		}
		return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return dropped[id] })
	}
	result.Drivers = slices.Clone(result.Drivers)
	for index := range result.Drivers {
		result.Drivers[index].ClaimedFactIDs = strip(result.Drivers[index].ClaimedFactIDs)
	}
	for _, list := range []*[]Finding{&result.RemainingWork, &result.ReadinessGaps, &result.Conflicts} {
		*list = slices.Clone(*list)
		for index := range *list {
			(*list)[index].ClaimedFactIDs = strip((*list)[index].ClaimedFactIDs)
		}
	}
	if result.Cohort != nil {
		cohort := copyCohortForRetry(result.Cohort)
		for member := range cohort.Members {
			for driver := range cohort.Members[member].Drivers {
				cohort.Members[member].Drivers[driver].SourceClaimedFactIDs = strip(cohort.Members[member].Drivers[driver].SourceClaimedFactIDs)
			}
		}
		result.Cohort = cohort
	}
	return result
}

func budgetTrimDisclose(result InvestigationResult, served, declared int) (InvestigationResult, bool) {
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{contractsv1.ContextFabricBudgetTrimClaimedFactsLimitation})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	result.Completeness.Outcomes = appendOutcomeRows(result.Completeness.Outcomes, claimDepthOutcomeRow(served, declared))
	result.Coverage.Partial = true
	if result.Status == InvestigationComplete {
		result.Status = InvestigationPartial
	}
	return result, true
}

// planBudgetTrim returns the first step's document that fits, re-finalized
// and re-measured through the gate every stage-3 exit uses.
func (e *Engine) planBudgetTrim(
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
	cardinality MembershipCardinality,
) (claimDepthAttempt, error) {
	depth := newClaimDepthPlan(result)
	attempt := claimDepthAttempt{Measured: measured, Members: depth.members, ClaimsBefore: depth.declared, ClaimsAfter: depth.declared, ClaimsCited: depth.cites}
	if measured.Overrun != contractsv1.ContextFabricBudgetOverrunItems || budget.MaxItems <= 0 {
		attempt.Declined = ClaimDepthNotItemsAxis
		return attempt, nil
	}
	if depth.members == 0 {
		attempt.Declined = ClaimDepthNoCohort
		return attempt, nil
	}
	attempt.Declined = ClaimDepthNothingCuttable
	for _, step := range budgetTrimSteps {
		cut, served, declared, ok := budgetTrimCut(result, step)
		if !ok {
			continue
		}
		cut, _ = budgetTrimDisclose(cut, served, declared)
		disclosed := cut
		cut = e.finalizeResult(ctx, principal, disclosed, *plan, frame, facts, nil, pass, cardinality)
		attempt.ClaimsAfter = served
		trimmed, err := e.measureAssembledAttempt(ctx, principal, "budget_trim", measured.Allocation, e.budgetTrimServedShape(ctx, cut), budget)
		if err != nil {
			if errors.Is(err, ErrItemAccounting) {
				return attempt, err
			}
			attempt.Declined = ClaimDepthUnmeasurable
			return attempt, nil
		}
		attempt.Measured = trimmed
		if trimmed.Overrun != contractsv1.ContextFabricBudgetFits || !trimmed.CertifiedFit() {
			attempt.Declined = ClaimDepthInsufficient
			continue
		}
		if !servableLeverResult(cut) {
			attempt.Declined = ClaimDepthInvalidResult
			continue
		}
		// Only the document that is served records its cover events.
		cut = e.finalizeResult(ctx, principal, disclosed, *plan, frame, facts, pending, pass, cardinality)
		attempt.Result = cut
		attempt.Served = true
		attempt.Declined = ClaimDepthNotApplicable
		return attempt, nil
	}
	return attempt, nil
}

// budgetTrimServedShape is the trimmed document in the state finalizeServed
// will serve it: the server completeness correction applied, and the census
// repository-scope limitation present when this call recorded one. Fitting
// the budget is decided on that document, so the final route assertion cannot
// refuse what this lever served. The correction only moves status and the
// completeness block; it never adds a charged item.
func (e *Engine) budgetTrimServedShape(ctx context.Context, result InvestigationResult) InvestigationResult {
	if WorkItemCensusRepositoryScopeRecorded(ctx) {
		composed, displaced := appendBoundedLimitations(result.Limitations, []string{contractsv1.ContextFabricWorkItemCensusRepositoryScopeLimitation})
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
	}
	result.Completeness = ComputeAnswerCompleteness(result)
	return ApplyServerCompletenessAuthority(result, e.serverCompletenessAuthorityEnabled, e.serverCompletenessAuthoritySymmetricEnabled, DeriveCompletenessAuthority(result))
}
