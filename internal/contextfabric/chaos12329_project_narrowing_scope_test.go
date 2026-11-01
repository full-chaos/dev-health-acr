package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12329: the walk's scope exclusion reaches its Info line apart from the
// authorization count, and a walk whose only refusals are scope gives no
// authorization-gap disclosure.
func TestAProjectNarrowingExclusionReachesTheWalkLineApartFromDenied(t *testing.T) {
	gate, _ := NewWorkItemMembershipGate(1, 0)
	ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
	defer owner.Complete()
	graph := &treeGraphFake{walk: TreeWorkItemWalk{
		Members: []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative)}, PullRequests: 1, LinkedIssues: 3,
		ProjectExcluded: 2, Crossed: CrossedPullRequests{ExplicitText: 3, Heuristic: 1},
	}}
	telemetry := &recordingTelemetry{}
	engine := &Engine{treeWorkItemMembership: NewTreeWorkItemMembership(graph, &treeFilterFake{}, gate), telemetry: telemetry}
	request := InvestigationRequest{RequestedScope: RequestedScope{ProjectIDs: []string{"proj-1"}}}
	_, _, err := engine.discoverWorkItemTuple(ctx, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, ResolvedGraphBinding{}, repositoryAnchoredResolution(repositoryWorkItemAnchor), &AnswerPlan{}, workItemMemberFilter{})
	if err != nil {
		t.Fatalf("discoverWorkItemTuple() error = %v", err)
	}
	if len(telemetry.repositoryWorkItemWalks) != 1 {
		t.Fatalf("repository walk lines = %+v", telemetry.repositoryWorkItemWalks)
	}
	line := telemetry.repositoryWorkItemWalks[0]
	if line.ProjectExcluded != 2 || line.Denied != 0 {
		t.Fatalf("walk line project_excluded=%d denied=%d, want 2 and 0", line.ProjectExcluded, line.Denied)
	}
	// CHAOS-12308: the crossed pull requests reach the line, per tier.
	if line.Crossed != (CrossedPullRequests{ExplicitText: 3, Heuristic: 1}) {
		t.Fatalf("walk line crossed = %+v", line.Crossed)
	}
	if len(telemetry.workItemAuthorizationGaps) != 0 {
		t.Fatalf("authorization gap lines = %+v, want none for a scope exclusion", telemetry.workItemAuthorizationGaps)
	}
}
