package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12319 (lead ruling, anchor B): when no requested team owns the anchor,
// the anchor stays the subject and its own totals are not the team's. A walk
// with no member is then never explained by the anchor's totals (no pull
// request, unlinked): it is no_match.
func TestAnAnchorOutsideTheTeamNarrowingIsNotExplainedByItsTotals(t *testing.T) {
	for name, tc := range map[string]struct {
		outside                   bool
		pullRequests, linkedIssue int
		want                      RepositoryWorkItemWalkOutcome
	}{
		"outside, no pull request":       {true, 0, 0, RepositoryWorkItemWalkNoMatch},
		"outside, unlinked":              {true, 3, 0, RepositoryWorkItemWalkNoMatch},
		"owned, no pull request":         {false, 0, 0, RepositoryWorkItemWalkNoPullRequests},
		"owned, unlinked":                {false, 3, 0, RepositoryWorkItemWalkUnlinked},
		"no narrowing, linked, no match": {false, 3, 2, RepositoryWorkItemWalkNoMatch},
	} {
		t.Run(name, func(t *testing.T) {
			gate, _ := NewWorkItemMembershipGate(1, 0)
			ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
			defer owner.Complete()
			graph := &treeGraphFake{walk: TreeWorkItemWalk{
				Members: []TreeWorkItemMember{}, PullRequests: tc.pullRequests, LinkedIssues: tc.linkedIssue,
				AnchorOutsideTeamNarrowing: tc.outside,
			}}
			telemetry := &recordingTelemetry{}
			engine := &Engine{treeWorkItemMembership: NewTreeWorkItemMembership(graph, &treeFilterFake{}, gate), telemetry: telemetry}
			request := InvestigationRequest{RequestedScope: RequestedScope{TeamIDs: []string{"team-C"}}}
			_, _, err := engine.discoverWorkItemTuple(ctx, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, ResolvedGraphBinding{}, repositoryAnchoredResolution(repositoryWorkItemAnchor), &AnswerPlan{}, workItemMemberFilter{})
			if err != nil {
				t.Fatalf("discoverWorkItemTuple() error = %v", err)
			}
			if graph.calls != 1 {
				t.Fatalf("premise: walk calls %d", graph.calls)
			}
			if len(telemetry.repositoryWorkItemWalks) != 1 || telemetry.repositoryWorkItemWalks[0].Outcome != tc.want {
				t.Fatalf("repository walk lines = %+v, want one with outcome %s", telemetry.repositoryWorkItemWalks, tc.want)
			}
		})
	}
}
