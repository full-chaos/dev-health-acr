package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12361 on a real FalkorDB: a project-anchored cohort member walk
// narrowed to another project serves no member and denies none; its members
// are out of scope.
func TestLiveACohortWalkCountsAProjectNarrowingExclusionAsScope(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	assertCohortWalkNarrowingIsScope(t, ctx, adapter)
}

func assertCohortWalkNarrowingIsScope(t *testing.T, ctx context.Context, adapter *Adapter) {
	t.Helper()
	orgID := "live-12361-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:proj-p", Label: "proj-p"}
	entities, relationships := newLiveMemberFixture(orgID, time.Now().UTC(), func(add func(contextfabric.SubjectRef, contextfabric.AuthorizationScope, string), relate func(string, contractsv1.ContextFabricRelationshipType, contextfabric.SubjectRef, contextfabric.SubjectRef, contextfabric.AuthorizationScope, string)) {
		add(project, contextfabric.AuthorizationScope{ProjectIDs: []string{"proj-p"}}, "")
		for _, id := range []string{memberLinearIssue, memberJiraIssue} {
			// The producer's shape: the edge carries its subject's own
			// authorization (a repository-less work item).
			relate(id+"_p", contractsv1.ContextFabricRelationshipBelongsToProject, contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id, Label: "issue"}, project, contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}}, "")
		}
	}, liveMemberLinks())
	for i := range entities.Entities {
		switch entities.Entities[i].Subject.CanonicalID {
		case memberLinearIssue, memberJiraIssue:
			ids := []string{project.CanonicalID}
			entities.Entities[i].ProjectIdentities = &ids
		}
	}
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	principal := storage.Principal{OrgID: orgID}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	key, err := adapter.effectiveKey(ctx, orgID, binding)
	if err != nil {
		t.Fatal(err)
	}
	narrowed := func(id string) contextfabric.RequestedScope {
		return contextfabric.RequestedScope{ProjectIDs: []string{id}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:" + id}, Complete: true}}
	}
	for name, tc := range map[string]struct {
		scope                    contextfabric.RequestedScope
		members, denied, outside int
	}{
		"no narrowing":             {contextfabric.RequestedScope{}, 2, 0, 0},
		"the anchor's own project": {narrowed("proj-p"), 2, 0, 0},
		"another project":          {narrowed("proj-1"), 0, 0, 2},
	} {
		walk, err := adapter.teamAnchorMembers(ctx, key, orgID, principal, tc.scope, project, treeIssue, 25, temporalFilter{}, memberStatusPredicate{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(walk.nodes) != tc.members || walk.denied != tc.denied || walk.filters.Authz != tc.denied || len(walk.hopProjectExcluded) != tc.outside || walk.anchorDenied {
			t.Fatalf("%s: members=%d denied=%d authz=%d excluded=%d anchorDenied=%v, want %d %d %d false", name, len(walk.nodes), walk.denied, walk.filters.Authz, len(walk.hopProjectExcluded), walk.anchorDenied, tc.members, tc.denied, tc.outside)
		}
	}
}
