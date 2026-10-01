package pgprojection_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	// CHAOS-4855: pinned by digest (was a bare tag) so
	// TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves this to the ghcr.io
	// mirror by digest, same as every other postgres:18-alpine pull in
	// this module.
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
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
		// CHAOS-4305: RowsApplied round-trips through the LEGACY (epoch-0)
		// CAS path too, not only ForEpoch's -- codex R1 flagged the absence
		// of a legacy-path assertion as a coverage gap.
		RowsApplied: 42,
	}
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, expected, updated))

	loaded, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.Equal(t, "cursor-1", loaded.Cursor)
	require.Equal(t, "v1", loaded.SourceVersion)
	require.Equal(t, "watermark-1", loaded.BackendWatermark)
	require.Equal(t, int64(42), loaded.RowsApplied)
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

// TestCheckpointStore_replayAfterRebuildResetAdvancesNormally is C1's probe
// promoted to a permanent regression test: a rebuild resets an EXISTING
// row's cursor back to "" (not "no row exists"), so replaying from that
// reset state must advance the checkpoint exactly like any other CAS, not
// permanently return ErrProjectionConflict via a silently-no-op INSERT.
func TestCheckpointStore_replayAfterRebuildResetAdvancesNormally(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewCheckpointStore(newCheckpointTestDatabase(t, ctx))
	require.NoError(t, err)

	zero, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, zero, contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-1", SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
	}))

	withCursor, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, withCursor, contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "", UpdatedAt: time.Now().UTC(),
	}))

	reset, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.Empty(t, reset.Cursor, "sanity: checkpoint must be reset to empty before replay")

	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, reset, contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-2", SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
	}), "post-rebuild replay must advance the checkpoint, not permanently conflict")

	final, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)
	require.Equal(t, "cursor-2", final.Cursor)
}

// TestCheckpointStore_firstInsertStillLosesToARacingConcurrentFirstInsert
// proves the fix didn't regress the original first-ever-checkpoint race:
// when NO row exists yet, two concurrent CAS calls from the true zero value
// must still have exactly one winner.
//
// CONTRIVED-proof gap flagged in the codex round-2 review: the previous
// version called the two CompareAndSwapProjectionCheckpoint calls one
// after the other and awaited each in turn -- by the time the second call
// even started, the first had already committed, so this was really
// testing "insert, then insert-again-and-conflict", a strictly weaker,
// entirely deterministic property that says nothing about real
// concurrent contention on the same INSERT ... ON CONFLICT DO NOTHING.
// Now launches both calls as goroutines released simultaneously from a
// shared channel (each uses a separate pooled connection, so this is
// genuine concurrent contention at the database level, not simulated).
func TestCheckpointStore_firstInsertStillLosesToARacingConcurrentFirstInsert(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewCheckpointStore(newCheckpointTestDatabase(t, ctx))
	require.NoError(t, err)
	zero, err := store.LoadProjectionCheckpoint(ctx, "org-1", "dev_health_clickhouse")
	require.NoError(t, err)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var firstErr, secondErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		firstErr = store.CompareAndSwapProjectionCheckpoint(ctx, zero, contextfabric.ProjectionCheckpoint{
			OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-a", SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		secondErr = store.CompareAndSwapProjectionCheckpoint(ctx, zero, contextfabric.ProjectionCheckpoint{
			OrgID: "org-1", Source: "dev_health_clickhouse", Cursor: "cursor-b", SourceVersion: "v1", UpdatedAt: time.Now().UTC(),
		})
	}()
	close(start) // release both goroutines at once
	wg.Wait()

	require.True(t, (firstErr == nil) != (secondErr == nil), "exactly one of two racing first-ever inserts must win: first=%v second=%v", firstErr, secondErr)
	if secondErr != nil {
		require.True(t, errors.Is(secondErr, contextfabric.ErrProjectionConflict))
	} else {
		require.True(t, errors.Is(firstErr, contextfabric.ErrProjectionConflict))
	}
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

// TestCheckpointStore_listsTheRowsOfOneEpochOnlyExactlyAsStored pins the
// capability the epoch activation guard reads: every checkpoint row of one
// (organization, epoch), sorted by source, nothing from another epoch or
// org, and each source name and version exactly as stored -- a padded name
// or version must reach the guard untouched.
func TestCheckpointStore_listsTheRowsOfOneEpochOnlyExactlyAsStored(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewCheckpointStore(newCheckpointTestDatabase(t, ctx))
	if err != nil {
		t.Fatal(err)
	}
	write := func(view contextfabric.ProjectionCheckpointStore, org, source, version string) {
		t.Helper()
		if err := view.CompareAndSwapProjectionCheckpoint(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: source},
			contextfabric.ProjectionCheckpoint{OrgID: org, Source: source, Cursor: "c1", SourceVersion: version}); err != nil {
			t.Fatalf("write %s/%q: %v", org, source, err)
		}
	}
	write(store, "org-1", "zeta", "v1")
	write(store, "org-1", " alpha ", "v2 ")
	write(store.ForEpoch(2), "org-1", "beta", "v3")
	write(store, "org-2", "gamma", "v4")

	describe := func(rows []contextfabric.ProjectionCheckpoint) string {
		parts := make([]string, 0, len(rows))
		for _, row := range rows {
			parts = append(parts, fmt.Sprintf("%q=%q", row.Source, row.SourceVersion))
		}
		return strings.Join(parts, ",")
	}
	epoch0, err := store.ListProjectionCheckpoints(ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := describe(epoch0), `" alpha "="v2 ","zeta"="v1"`; got != want {
		t.Fatalf("epoch 0 rows = %s, want %s", got, want)
	}
	epoch2, err := store.ForEpoch(2).(contextfabric.ProjectionCheckpointLister).ListProjectionCheckpoints(ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := describe(epoch2), `"beta"="v3"`; got != want {
		t.Fatalf("epoch 2 rows = %s, want %s", got, want)
	}
	none, err := store.ForEpoch(3).(contextfabric.ProjectionCheckpointLister).ListProjectionCheckpoints(ctx, "org-1")
	if err != nil || len(none) != 0 {
		t.Fatalf("an epoch with no rows lists %v, %v; want nothing", none, err)
	}
}

func TestCheckpointStore_rebuildOwedIsEpochScopedNeverCreatesARowAndIsClearedByTheCursorCAS(t *testing.T) {
	ctx := context.Background()
	store, err := pgprojection.NewCheckpointStore(newCheckpointTestDatabase(t, ctx))
	require.NoError(t, err)
	const org, source = "org-1", "dev_health_clickhouse"

	require.NoError(t, store.MarkProjectionRebuildOwed(ctx, org, source))
	absent, err := store.LoadProjectionCheckpoint(ctx, org, source)
	require.NoError(t, err)
	require.False(t, absent.RebuildOwed, "marking a source with no checkpoint row must not invent one")
	require.Empty(t, absent.Cursor)

	first := contextfabric.ProjectionCheckpoint{OrgID: org, Source: source, Cursor: "c1", SourceVersion: "v1", UpdatedAt: time.Now().UTC()}
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, absent, first))
	epochOne := store.ForEpoch(1)
	require.NoError(t, epochOne.CompareAndSwapProjectionCheckpoint(ctx,
		contextfabric.ProjectionCheckpoint{OrgID: org, Source: source},
		contextfabric.ProjectionCheckpoint{OrgID: org, Source: source, Cursor: "e1", SourceVersion: "v1", UpdatedAt: time.Now().UTC()}))

	require.NoError(t, epochOne.(contextfabric.ProjectionRebuildOwedMarker).MarkProjectionRebuildOwed(ctx, org, source))
	legacy, err := store.LoadProjectionCheckpoint(ctx, org, source)
	require.NoError(t, err)
	require.False(t, legacy.RebuildOwed, "epoch 1's flag must not leak into epoch 0")
	built, err := epochOne.LoadProjectionCheckpoint(ctx, org, source)
	require.NoError(t, err)
	require.True(t, built.RebuildOwed)
	listed, err := epochOne.(contextfabric.ProjectionCheckpointLister).ListProjectionCheckpoints(ctx, org)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.True(t, listed[0].RebuildOwed)

	require.NoError(t, store.MarkProjectionRebuildOwed(ctx, org, source))
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(ctx, first, contextfabric.ProjectionCheckpoint{OrgID: org, Source: source, Cursor: "c2", SourceVersion: "v1", UpdatedAt: time.Now().UTC()}))
	cleared, err := store.LoadProjectionCheckpoint(ctx, org, source)
	require.NoError(t, err)
	require.False(t, cleared.RebuildOwed, "the cursor CAS clears the flag")

	require.NoError(t, epochOne.CompareAndSwapProjectionCheckpoint(ctx, built, contextfabric.ProjectionCheckpoint{OrgID: org, Source: source, Cursor: "e2", SourceVersion: "v1", UpdatedAt: time.Now().UTC()}))
	clearedEpoch, err := epochOne.LoadProjectionCheckpoint(ctx, org, source)
	require.NoError(t, err)
	require.False(t, clearedEpoch.RebuildOwed)
}
