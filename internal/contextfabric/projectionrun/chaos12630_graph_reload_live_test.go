package projectionrun_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/falkorfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// reloadFalkorAddrEnv names a FalkorDB the test uses instead of starting one, for
// a run beside a FalkorDB container started by name (a Mac-local proof). Unset, the
// test starts the digest-pinned image through Testcontainers, as CI does.
const reloadFalkorAddrEnv = "ACR_TEST_FALKORDB_ADDR"

func reloadFalkorAddr(t *testing.T, ctx context.Context) string {
	t.Helper()
	if addr := strings.TrimSpace(os.Getenv(reloadFalkorAddrEnv)); addr != "" {
		return addr
	}
	chfixture.RequireContainers(t)
	container := falkorfixture.Start(ctx, t, epochGuardFalkorImage)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	return host + ":" + port.Port()
}

// CHAOS-12630, on a real FalkorDB with the real falkorgraph adapter. The graph is
// copied after the first batch (GRAPH.COPY), two more batches are projected, and
// the copy is put back in the graph's place: the state a FalkorDB restart from an
// older snapshot leaves, with the watermark node of the first batch and the
// checkpoint of the third. The next ticks must walk the lost batches again. The
// checkpoint store is the in-memory one: what is under test is the graph.
func TestAGraphReloadedFromAnOlderCopyIsWalkedAgainOnRealStores(t *testing.T) {
	ctx := context.Background()
	addr := reloadFalkorAddr(t, ctx)
	prefix := fmt.Sprintf("acr-cf-12630-%d", time.Now().UnixNano())
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: addr, GraphPrefix: prefix, RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 4, AllowInsecure: true, TLS: false,
	})
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	source := &steppedSource{name: "source-a", last: 3}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-12630"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: adapter, Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(), DrainBatchBudget: -1,
	})
	require.NoError(t, err)

	nodes := func(key, id string) int64 {
		t.Helper()
		reply, err := client.Do(ctx, "GRAPH.RO_QUERY", key, "MATCH (n {canonical_id:'"+id+"'}) RETURN count(n)").Slice()
		require.NoError(t, err)
		rows, ok := reply[1].([]interface{})
		require.True(t, ok && len(rows) == 1, "one result row, got %v", reply)
		cells, ok := rows[0].([]interface{})
		require.True(t, ok && len(cells) == 1, "one cell, got %v", rows[0])
		count, ok := cells[0].(int64)
		require.True(t, ok, "an integer count, got %T", cells[0])
		return count
	}

	coordinator.Tick(ctx)
	graphs, err := client.Do(ctx, "GRAPH.LIST").StringSlice()
	require.NoError(t, err)
	key := ""
	for _, name := range graphs {
		if strings.HasPrefix(name, prefix) {
			key = name
		}
	}
	require.NotEmpty(t, key, "the organization's graph key")
	t.Cleanup(func() {
		_ = client.Do(context.Background(), "GRAPH.DELETE", key).Err()
		_ = client.Do(context.Background(), "GRAPH.DELETE", key+"-snapshot").Err()
	})
	require.Equal(t, int64(1), nodes(key, "repository:test-c1"), "precondition: the first batch is in the graph")
	require.NoError(t, client.Do(ctx, "GRAPH.COPY", key, key+"-snapshot").Err())

	coordinator.Tick(ctx)
	coordinator.Tick(ctx)
	require.Equal(t, int64(1), nodes(key, "repository:test-c3"), "precondition: the third batch is in the graph")

	require.NoError(t, client.Do(ctx, "GRAPH.DELETE", key).Err())
	require.NoError(t, client.Do(ctx, "GRAPH.COPY", key+"-snapshot", key).Err())
	require.Equal(t, int64(0), nodes(key, "repository:test-c3"), "precondition: the reloaded graph lost the later batches")

	for i := 0; i < 3; i++ {
		coordinator.Tick(ctx)
	}
	require.Equal(t, int64(1), nodes(key, "repository:test-c2"), "the second batch was not walked again after the reload")
	require.Equal(t, int64(1), nodes(key, "repository:test-c3"), "the third batch was not walked again after the reload")
}
