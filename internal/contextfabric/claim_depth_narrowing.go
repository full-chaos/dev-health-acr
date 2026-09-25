package contextfabric

import (
	"context"
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-6743: the ITEM-axis allocation rule -- members before claims.
//
// THE DEFECT (prod, 2026-09-25, acr 163629d2, api
// req_e10f944ad363ca7e2ff2d0f4067f3723). "Which repositories does this
// organization have?" assembled 37 items against max_items=30 at 58,769 bytes
// (inside 65,536): 11 cohort members, 2 claims per repository, 2 narrated
// drivers. Stage 3's only item lever for a cohort was the retry, which halves
// the cohort (narrowSynthesisInput, before/2), so the inventory served 5 of its
// 11 repositories. That is a wrong allocation between collections, not a
// budget problem: the members are the answer; per-member claims are depth.
//
// THE RULE (team-lead GO, 2026-09-25). On an items overrun over a cohort, cut
// the claims ABOUT COHORT MEMBERS first, to the largest per-member cap K that
// fits (water-filling: a member with K or fewer claims loses none). The floor
// is K=1. A claim any driver, finding or member driver cites is never cut; a
// member's other claims are kept in synthesis order. The answer is served
// PARTIAL with the cut disclosed. The cohort retry runs only when K=1 still
// overruns.

// ClaimDepthDeclined is the CLOSED vocabulary of why the claim-depth lever
// did not serve an answer.
type ClaimDepthDeclined string

const (
	ClaimDepthNotApplicable ClaimDepthDeclined = ""
	// ClaimDepthNotItemsAxis: the answer overran on BYTES, or had no item
	// ceiling.
	ClaimDepthNotItemsAxis ClaimDepthDeclined = "not_items_axis"
	// ClaimDepthNoCohort: no cohort members to allocate claims between.
	ClaimDepthNoCohort ClaimDepthDeclined = "no_cohort"
	// ClaimDepthNothingCuttable: no member carries more uncited claims than
	// the one-claim floor admits.
	ClaimDepthNothingCuttable ClaimDepthDeclined = "nothing_cuttable"
	// ClaimDepthInsufficient: the lever ran down to one claim per member and
	// the document still did not fit.
	ClaimDepthInsufficient ClaimDepthDeclined = "insufficient"
	// ClaimDepthUnmeasurable: a cut document could not be marshaled.
	ClaimDepthUnmeasurable ClaimDepthDeclined = "unmeasurable"
)

type claimDepthAttempt struct {
	Result       InvestigationResult
	Served       bool
	Measured     MeasuredAttempt
	Declined     ClaimDepthDeclined
	Members      int
	ClaimsBefore int
	ClaimsAfter  int
	ClaimsCited  int
	PerMember    int
}

// claimDepthPlan is the per-member view of one document's claims.
type claimDepthPlan struct {
	// member maps a claim index to its member's canonical id; claims about
	// anything else are absent and always kept.
	member map[int]string
	cited  map[string]bool
	// citedPerMember counts each member's cited claims.
	citedPerMember map[string]int
	// declared is every claim about a member; longest is the most claims
	// any one member carries.
	declared int
	longest  int
	members  int
	cites    int
}

func newClaimDepthPlan(result InvestigationResult) claimDepthPlan {
	plan := claimDepthPlan{member: map[int]string{}, cited: citedClaimIDs(result), citedPerMember: map[string]int{}}
	if result.Cohort == nil {
		return plan
	}
	members := map[string]bool{}
	for _, member := range result.Cohort.Members {
		members[member.Subject.CanonicalID] = true
	}
	plan.members = len(result.Cohort.Members)
	perMember := map[string]int{}
	for index, claim := range result.ClaimedFacts {
		id := claim.Subject.CanonicalID
		if !members[id] {
			continue
		}
		plan.member[index] = id
		plan.declared++
		perMember[id]++
		if perMember[id] > plan.longest {
			plan.longest = perMember[id]
		}
		if plan.cited[claim.ClaimID] {
			plan.citedPerMember[id]++
			plan.cites++
		}
	}
	return plan
}

// citedClaimIDs is every claim id the document's own prose or structure
// cites. Cutting one would orphan its citation.
func citedClaimIDs(result InvestigationResult) map[string]bool {
	cited := map[string]bool{}
	for _, driver := range result.Drivers {
		for _, id := range driver.ClaimedFactIDs {
			cited[id] = true
		}
	}
	for _, findings := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			for _, id := range finding.ClaimedFactIDs {
				cited[id] = true
			}
		}
	}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			for _, driver := range member.Drivers {
				for _, id := range driver.SourceClaimedFactIDs {
					cited[id] = true
				}
			}
		}
	}
	return cited
}

// cut returns a COPY of claims keeping, per member, every cited claim and its
// other claims in order while the member holds fewer than perMember.
func (p claimDepthPlan) cut(claims []ClaimedFact, perMember int) ([]ClaimedFact, int) {
	kept := make([]ClaimedFact, 0, len(claims))
	held := map[string]int{}
	served := 0
	for index, claim := range claims {
		id, isMember := p.member[index]
		if !isMember {
			kept = append(kept, claim)
			continue
		}
		if p.cited[claim.ClaimID] {
			kept = append(kept, claim)
			served++
			continue
		}
		if held[id]+p.citedPerMember[id] < perMember {
			held[id]++
			kept = append(kept, claim)
			served++
		}
	}
	return kept, served
}

func (p claimDepthPlan) apply(result InvestigationResult, perMember int) (InvestigationResult, int, bool) {
	claims, served := p.cut(result.ClaimedFacts, perMember)
	if served >= p.declared {
		return result, served, false
	}
	sentence, ok := contractsv1.ContextFabricClaimDepthLimitation(served, p.declared, p.members, perMember)
	if !ok {
		return result, served, false
	}
	result.ClaimedFacts = claims
	result.Completeness.Outcomes = appendOutcomeRows(result.Completeness.Outcomes, claimDepthOutcomeRow(served, p.declared))
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	result.Coverage.Partial = true
	if result.Status == InvestigationComplete {
		result.Status = InvestigationPartial
	}
	return result, served, true
}

// claimDepthOutcomeRow: NARROWED in DEPTH (every member stays; each carries
// fewer claims) because of the ITEM ceiling, observed here. Unattributed for
// the same reason as the row lever's: the cap applies across every member.
func claimDepthOutcomeRow(served, declared int) RequirementOutcomeRow {
	return contractsv1.ContextFabricWithReductionRefinement(RequirementOutcomeRow{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        contractsv1.ContextFabricAnswerImpactDepth,
		CauseOverrun:  contractsv1.ContextFabricBudgetOverrunItems,
		CauseObserved: true,
		Served:        served,
		Declared:      declared,
	})
}

// planClaimDepthNarrowing searches the LARGEST per-member cap whose finalized
// document fits, then re-finalizes and re-measures it through the same gate
// every stage-3 exit uses.
func (e *Engine) planClaimDepthNarrowing(
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
	if _, floor := depth.cut(result.ClaimedFacts, 1); floor >= depth.declared {
		attempt.Declined = ClaimDepthNothingCuttable
		return attempt, nil
	}

	fits := func(perMember int) (contractsv1.ContextFabricResponseMeasurement, bool, error) {
		cut, _, ok := depth.apply(result, perMember)
		if !ok {
			return contractsv1.ContextFabricResponseMeasurement{}, false, nil
		}
		cut = e.finalizeResult(ctx, principal, cut, *plan, frame, facts, nil, pass, cardinality)
		measurement, err := contractsv1.MeasureContextFabricResponse(servedMeasurementShape(cut))
		return measurement, err == nil && measurement.Overrun(budget) == contractsv1.ContextFabricBudgetFits, err
	}
	// Largest fitting cap in [1, longest-1]. Items fall monotonically with
	// the cap; the final measurement below is the gate.
	low, high, best := 1, depth.longest-1, 0
	var smallest contractsv1.ContextFabricResponseMeasurement
	for low <= high {
		mid := low + (high-low)/2
		measurement, ok, err := fits(mid)
		if err != nil {
			attempt.Declined = ClaimDepthUnmeasurable
			return attempt, nil
		}
		if mid == 1 {
			smallest = measurement
		}
		if ok {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == 0 {
		if smallest.Bytes == 0 {
			if measurement, _, err := fits(1); err == nil {
				smallest = measurement
			}
		}
		_, served := depth.cut(result.ClaimedFacts, 1)
		attempt.Declined = ClaimDepthInsufficient
		attempt.ClaimsAfter, attempt.PerMember = served, 1
		attempt.Measured.Measurement = smallest
		attempt.Measured.Overrun = smallest.Overrun(budget)
		return attempt, nil
	}

	cut, served, _ := depth.apply(result, best)
	cut = e.finalizeResult(ctx, principal, cut, *plan, frame, facts, pending, pass, cardinality)
	attempt.ClaimsAfter, attempt.PerMember = served, best
	servedMeasured, err := e.measureAssembledAttempt(ctx, principal, "claim_depth_narrowing", measured.Allocation, cut, budget)
	if err != nil {
		if errors.Is(err, ErrItemAccounting) {
			return attempt, err
		}
		attempt.Declined = ClaimDepthUnmeasurable
		return attempt, nil
	}
	attempt.Measured = servedMeasured
	if servedMeasured.Overrun != contractsv1.ContextFabricBudgetFits || !servedMeasured.CertifiedFit() {
		attempt.Declined = ClaimDepthInsufficient
		return attempt, nil
	}
	attempt.Result = cut
	attempt.Served = true
	return attempt, nil
}

// ClaimDepthNarrowingEvent is the operator record of ONE claim-depth lever
// application. Counts and closed values only.
type ClaimDepthNarrowingEvent struct {
	Family       QuestionFamily
	Stage        contractsv1.ContextFabricPlanNarrowingStage
	Pass         int
	Overrun      contractsv1.ContextFabricBudgetOverrun
	MaxItems     int
	ItemsBefore  int
	ItemsAfter   int
	Members      int
	ClaimsBefore int
	ClaimsAfter  int
	ClaimsCited  int
	PerMemberCap int
	Served       bool
	Declined     ClaimDepthDeclined
}

// recordClaimDepthNarrowing emits one lever application. A lever that never
// ran (not the item axis) emits nothing.
func (e *Engine) recordClaimDepthNarrowing(ctx context.Context, principal storage.Principal, plan *AnswerPlan, pass int, before MeasuredAttempt, attempt claimDepthAttempt, budget ResponseBudget) {
	if e.telemetry == nil || attempt.Declined == ClaimDepthNotItemsAxis {
		return
	}
	e.telemetry.RecordClaimDepthNarrowing(ctx, principal, ClaimDepthNarrowingEvent{
		Family:       plan.Family,
		Stage:        contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Pass:         pass,
		Overrun:      before.Overrun,
		MaxItems:     budget.MaxItems,
		ItemsBefore:  before.Measurement.Items.Budgeted(),
		ItemsAfter:   attempt.Measured.Measurement.Items.Budgeted(),
		Members:      attempt.Members,
		ClaimsBefore: attempt.ClaimsBefore,
		ClaimsAfter:  attempt.ClaimsAfter,
		ClaimsCited:  attempt.ClaimsCited,
		PerMemberCap: attempt.PerMember,
		Served:       attempt.Served,
		Declined:     attempt.Declined,
	})
}
