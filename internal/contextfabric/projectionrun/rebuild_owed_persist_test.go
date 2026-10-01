package projectionrun_test

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

const rebuildOwedOrg = "org-a"

// restartedCoordinator builds a NEW coordinator over the same Postgres, the
// shape of a projector restart: no in-process backoff state survives.
func restartedCoordinator(t *testing.T, db *sql.DB, clock *fakeClock, sources ...*fakeSource) (*projectionrun.Coordinator, *bytes.Buffer) {
	t.Helper()
	return restartedCoordinatorOn(t, db, clock, newFakeBackend(), sources...)
}

func restartedCoordinatorOn(t *testing.T, db *sql.DB, clock *fakeClock, backend *fakeBackend, sources ...*fakeSource) (*projectionrun.Coordinator, *bytes.Buffer) {
	t.Helper()
	store, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	var buffer bytes.Buffer
	pairs := make([]projectionrun.SourcePair, 0, len(sources))
	for _, source := range sources {
		pairs = append(pairs, projectionrun.SourcePair{Name: source.name, Source: source})
	}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{rebuildOwedOrg},
		Sources:        pairs,
		Backend:        backend,
		Checkpoints:    store,
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo})),
		Now:            clock.Now,
	})
	require.NoError(t, err)
	return coordinator, &buffer
}

func tickSummary(t *testing.T, coordinator *projectionrun.Coordinator, buffer *bytes.Buffer) map[string]any {
	t.Helper()
	buffer.Reset()
	coordinator.Tick(context.Background())
	summary := freshnessSummary(t, buffer)
	requireBucketIdentity(t, summary)
	return summary
}

func seedMatchingCheckpoint(t *testing.T, db *sql.DB, source string) {
	t.Helper()
	store, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	// SourceVersion equals what the fake sources report, so the freshness
	// check alone cannot flag this pair: only the durable flag can.
	require.NoError(t, store.CompareAndSwapProjectionCheckpoint(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: rebuildOwedOrg, Source: source},
		contextfabric.ProjectionCheckpoint{OrgID: rebuildOwedOrg, Source: source, Cursor: "c", SourceVersion: "test.v1", UpdatedAt: time.Now().UTC()}))
}

func TestRebuildOwedSurvivesProjectorRestartUntilABatchApplies(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	clock := newFakeClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	seedMatchingCheckpoint(t, db, "src")

	refused, buffer := restartedCoordinator(t, db, clock, versionRefusedSource("src"))
	requireBuckets(t, tickSummary(t, refused, buffer), 1, 0, 0, 1)

	// Restart onto a dormant source: nothing refuses, nothing is stale by
	// version, so only the persisted flag can report the owed rebuild.
	dormant := &fakeSource{name: "src", dormant: true}
	restarted, buffer := restartedCoordinator(t, db, clock, dormant)
	requireBuckets(t, tickSummary(t, restarted, buffer), 1, 0, 0, 1)

	// A restart that stays dormant keeps reporting it: a no-op attempt clears nothing.
	again, buffer := restartedCoordinator(t, db, clock, &fakeSource{name: "src", dormant: true})
	requireBuckets(t, tickSummary(t, again, buffer), 1, 0, 0, 1)

	// An applied batch clears it, durably.
	applying, buffer := restartedCoordinator(t, db, clock, &fakeSource{name: "src", pages: 1})
	requireBuckets(t, tickSummary(t, applying, buffer), 0, 0, 0, 0)
	after, buffer := restartedCoordinator(t, db, clock, &fakeSource{name: "src", dormant: true})
	requireBuckets(t, tickSummary(t, after, buffer), 0, 0, 0, 0)

	store, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	checkpoint, err := store.LoadProjectionCheckpoint(ctx, rebuildOwedOrg, "src")
	require.NoError(t, err)
	require.False(t, checkpoint.RebuildOwed)
}

func TestRebuildOwedOfARemovedSourceDoesNotKeepTheOrgStale(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	clock := newFakeClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	seedMatchingCheckpoint(t, db, "gone")
	seedMatchingCheckpoint(t, db, "kept")

	refused, buffer := restartedCoordinator(t, db, clock, versionRefusedSource("gone"), &fakeSource{name: "kept", dormant: true})
	requireBuckets(t, tickSummary(t, refused, buffer), 1, 0, 0, 1)

	restarted, buffer := restartedCoordinator(t, db, clock, &fakeSource{name: "kept", dormant: true})
	requireBuckets(t, tickSummary(t, restarted, buffer), 0, 0, 0, 0)
}

func TestRebuildOwedSurvivesAResetUntilABatchApplies(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	clock := newFakeClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	seedMatchingCheckpoint(t, db, "src")

	refused, buffer := restartedCoordinator(t, db, clock, versionRefusedSource("src"))
	requireBuckets(t, tickSummary(t, refused, buffer), 1, 0, 0, 1)
	require.NoError(t, refused.Rebuild(ctx, rebuildOwedOrg))

	store, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	reset, err := store.LoadProjectionCheckpoint(ctx, rebuildOwedOrg, "src")
	require.NoError(t, err)
	require.Empty(t, reset.Cursor, "the rebuild reset the checkpoint")
	require.True(t, reset.RebuildOwed, "a reset applies nothing and must not resolve the owed rebuild")

	restarted, buffer := restartedCoordinator(t, db, clock, &fakeSource{name: "src", dormant: true})
	requireBuckets(t, tickSummary(t, restarted, buffer), 1, 0, 0, 1)

	applying, buffer := restartedCoordinator(t, db, clock, &fakeSource{name: "src", pages: 1})
	requireBuckets(t, tickSummary(t, applying, buffer), 0, 0, 0, 0)
}

func TestRebuildOwedClearedByAnotherReplicaIsNotReportedForever(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	clock := newFakeClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	seedMatchingCheckpoint(t, db, "src")

	// One graph backend for both replicas, as in production: separate fakes
	// would read as the divergence the coordinator recovers from.
	backend := newFakeBackend()
	sourceA := versionRefusedSource("src")
	replicaA, bufferA := restartedCoordinatorOn(t, db, clock, backend, sourceA)
	requireBuckets(t, tickSummary(t, replicaA, bufferA), 1, 0, 0, 1)

	replicaB, bufferB := restartedCoordinatorOn(t, db, clock, backend, &fakeSource{name: "src", pages: 1})
	requireBuckets(t, tickSummary(t, replicaB, bufferB), 0, 0, 0, 0)

	// A's source recovers but has nothing new: a no-op attempt clears nothing
	// in A's own memory, so only the durable row can tell A it is settled.
	sourceA.err = nil
	sourceA.dormant = true
	clock.Advance(time.Hour)
	requireBuckets(t, tickSummary(t, replicaA, bufferA), 0, 0, 0, 0)
}

func TestRebuildOwedSurvivesAClaimWhoseApplyFails(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	clock := newFakeClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	seedMatchingCheckpoint(t, db, "src")

	refused, buffer := restartedCoordinator(t, db, clock, versionRefusedSource("src"))
	requireBuckets(t, tickSummary(t, refused, buffer), 1, 0, 0, 1)
	require.NoError(t, refused.Rebuild(ctx, rebuildOwedOrg))

	// The next attempt claims the new source version on the reset checkpoint,
	// then its backend apply fails: nothing applied, so the rebuild is still owed.
	backend := newFakeBackend()
	backend.failOrgs[rebuildOwedOrg] = true
	claiming, buffer := restartedCoordinatorOn(t, db, clock, backend, &fakeSource{name: "src", pages: 1})
	tickSummary(t, claiming, buffer)

	store, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	checkpoint, err := store.LoadProjectionCheckpoint(ctx, rebuildOwedOrg, "src")
	require.NoError(t, err)
	require.Equal(t, "test.v1", checkpoint.SourceVersion, "the claim ran")
	require.True(t, checkpoint.RebuildOwed, "a claim applies nothing and must not resolve the owed rebuild")
}
