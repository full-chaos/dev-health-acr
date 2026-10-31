package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-12229, on a real FalkorDB: the resolver's lookup finds the project
// nodes that carry each requested raw id (their authorization_projects), in
// the caller's organization only, and an id two providers share stays
// unresolved.
func TestLiveTheProjectNarrowingLookupReadsTheProjectNodes(t *testing.T) {
	ctx := context.Background()
	assertProjectNarrowingLookup(t, ctx, newLiveAdapter(t, ctx))
}

func assertProjectNarrowingLookup(t *testing.T, ctx context.Context, adapter *falkorgraph.Adapter) {
	t.Helper()
	observed := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	project := func(canonical, rawID string) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject:        contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonical, Label: rawID},
			Properties:     map[string]contextfabric.ScalarValue{},
			Authorization:  contextfabric.AuthorizationScope{ProjectIDs: []string{rawID}},
			EvidenceRefIDs: []string{"evidence_" + rawID + "_00000001"}, ObservedAt: observed, SourceVersion: "v1",
		}
	}
	batch := func(orgID, batchID string, entities ...contextfabric.EntityProjection) contextfabric.ProjectionBatch {
		return contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: batchID, OrgID: orgID, Source: "live-test",
			SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
			Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
			Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}
	}
	_, err := adapter.ApplyProjectionBatch(ctx, batch("org_narrow_live", "batch_narrow_00000001",
		project("project.v2:linear:proj-1", "proj-1"),
		project("project.v2:github:P-SHARED", "P-SHARED"),
		project("project.v2:jira:P-SHARED", "P-SHARED"),
	))
	require.NoError(t, err)
	_, err = adapter.ApplyProjectionBatch(ctx, batch("org_narrow_other", "batch_narrow_00000002",
		project("project.v2:linear:proj-7", "proj-7"),
	))
	require.NoError(t, err)

	principal := storage.Principal{OrgID: "org_narrow_live"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		ids      []string
		want     []string
		complete bool
	}{
		"one id, one project":                 {[]string{"proj-1"}, []string{"project.v2:linear:proj-1"}, true},
		"an id under two providers":           {[]string{"P-SHARED"}, []string{}, false},
		"another organization's project":      {[]string{"proj-7"}, []string{}, false},
		"a resolved id beside an unknown one": {[]string{"proj-1", "proj-9"}, []string{"project.v2:linear:proj-1"}, false},
	} {
		got, err := adapter.ResolveProjectNarrowing(ctx, principal, binding, tc.ids)
		require.NoError(t, err, name)
		require.NotNil(t, got, name)
		require.Equal(t, tc.complete, got.Complete, name)
		require.Equal(t, tc.want, got.Identities, name)
	}
}
