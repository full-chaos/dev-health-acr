package contextfabric

import (
	"context"
	"slices"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// withWorkItemPopulation puts the measured population on a work-item cohort
// and, when the list is shorter than it, says how much shorter. The cohort is
// copied: the stored result is never edited in place. Nothing is set when the
// census holds no population, so an unmeasured read does not claim one.
func withWorkItemPopulation(result InvestigationResult, census *WorkItemTupleCensus) InvestigationResult {
	if census == nil || !census.measuredNow || result.Cohort == nil || result.Cohort.Kind != SubjectWorkItem || census.State == WorkItemMembershipCensusUnmeasured {
		return result
	}
	listed := len(result.Cohort.Members)
	if census.Value < listed || census.Value == 0 || result.Cohort.Population > 0 {
		// A population already on the cohort was written when the answer was
		// served, with the lower-bound reading only that request had; a
		// stored result read again keeps it.
		return result
	}
	lowerBound := census.State == WorkItemMembershipCensusFloor || census.incomplete
	cohort := *result.Cohort
	cohort.Population = census.Value
	cohort.PopulationLowerBound = lowerBound
	result.Cohort = &cohort
	if sentence, ok := contractsv1.ContextFabricWorkItemListedLimitation(listed, census.Value, lowerBound, census.listCutOrServer(), ""); ok {
		composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
	}
	return result
}

// withWorkItemWalkList puts the full member list of a walk on the served
// result. The synthesis and the claims covered only some of its members; the
// list carries every member the walk found, in id order. Applied on the request
// that measured the census: a stored result already holds the list it was
// served with.
func withWorkItemWalkList(result InvestigationResult, census *WorkItemTupleCensus) InvestigationResult {
	if census == nil || census.walkList == nil || result.Cohort == nil || result.Cohort.Kind != SubjectWorkItem {
		return result
	}
	// The members the answer-writing model read are the ones the result holds
	// now (the item ceiling's member budget, or fewer after a fit narrowed the
	// input again).
	read := len(result.Cohort.Members)
	if len(census.walkList.Members) <= read {
		return result
	}
	// The walk's own members carry their titles (the fact reads covered the whole
	// list before the model's input was narrowed) and their ranks in the list.
	cohort := *result.Cohort
	cohort.Members = slices.Clone(census.walkList.Members)
	cohort.Complete, cohort.Truncated = census.walkList.Complete, census.walkList.Truncated
	result.Cohort = &cohort
	if sentence, ok := contractsv1.ContextFabricWorkItemSynthesisCoverageLimitation(read, len(cohort.Members)); ok {
		composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
	}
	return result
}

// listCutOrServer is what bounded the list; a census read from storage has no
// request to ask and names the server's own limit.
func (c *WorkItemTupleCensus) listCutOrServer() contractsv1.ContextFabricWorkItemListCut {
	if c.listCut == "" {
		return contractsv1.ContextFabricWorkItemListCutServer
	}
	return c.listCut
}

// cutWalkListMembers is the byte fit's last lever for a work-item walk: the
// trailing tenth of the listed members that nothing in the answer cites is
// dropped, the population is left as it was, and the N of M sentence is
// restated with the size limit as its cause. A member a claim, a driver or a
// finding names is never dropped, so the cut cannot orphan a citation.
func cutWalkListMembers(result InvestigationResult) (InvestigationResult, bool) {
	if !contractsv1.ContextFabricCohortIsWalkList(result.Cohort) || len(result.Cohort.Members) < 2 {
		return result, false
	}
	cited := map[string]bool{}
	for _, claim := range result.ClaimedFacts {
		cited[claim.Subject.CanonicalID] = true
	}
	for _, driver := range result.Drivers {
		for _, subject := range driver.AffectedSubjects {
			cited[subject.CanonicalID] = true
		}
	}
	for _, findings := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			for _, subject := range finding.Subjects {
				cited[subject.CanonicalID] = true
			}
		}
	}
	// A member whose evidence the answer cites stays listed: the result, its
	// drivers and its findings may cite the evidence of a member they name no
	// subject for, and a cut that dropped it would orphan that citation.
	citedEvidence := map[string]bool{}
	for _, ref := range result.EvidenceRefIDs {
		citedEvidence[ref] = true
	}
	for _, driver := range result.Drivers {
		for _, ref := range driver.EvidenceRefIDs {
			citedEvidence[ref] = true
		}
	}
	for _, findings := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			for _, ref := range finding.EvidenceRefIDs {
				citedEvidence[ref] = true
			}
		}
	}
	for _, member := range result.Cohort.Members {
		if ref, ok := canonicalWorkItemEvidenceRef(member.Subject); ok && citedEvidence[ref] {
			cited[member.Subject.CanonicalID] = true
		}
	}
	// The members the written summary read stay listed too: the coverage
	// sentence names how many listed members it read, and a cut that removed
	// one of them would make that count describe a list the answer no longer has.
	read := 0
	for _, limitation := range result.Limitations {
		if count, ok := contractsv1.ContextFabricWorkItemSynthesisCoverageRead(limitation); ok {
			read = count
			break
		}
	}
	for id := range walkReadSet(result.Cohort.Members, read) {
		cited[id] = true
	}
	// While the summary read fewer members than are listed, the sentence that
	// says so is the record of which members stay; a cut that left only those
	// would remove it and the next cut would no longer know them.
	step := max(1, len(result.Cohort.Members)/10)
	if read > 0 {
		step = min(step, len(result.Cohort.Members)-read-1)
		if step < 1 {
			return result, false
		}
	}
	drop := map[string]bool{}
	for index := len(result.Cohort.Members) - 1; index >= 0 && len(drop) < step; index-- {
		if id := result.Cohort.Members[index].Subject.CanonicalID; !cited[id] {
			drop[id] = true
		}
	}
	if len(drop) == 0 {
		return result, false
	}
	cohort := *result.Cohort
	cohort.Members = make([]CohortMember, 0, len(result.Cohort.Members)-len(drop))
	for _, member := range result.Cohort.Members {
		if !drop[member.Subject.CanonicalID] {
			cohort.Members = append(cohort.Members, member)
		}
	}
	cohort.Complete, cohort.Truncated = false, true
	limitations := slices.DeleteFunc(slices.Clone(result.Limitations), func(limitation string) bool {
		return contractsv1.IsContextFabricWorkItemListedLimitation(limitation) || contractsv1.IsContextFabricWorkItemSynthesisCoverageLimitation(limitation)
	})
	result.Cohort = &cohort
	if read > 0 {
		if sentence, ok := contractsv1.ContextFabricWorkItemSynthesisCoverageLimitation(read, len(cohort.Members)); ok {
			composed, displaced := appendBoundedLimitations(limitations, []string{sentence})
			limitations = composed
			result.LimitationsDisplaced += displaced
		}
	}
	if sentence, ok := contractsv1.ContextFabricWorkItemListedLimitation(len(cohort.Members), cohort.Population, cohort.PopulationLowerBound, contractsv1.ContextFabricWorkItemListCutSize, ""); ok {
		composed, displaced := appendBoundedLimitations(limitations, []string{sentence})
		limitations = composed
		result.LimitationsDisplaced += displaced
	}
	result.Limitations = limitations
	return restrictWorkItemTupleEvidence(result), true
}

// walkReadSet is the members the written summary read out of a listed walk:
// the strongest links first (the tier a repository member's inclusion reason
// states) and then canonical id, the choice synthesisMembers makes from the
// census.
func walkReadSet(members []CohortMember, read int) map[string]bool {
	if read <= 0 || read >= len(members) {
		return map[string]bool{}
	}
	tier := func(member CohortMember) int {
		for _, reason := range member.InclusionReasons {
			for rank, name := range []string{TreeLinkTierNative, TreeLinkTierExplicitText, TreeLinkTierHeuristic} {
				if reason == contractsv1.ContextFabricWorkItemRepositoryMembershipReason(name) {
					return rank
				}
			}
		}
		return 3
	}
	ordered := slices.Clone(members)
	slices.SortStableFunc(ordered, func(a, b CohortMember) int {
		if byTier := tier(a) - tier(b); byTier != 0 {
			return byTier
		}
		return strings.Compare(a.Subject.CanonicalID, b.Subject.CanonicalID)
	})
	set := make(map[string]bool, read)
	for _, member := range ordered[:read] {
		set[member.Subject.CanonicalID] = true
	}
	return set
}

// fitWalkListToBytes cuts the listed members of a work-item walk, one step at
// a time, until the document the route will send fits the byte ceiling. It
// returns false when nothing uncited is left to cut, and the caller falls through
// to its other arms; a measurement that fails is a defect and is returned.
func (e *Engine) fitWalkListToBytes(ctx context.Context, principal storage.Principal, plan *AnswerPlan, frame *QuestionFrame, result InvestigationResult, budget ResponseBudget, measured MeasuredAttempt, facts CanonicalFactBundle, pending *assemblyTelemetry, cardinality MembershipCardinality) (InvestigationResult, bool, error) {
	current := result
	for {
		next, cut := cutWalkListMembers(current)
		if !cut {
			return result, false, nil
		}
		current = next
		finalized := e.finalizeResult(ctx, principal, current, *plan, frame, facts, nil, answerPassSecond, cardinality)
		attempt, err := e.measureAssembledAttempt(ctx, principal, "walk_list_fit", measured.Allocation, e.budgetTrimServedShape(ctx, finalized), budget)
		if err != nil {
			return result, false, err
		}
		if attempt.Overrun == contractsv1.ContextFabricBudgetFits && attempt.CertifiedFit() {
			return e.finalizeResult(ctx, principal, current, *plan, frame, facts, pending, answerPassSecond, cardinality), true, nil
		}
	}
}

// walkBasis is the order the walk keeps and reads members in: the strongest
// link first for a repository, canonical id for a project.
func (c *WorkItemTupleCensus) walkBasis() contractsv1.ContextFabricNarrowingBasis {
	if c != nil && c.anchorKind == SubjectRepository {
		return contractsv1.ContextFabricNarrowingBasisLinkStrengthThenID
	}
	return contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical
}

func stepBasisOr(step, fallback contractsv1.ContextFabricNarrowingBasis) contractsv1.ContextFabricNarrowingBasis {
	if step != "" {
		return step
	}
	return fallback
}

// synthesisMembers picks the members the answer-writing model reads: the
// strongest links first (a repository's walk) and then canonical id, kept in
// the list's own (id) order, ranked from one.
func (c *WorkItemTupleCensus) synthesisMembers(cohort *Cohort, limit int) ([]CohortMember, bool) {
	if cohort == nil || limit <= 0 || len(cohort.Members) <= limit {
		return nil, false
	}
	ordered := slices.Clone(cohort.Members)
	slices.SortStableFunc(ordered, func(a, b CohortMember) int {
		if byTier := treeLinkTierRank(c.linkTier[a.Subject.CanonicalID]) - treeLinkTierRank(c.linkTier[b.Subject.CanonicalID]); byTier != 0 && c.anchorKind == SubjectRepository {
			return byTier
		}
		return strings.Compare(a.Subject.CanonicalID, b.Subject.CanonicalID)
	})
	chosen := map[string]bool{}
	for _, member := range ordered[:limit] {
		chosen[member.Subject.CanonicalID] = true
	}
	kept := make([]CohortMember, 0, limit)
	for _, member := range cohort.Members {
		if chosen[member.Subject.CanonicalID] {
			member.Rank = len(kept) + 1
			kept = append(kept, member)
		}
	}
	return kept, true
}
