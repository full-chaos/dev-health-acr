package contextfabric

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// serverItemCeiling is the server's default answer item ceiling.
const serverItemCeiling = 30

func cutTestMembers(t *testing.T, count int, tier func(i int) string) []TreeWorkItemMember {
	t.Helper()
	members := make([]TreeWorkItemMember, 0, count)
	for i := 0; i < count; i++ {
		members = append(members, treeMember(t, fmt.Sprintf("ENG-%04d", i), tier(i)))
	}
	return members
}

func listedSentence(limitations []string) string {
	for _, limitation := range limitations {
		if contractsv1.IsContextFabricWorkItemListedLimitation(limitation) {
			return limitation
		}
	}
	return ""
}

func synthesisCoverageSentence(limitations []string) string {
	for _, limitation := range limitations {
		if contractsv1.IsContextFabricWorkItemSynthesisCoverageLimitation(limitation) {
			return limitation
		}
	}
	return ""
}

func requestCappedAt(cap int) InvestigationRequest {
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = cap
	return request
}

func projectedCohortOf(t *testing.T, result InvestigationResult) *contractsv1.ContextFabricProjectedCohort {
	t.Helper()
	projection := answerprojection.Project(result, answerprojection.Budget{})
	if projection.Cohort == nil {
		t.Fatalf("the projection carries no cohort")
	}
	return projection.Cohort
}

func TestADefaultCallListsEveryMemberAndSaysWhatTheSummaryRead(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 20, func(int) string { return TreeLinkTierNative })
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 20}})
	if run.err != nil {
		t.Fatal(run.err)
	}
	if listed := len(run.result.Cohort.Members); listed != 20 {
		t.Fatalf("listed = %d, want all 20", listed)
	}
	if listedSentence(run.result.Limitations) != "" || limitationsContain(run.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation) {
		t.Fatalf("a list holding every member says it is cut: %v", run.result.Limitations)
	}
	want, _ := contractsv1.ContextFabricWorkItemSynthesisCoverageLimitation(14, 20)
	if got := synthesisCoverageSentence(run.result.Limitations); got != want {
		t.Fatalf("synthesis sentence = %q, want %q", got, want)
	}
	cohort := projectedCohortOf(t, run.result)
	if cohort.Total != 20 || cohort.Population != 20 || cohort.PopulationLowerBound || len(cohort.Members) != 20 {
		t.Fatalf("projected total=%d population=%d members=%d, want 20, 20, 20", cohort.Total, cohort.Population, len(cohort.Members))
	}
	counts := contractsv1.CountContextFabricResultItems(run.result)
	if counts.WalkCohortMembers != 20 || counts.CohortMembers != 0 || counts.Budgeted() > serverItemCeiling {
		t.Fatalf("item counts = %+v: the list is a walk bucket outside the item ceiling", counts)
	}
}

func TestARequestCapCutNamesNOfMWithoutTheTierSentence(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 20, func(int) string { return TreeLinkTierNative })
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, request: requestCappedAt(5), walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 20}})
	if run.err != nil {
		t.Fatal(run.err)
	}
	want, _ := contractsv1.ContextFabricWorkItemListedLimitation(5, 20, false, contractsv1.ContextFabricWorkItemListCutRequest, "")
	if got := listedSentence(run.result.Limitations); got != want || len(run.result.Cohort.Members) != 5 {
		t.Fatalf("listed %d, sentence = %q, want 5 and %q", len(run.result.Cohort.Members), got, want)
	}
	if limitationsContain(run.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation) {
		t.Fatalf("a cut inside one tier says lower tiers were cut first: %v", run.result.Limitations)
	}
	if len(run.walks) != 1 || run.walks[0].Members != 5 || run.walks[0].Population != 20 || !run.walks[0].Truncated {
		t.Fatalf("walk decision line = %+v, want members 5, population 20, truncated true", run.walks)
	}
	cohort := projectedCohortOf(t, run.result)
	if cohort.Population != 20 || cohort.PopulationLowerBound || len(cohort.Members) != 5 {
		t.Fatalf("projected population = %d (lower bound %v), members = %d; want 20, false, 5", cohort.Population, cohort.PopulationLowerBound, len(cohort.Members))
	}
}

func TestAServerCapCutListsTwoHundredOfTheLinkedMembers(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 250, func(int) string { return TreeLinkTierNative })
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, request: requestCappedAt(200), walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 250}})
	if run.err != nil {
		t.Fatal(run.err)
	}
	want, _ := contractsv1.ContextFabricWorkItemListedLimitation(200, 250, false, contractsv1.ContextFabricWorkItemListCutServer, "")
	if got := listedSentence(run.result.Limitations); got != want || len(run.result.Cohort.Members) != 200 {
		t.Fatalf("listed %d, sentence = %q, want 200 and %q", len(run.result.Cohort.Members), got, want)
	}
}

func TestAMixedTierCutNamesTheTierOrderAndNOfM(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 20, func(i int) string {
		if i < 10 {
			return TreeLinkTierNative
		}
		return TreeLinkTierHeuristic
	})
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, request: requestCappedAt(5), walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 20}})
	if run.err != nil {
		t.Fatal(run.err)
	}
	if !limitationsContain(run.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation) {
		t.Fatalf("a cut that dropped weaker links first does not say so: %v", run.result.Limitations)
	}
	if listedSentence(run.result.Limitations) == "" {
		t.Fatalf("a cut list carries no N of M sentence: %v", run.result.Limitations)
	}
}

func TestAListHoldingEveryMemberStatesNoCutAndTheTotalIsThePopulation(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 5, func(int) string { return TreeLinkTierNative })
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 5}})
	if run.err != nil {
		t.Fatal(run.err)
	}
	for _, limitation := range run.result.Limitations {
		if strings.Contains(limitation, "Not every member is listed") || contractsv1.IsContextFabricWorkItemSynthesisCoverageLimitation(limitation) {
			t.Fatalf("a complete list says it is cut: %q", limitation)
		}
	}
	cohort := projectedCohortOf(t, run.result)
	if cohort.Total != 5 || cohort.Population != 5 {
		t.Fatalf("total = %d population = %d, want 5 and 5", cohort.Total, cohort.Population)
	}
}

func TestACutOverAnIncompleteCensusSaysAtLeast(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 20, func(int) string { return TreeLinkTierNative })
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, request: requestCappedAt(5), walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 20, Truncated: true}})
	if run.err != nil {
		t.Fatal(run.err)
	}
	if got := listedSentence(run.result.Limitations); !strings.Contains(got, " of at least 20 members") {
		t.Fatalf("an incomplete census is stated as exact: %q", got)
	}
	if cohort := projectedCohortOf(t, run.result); !cohort.PopulationLowerBound {
		t.Fatalf("an incomplete population is not marked as a lower bound")
	}
}

func TestARestrictedCallersCutNamesOnlyWhatTheCallerMayRead(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 20, func(int) string { return TreeLinkTierNative })
	run := func(denied int) InvestigationResult {
		r := runRepositoryTree(t, repositoryTreeCase{
			maxItems:  serverItemCeiling,
			request:   requestCappedAt(5),
			principal: storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}},
			walk:      TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 20 + denied, Denied: denied},
		})
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.result
	}
	none, hidden := run(0), run(7)
	if none.Cohort.Population != hidden.Cohort.Population || listedSentence(none.Limitations) != listedSentence(hidden.Limitations) {
		t.Fatalf("hidden members change the answer: population %d vs %d, %q vs %q", none.Cohort.Population, hidden.Cohort.Population, listedSentence(none.Limitations), listedSentence(hidden.Limitations))
	}
	if none.Cohort.Population != 20 {
		t.Fatalf("population = %d, want the 20 the caller may read", none.Cohort.Population)
	}
}

func TestTheByteFitCutsOnlyUncitedTrailingMembersAndRestatesTheSentence(t *testing.T) {
	members := make([]CohortMember, 0, 20)
	for i := 0; i < 20; i++ {
		members = append(members, CohortMember{Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item:%02d", i), Label: fmt.Sprintf("W-%02d", i)}, Rank: i + 1})
	}
	cited := members[19].Subject
	result := InvestigationResult{
		Cohort:       &Cohort{Kind: SubjectWorkItem, Members: members, Population: 20, Complete: true},
		ClaimedFacts: []ClaimedFact{{Subject: cited}},
	}
	cut, ok := cutWalkListMembers(result)
	if !ok || len(cut.Cohort.Members) != 18 {
		t.Fatalf("cut = %v with %d members, want a cut to 18 (two uncited trailing members dropped)", ok, len(cut.Cohort.Members))
	}
	if cut.Cohort.Members[len(cut.Cohort.Members)-1].Subject != cited {
		t.Fatalf("a cited member was dropped")
	}
	if want, _ := contractsv1.ContextFabricWorkItemListedLimitation(18, 20, false, contractsv1.ContextFabricWorkItemListCutSize, ""); listedSentence(cut.Limitations) != want {
		t.Fatalf("sentence = %q, want %q", listedSentence(cut.Limitations), want)
	}
	if again, ok := cutWalkListMembers(InvestigationResult{Cohort: &Cohort{Kind: SubjectWorkItem, Members: members[:1], Population: 20}}); ok || len(again.Cohort.Members) != 1 {
		t.Fatalf("a one-member list was cut")
	}
	if _, ok := cutWalkListMembers(InvestigationResult{Cohort: &Cohort{Kind: SubjectWorkItem, Members: members}}); ok {
		t.Fatalf("a work-item cohort with no measured population was cut as a walk list")
	}
}

func TestReuseRefusesAStoredTierSentenceThatTheCutNoLongerSupports(t *testing.T) {
	census := &WorkItemTupleCensus{State: WorkItemMembershipCensusExact}
	stored := func(limitations ...string) InvestigationResult {
		return InvestigationResult{Limitations: limitations, SubjectResolution: SubjectResolution{Committed: []SubjectRef{repositoryWorkItemAnchor}}}
	}
	current := func(lowerTierCut bool) WorkItemMembershipResult {
		return WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, LowerTierCut: lowerTierCut}}
	}
	tier := contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation
	if workItemReuseMembershipEqual(stored(tier), census, current(false)) {
		t.Error("a stored tier sentence was reused after the cut stopped dropping a weaker tier")
	}
	if workItemReuseMembershipEqual(stored(), census, current(true)) {
		t.Error("an answer with no tier sentence was reused after the cut began dropping a weaker tier")
	}
	if !workItemReuseMembershipEqual(stored(tier), census, current(true)) || !workItemReuseMembershipEqual(stored(), census, current(false)) {
		t.Error("an answer whose tier sentence still holds was refused")
	}
}

// A result stored before the population existed is served by id exactly as it was stored: no
// population, no N of M sentence, nothing computed from a re-read census (which does not carry
// whether the read was partial), whatever limitations the stored result holds.
func TestAResultStoredBeforeThePopulationExistedIsServedAsItWasStored(t *testing.T) {
	members := make([]CohortMember, 0, 5)
	for i := 0; i < 5; i++ {
		members = append(members, CohortMember{Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item:%02d", i), Label: fmt.Sprintf("W-%02d", i)}, Rank: i + 1})
	}
	for name, limitations := range map[string][]string{
		"partial read":  {contractsv1.ContextFabricWorkItemRepositoryPartialLimitation},
		"no limitation": {},
	} {
		stored := InvestigationResult{Cohort: &Cohort{Kind: SubjectWorkItem, Members: members}, Limitations: limitations}
		census := &WorkItemTupleCensus{State: WorkItemMembershipCensusExact, Value: 20, Retained: 5}
		served := withWorkItemPopulation(stored, census)
		if served.Cohort.Population != 0 || served.Cohort.PopulationLowerBound || len(served.Limitations) != len(limitations) || listedSentence(served.Limitations) != "" {
			t.Fatalf("%s: a legacy stored result was given population %d (lower bound %v) and limitations %v", name, served.Cohort.Population, served.Cohort.PopulationLowerBound, served.Limitations)
		}
	}
}

func TestTheByteFitKeepsTheMembersTheSummaryReadAndRestatesWhatItCovered(t *testing.T) {
	members := make([]CohortMember, 0, 40)
	for i := 0; i < 40; i++ {
		tier := contractsv1.ContextFabricWorkItemRepositoryTierHeuristic
		if i >= 30 {
			tier = contractsv1.ContextFabricWorkItemRepositoryTierNative
		}
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item:%02d", i), Label: fmt.Sprintf("W-%02d", i)},
			Rank:             i + 1,
			InclusionReasons: []string{contractsv1.ContextFabricWorkItemRepositoryMembershipReason(tier)},
		})
	}
	// The summary read the 10 native members (the last ten ids), so the trailing
	// members the byte fit would drop first are the ones it read.
	coverage, _ := contractsv1.ContextFabricWorkItemSynthesisCoverageLimitation(10, 40)
	result := InvestigationResult{
		Cohort:      &Cohort{Kind: SubjectWorkItem, Members: members, Population: 40, Complete: true},
		Limitations: []string{coverage},
	}
	listed := 40
	for {
		cut, ok := cutWalkListMembers(result)
		if !ok {
			break
		}
		result = cut
		listed = len(cut.Cohort.Members)
		read, found := 0, false
		for _, limitation := range cut.Limitations {
			if count, ok := contractsv1.ContextFabricWorkItemSynthesisCoverageRead(limitation); ok {
				read, found = count, true
			}
		}
		if listed > 10 && (!found || read != 10) {
			t.Fatalf("after a cut to %d members the coverage sentence read=%d found=%v, want 10 of %d", listed, read, found, listed)
		}
		want, _ := contractsv1.ContextFabricWorkItemSynthesisCoverageLimitation(10, listed)
		if listed > 10 && !slices.Contains(cut.Limitations, want) {
			t.Fatalf("limitations %q lack %q", cut.Limitations, want)
		}
	}
	kept := map[string]bool{}
	for _, member := range result.Cohort.Members {
		kept[member.Subject.CanonicalID] = true
	}
	for i := 30; i < 40; i++ {
		if !kept[fmt.Sprintf("work_item:%02d", i)] {
			t.Fatalf("a member the summary read was cut: work_item:%02d (listed %d)", i, listed)
		}
	}
	if listed != 11 {
		t.Fatalf("the fit stopped at %d members, want 11 (the 10 the summary read and one more)", listed)
	}
}

func TestTheByteFitReturnsAMeasurementDefectInsteadOfFallingThrough(t *testing.T) {
	members := make([]CohortMember, 0, 20)
	for i := 0; i < 20; i++ {
		members = append(members, CohortMember{Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item:%02d", i), Label: fmt.Sprintf("W-%02d", i)}, Rank: i + 1})
	}
	// A member whose score cannot be encoded makes the document unmeasurable: a
	// defect of the answer, not an overrun the fit can cut its way out of.
	nan := math.NaN()
	for i := range members {
		members[i].Score = &nan
	}
	engine := &Engine{}
	result := InvestigationResult{Cohort: &Cohort{Kind: SubjectWorkItem, Members: members, Population: 20}}
	_, fitted, fitErr := engine.fitWalkListToBytes(context.Background(), storage.Principal{}, &AnswerPlan{}, nil, result, ResponseBudget{}, MeasuredAttempt{}, CanonicalFactBundle{}, nil, MembershipCardinality{})
	if fitted || fitErr == nil {
		t.Fatalf("fitted=%v err=%v, want the measurement defect returned", fitted, fitErr)
	}
}
