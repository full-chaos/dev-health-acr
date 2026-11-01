package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12329 on a real FalkorDB: under a complete project narrowing, a
// linked issue whose stated membership is another project is out of scope.
// It is neither a member nor denied, for an unrestricted caller.
func TestLiveAProjectNarrowingExclusionIsNotADenial(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	assertProjectNarrowingExclusionIsScope(t, ctx, adapter)
}

func assertProjectNarrowingExclusionIsScope(t *testing.T, ctx context.Context, adapter *Adapter) {
	t.Helper()
	orgID := "live-12329-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	entities, relationships := newLiveMemberFixture(orgID, time.Now().UTC(), nil, liveMemberLinks())
	stated := map[string][]string{memberLinearIssue: {"project.v2:linear:proj-other"}, memberJiraIssue: {"project.v2:linear:proj-1"}}
	for i := range entities.Entities {
		if ids, ok := stated[entities.Entities[i].Subject.CanonicalID]; ok {
			ids := ids
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
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	for name, tc := range map[string]struct {
		scope                   contextfabric.RequestedScope
		members, denied, outOfS int
	}{
		"no narrowing": {contextfabric.RequestedScope{}, 8, 0, 0},
		"proj-1, complete": {contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"},
			ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true}}, 7, 0, 1},
	} {
		walk, err := adapter.TreeWorkItemMembers(ctx, principal, binding, tc.scope, anchor, 25)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(walk.Members) != tc.members || walk.Denied != tc.denied || walk.ProjectExcluded != tc.outOfS {
			t.Fatalf("%s: members=%d denied=%d project_excluded=%d, want %d %d %d", name, len(walk.Members), walk.Denied, walk.ProjectExcluded, tc.members, tc.denied, tc.outOfS)
		}
	}
}
