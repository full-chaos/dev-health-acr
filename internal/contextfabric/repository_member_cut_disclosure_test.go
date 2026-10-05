package contextfabric

import (
	"fmt"
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

func projectedCohortOf(t *testing.T, result InvestigationResult) *contractsv1.ContextFabricProjectedCohort {
	t.Helper()
	projection := answerprojection.Project(result, answerprojection.Budget{})
	if projection.Cohort == nil {
		t.Fatalf("the projection carries no cohort")
	}
	return projection.Cohort
}

func TestASameTierCutListsNOfMWithoutTheTierSentence(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 20, func(int) string { return TreeLinkTierNative })
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 20}})
	if run.err != nil {
		t.Fatal(run.err)
	}
	listed := len(run.result.Cohort.Members)
	if listed >= 20 {
		t.Fatalf("the item ceiling did not cut the list (%d listed)", listed)
	}
	if limitationsContain(run.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation) {
		t.Fatalf("a cut inside one tier says lower tiers were cut first: %v", run.result.Limitations)
	}
	want, _ := contractsv1.ContextFabricWorkItemListedLimitation(listed, 20, false, contractsv1.ContextFabricWorkItemListCutServer)
	if got := listedSentence(run.result.Limitations); got != want {
		t.Fatalf("listed sentence = %q, want %q", got, want)
	}
	if len(run.walks) != 1 || run.walks[0].Members != listed || run.walks[0].Population != 20 || !run.walks[0].Truncated {
		t.Fatalf("walk decision line = %+v, want members %d, population 20, truncated true", run.walks, listed)
	}
	cohort := projectedCohortOf(t, run.result)
	if cohort.Population != 20 || cohort.PopulationLowerBound || len(cohort.Members) != listed {
		t.Fatalf("projected population = %d (lower bound %v), members = %d; want 20, false, %d", cohort.Population, cohort.PopulationLowerBound, len(cohort.Members), listed)
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
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 20}})
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
		if strings.Contains(limitation, "Not every member is listed") {
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
	run := runRepositoryTree(t, repositoryTreeCase{maxItems: serverItemCeiling, walk: TreeWorkItemWalk{Members: members, PullRequests: 1, LinkedIssues: 20, Truncated: true}})
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

func TestARestrictedCallersPopulationAndListedSentenceCountOnlyWhatTheCallerMayRead(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	members := cutTestMembers(t, 20, func(int) string { return TreeLinkTierNative })
	run := func(denied int) InvestigationResult {
		r := runRepositoryTree(t, repositoryTreeCase{
			maxItems:  serverItemCeiling,
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
