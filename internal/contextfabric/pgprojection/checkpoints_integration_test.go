package pgprojection_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func newCheckpointTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)
	return db
}

func TestCheckpointStore_initialLoadIsZeroValue_whenNeverProjected(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewCheckpointStore(newCheckpointTestDatabase(t, ctx))
	require.NoError(t, err)

	checkpoint, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.Equal(t, "org-1", checkpoint.OrgID)
	require.Equal(t, "dev_health_clickhouse", checkpoint.Source)
	require.Empty(t, checkpoint.Cursor)
}

func TestCheckpointStore_firstCompareAndSwapInsertsThenLoadRoundTrips(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewCheckpointStore(newCheckpointTestDatabase(t, ctx))
	require.NoError(t, err)
	expected, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)

	updated := contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-1",
		SourceVersion: "v1", BackendWatermark: "watermark-1", UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, expected, updated))

	loaded, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.Equal(t, "cursor-1", loaded.Cursor)
	require.Equal(t, "v1", loaded.SourceVersion)
	require.Equal(t, "watermark-1", loaded.BackendWatermark)
}

// Restart-from-checkpoint: a second store instance (simulating a worker
// restart) reads exactly the durable cursor a prior instance advanced to.
func TestCheckpointStore_restartResumesFromDurableCheckpoint(t *testing.T) {
	ctx := context.Background()
	db := newCheckpointTestDatabase(t, ctx)
	first, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	zero, err := first.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.NoError(t, first.CompareAndSwapProjectionCheckpoint(ctx, zero, contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-1", SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
	}))

	restarted, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	resumed, err := restarted.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.Equal(t, "cursor-1", resumed.Cursor)
}

// Concurrent checkpoint conflict: two workers both read the same checkpoint
// (simulating a race), then both try to advance it. Exactly one succeeds;
// the loser gets ErrProjectionConflict and must not have moved the cursor.
func TestCheckpointStore_concurrentCompareAndSwapConflict(t *testing.T) {
	ctx := context.Background()
	db := newCheckpointTestDatabase(t, ctx)
	store, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	zero, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, zero, contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-1", SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
	}))
	// Both "workers" read the checkpoint at cursor-1...
	read, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)

	// ...worker A advances first.
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, read, contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-2", SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
	}))

	// ...worker B, still holding the stale cursor-1 read, must lose.
	err = store.CompareAndSwapProjectionCheckpoint(ctx, read, contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-2-conflict", SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
	})
	require.True(t, errors.Is(err, contextfabric.ErrProjectionConflict))

	final, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.Equal(t, "cursor-2", final.Cursor, "the losing compare-and-swap must not have moved the cursor")
}

func TestCheckpointStore_isolatesCheckpointsPerOrganizationAndSource(t *testing.T) {
	ctx := context.Background()
	db := newCheckpointTestDatabase(t, ctx)
	store, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	for _, pair := range [][2]string{{"org-1", "dev_health_clickhouse"}, {"org-1", "dev_health_episodes"}, {"org-2", "dev_health_clickhouse"}} {
		zero, err := store.LoadProjectionCheckpoint(ctx, pair[0], pair[1])
		require.NoError(t, err)
		require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, zero, contextfabric.ProjectionCheckpoint{
			OrgID: pair[0], Source: pair[1], Cursor: "cursor-" + pair[0] + "-" + pair[1], SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
		}))
	}
	orgOneClickHouse, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	orgOneEpisodes, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_episodes")
	require.NoError(t, err)
	orgTwoClickHouse, err := store.LoadProjectionCheckpoint(ctx, "org-2", "dev_health_clickhouse")
	require.NoError(t, err)
	require.Equal(t, "cursor-org-1-dev_health_clickhouse", orgOneClickHouse.Cursor)
	require.Equal(t, "cursor-org-1-dev_health_episodes", orgOneEpisodes.Cursor)
	require.Equal(t, "cursor-org-2-dev_health_clickhouse", orgTwoClickHouse.Cursor)
}
