package falkorgraph

import (
	"context"
	"testing"

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
