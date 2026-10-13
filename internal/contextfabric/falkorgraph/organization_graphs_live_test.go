package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestLiveDeleteOrganizationGraphsDropsEveryEpochAndNothingElse plants graphs
// for epochs 0, 1, 2 and an orphan epoch 7 for one organization, a graph for
// another organization, and two look-alike keys, then deletes the first
// organization's graphs through the adapter.
func TestLiveDeleteOrganizationGraphsDropsEveryEpochAndNothingElse(t *testing.T) {
	ctx := context.Background()
	adapter, addr := newLiveFalkorAdapter(t, ctx)
	raw := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = raw.Close() })

	const orgA, orgB = "purge-org-a", "purge-org-b"
	prefix := adapter.config.GraphPrefix
	plant := func(key string) {
		require.NoError(t, raw.Do(ctx, "GRAPH.QUERY", key, "CREATE (:Probe {k:1})").Err())
	}
	for _, epoch := range []int64{0, 1, 2, 7} {
		plant(graphKeyForEpoch(prefix, orgA, epoch))
	}
	plant(graphKeyForEpoch(prefix, orgB, 0))
	plant(graphKeyForEpoch(prefix, orgB, 3))
	lookalikes := []string{graphKey(prefix, orgA) + "-ex", graphKey(prefix, orgA) + "-e", graphKey(prefix, orgA) + "-e1x"}
	for _, key := range lookalikes {
		plant(key)
	}

	count, err := adapter.CountOrganizationGraphs(ctx, orgA)
	require.NoError(t, err)
	require.Equal(t, 4, count)

	deleted, err := adapter.DeleteOrganizationGraphs(ctx, orgA)
	require.NoError(t, err)
	require.Equal(t, 4, deleted)

	count, err = adapter.CountOrganizationGraphs(ctx, orgA)
	require.NoError(t, err)
	require.Zero(t, count, "no graph of the organization may survive at any epoch")
	names, err := adapter.api.listGraphs(ctx)
	require.NoError(t, err)
	for _, epoch := range []int64{0, 1, 2, 7} {
		require.NotContains(t, names, graphKeyForEpoch(prefix, orgA, epoch))
	}
	require.Contains(t, names, graphKeyForEpoch(prefix, orgB, 0))
	require.Contains(t, names, graphKeyForEpoch(prefix, orgB, 3))
	for _, key := range lookalikes {
		require.Contains(t, names, key, "a key that is not <org key>-e<digits> must be left alone")
	}

	again, err := adapter.DeleteOrganizationGraphs(ctx, orgA)
	require.NoError(t, err)
	require.Zero(t, again, "second delete is a no-op")
}

// TestLiveDeleteOrganizationGraphsFindsGraphsCreatedUnderAnotherUUIDSpelling
// writes graphs through the adapter for the same organization spelled in
// upper case and braced, then deletes by the canonical lower-case spelling.
func TestLiveDeleteOrganizationGraphsFindsGraphsCreatedUnderAnotherUUIDSpelling(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	const canonical = "abcdef01-2345-4678-89ab-cdef01234567"
	const other = "abcdef01-2345-4678-89ab-cdef01234568"
	apply := func(org string) {
		observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work-item-1", Label: "WIDGET-1"}
		_, err := adapter.ApplyProjectionBatch(ctx, contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_spelling_00000001", OrgID: org, Source: "live-test",
			SourceVersion: "v1", NextCursor: "cursor-1", GeneratedAt: observed,
			Entities: []contextfabric.EntityProjection{{Subject: subject, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"example-org/widget-service"}},
				EvidenceRefIDs: []string{"evidence_1"}, ObservedAt: observed, SourceVersion: "v1"}},
			Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
			Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		})
		require.NoError(t, err)
	}
	apply(canonical)
	apply(strings.ToUpper(canonical))
	apply("{" + strings.ToUpper(canonical) + "}")
	apply(other)

	count, err := adapter.CountOrganizationGraphs(ctx, canonical)
	require.NoError(t, err)
	require.Equal(t, 3, count, "graphs created under every spelling of the organization")

	deleted, err := adapter.DeleteOrganizationGraphs(ctx, canonical)
	require.NoError(t, err)
	require.Equal(t, 3, deleted)
	left, err := adapter.CountOrganizationGraphs(ctx, canonical)
	require.NoError(t, err)
	require.Zero(t, left)
	otherCount, err := adapter.CountOrganizationGraphs(ctx, other)
	require.NoError(t, err)
	require.Equal(t, 1, otherCount, "a different organization keeps its graph")
}
