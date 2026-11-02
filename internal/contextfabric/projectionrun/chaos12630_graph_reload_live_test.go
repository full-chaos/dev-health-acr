package projectionrun_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tcexec "github.com/testcontainers/testcontainers-go/exec"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/falkorfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// CHAOS-12630, on real stores: FalkorDB holds the graph and Postgres holds the
// checkpoint, as in production. The graph is copied after the first batch
// (GRAPH.COPY), two more batches are projected, and the copy is put back in the
// graph's place: the state a FalkorDB restart from an older snapshot leaves, with
// the watermark node of the first batch and the checkpoint of the third. The next
// ticks must walk the lost batches again.
func TestAGraphReloadedFromAnOlderCopyIsWalkedAgainOnRealStores(t *testing.T) {
	chfixture.RequireContainers(t)
	ctx := context.Background()
	container := falkorfixture.Start(ctx, t, epochGuardFalkorImage)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-12630", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 4, AllowInsecure: true, TLS: false,
	})
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(newProjectionRunTestDatabase(t, ctx))
	require.NoError(t, err)

	source := &steppedSource{name: "source-a", last: 3}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-12630"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: adapter, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), DrainBatchBudget: -1,
	})
	require.NoError(t, err)

	redis := func(args ...string) string {
		t.Helper()
		code, reader, err := container.Exec(ctx, append([]string{"redis-cli", "--no-auth-warning"}, args...), tcexec.Multiplexed())
		require.NoError(t, err)
		out, _ := io.ReadAll(reader)
		require.Equal(t, 0, code, "redis-cli %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	nodes := func(key, id string) string {
		return redis("GRAPH.RO_QUERY", key, "MATCH (n {canonical_id:'"+id+"'}) RETURN count(n)")
	}

	coordinator.Tick(ctx)
	key := ""
	for _, name := range strings.Fields(redis("GRAPH.LIST")) {
		if strings.HasPrefix(name, "acr-cf-12630") {
			key = name
		}
	}
	require.NotEmpty(t, key, "the organization's graph key")
	require.Contains(t, nodes(key, "repository:test-c1"), "\n1\n", "precondition: the first batch is in the graph")
	redis("GRAPH.COPY", key, key+"-snapshot")

	coordinator.Tick(ctx)
	coordinator.Tick(ctx)
	require.Contains(t, nodes(key, "repository:test-c3"), "\n1\n", "precondition: the third batch is in the graph")

	redis("GRAPH.DELETE", key)
	redis("GRAPH.COPY", key+"-snapshot", key)
	require.Contains(t, nodes(key, "repository:test-c3"), "\n0\n", "precondition: the reloaded graph lost the later batches")

	for i := 0; i < 3; i++ {
		coordinator.Tick(ctx)
	}
	require.Contains(t, nodes(key, "repository:test-c2"), "\n1\n", "the second batch was not walked again after the reload")
	require.Contains(t, nodes(key, "repository:test-c3"), "\n1\n", "the third batch was not walked again after the reload")
}
