package falkorgraph

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// ownedCountFalkorAddrEnv names a FalkorDB the CHAOS-12649 live test uses instead
// of starting one, for a run beside a FalkorDB container started by name (a
// Mac-local proof). Unset, the test starts the digest-pinned image, as CI does.
const ownedCountFalkorAddrEnv = "ACR_TEST_FALKORDB_ADDR"

func ownedCountLiveAdapter(t *testing.T, ctx context.Context) *Adapter {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv(ownedCountFalkorAddrEnv))
	if addr == "" {
		adapter, _ := newLiveFalkorAdapter(t, ctx)
		return adapter
	}
	adapter, err := New(Config{
		Addr: addr, GraphPrefix: "acr-cf-12649-live", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 4, AllowInsecure: true, TLS: false,
	})
	require.NoError(t, err)
	return adapter
}

// CHAOS-12649 on a real FalkorDB through the real adapter: a repository a
// relationship only references is a stub, and the count of projected repositories
// does not include it. The first batch of a walk from zero marks the source, and a
// later batch keeps the mark; a source whose walk did not start from zero is not
// marked.
func TestLiveCountKindCountsOwnedNodesNotStubs(t *testing.T) {
	ctx := context.Background()
	adapter := ownedCountLiveAdapter(t, ctx)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	observed := time.Now().UTC()

	batch := func(orgID, cursor, next string, edges bool) contextfabric.ProjectionBatch {
		b := builderFixtureBatch(orgID, observed)
		b.BatchID = "batch_12649_" + strings.ReplaceAll(next, "-", "_")
		b.Source, b.Cursor, b.NextCursor = "owned-count-live", cursor, next
		team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:a", Label: "Auth Team"}
		if edges {
			// repository:stub is only referenced: the edge makes it a stub.
			edge := ownershipEdge(team, "acme/stub")
			edge.From = contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:stub", Label: "acme/stub"}
			edge.ObservedAt = observed
			b.Relationships = []contextfabric.RelationshipProjection{edge}
		}
		return b
	}

	fromZero := "live-12649-zero-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), fromZero) })
	_, err := adapter.ApplyProjectionBatch(ctx, batch(fromZero, "", "cursor-1", true))
	require.NoError(t, err)
	repositories, err := adapter.CountKind(ctx, fromZero, contextfabric.SubjectRepository)
	require.NoError(t, err)
	require.Equal(t, int64(1), repositories, "the referenced repository stub was counted as a projected node")
	known, err := adapter.OwnedCountsKnown(ctx, fromZero, "owned-count-live")
	require.NoError(t, err)
	require.True(t, known, "the first batch of a walk from zero did not mark the source")
	_, err = adapter.ApplyProjectionBatch(ctx, batch(fromZero, "cursor-1", "cursor-2", false))
	require.NoError(t, err)
	known, err = adapter.OwnedCountsKnown(ctx, fromZero, "owned-count-live")
	require.NoError(t, err)
	require.True(t, known, "a later batch dropped the walk's mark")

	midWalk := "live-12649-mid-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), midWalk) })
	_, err = adapter.ApplyProjectionBatch(ctx, batch(midWalk, "cursor-7", "cursor-8", false))
	require.NoError(t, err)
	known, err = adapter.OwnedCountsKnown(ctx, midWalk, "owned-count-live")
	require.NoError(t, err)
	require.False(t, known, "a source whose walk did not start from zero was read as marked")
}
