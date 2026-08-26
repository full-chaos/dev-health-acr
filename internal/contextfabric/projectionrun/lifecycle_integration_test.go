package projectionrun_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// CHAOS-3898 S2a-2: this file exercises projectionrun.Coordinator wired to
// the REAL pglifecycle.Store and pgprojection.CheckpointStore (both
// Postgres-backed, testcontainers) rather than a hand-rolled fake lifecycle
// store -- a fake would risk giving false confidence by re-implementing
// (and possibly getting wrong) the exact CAS semantics S2a's own test suite
// already pins. Only the graph backend (fakeBackend, shared with
// coordinator_test.go) and epoch-graph-deletion are faked, since neither
// needs a real FalkorDB to prove the COORDINATOR drives the lifecycle
// machinery correctly.

func newProjectionRunTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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

// lifecycleFakeSource produces exactly `pages` batches (CompleteEnumeration
// true only on the last one, matching a genuinely single-final-page source
// like episodes.go's own "fromScratch && !truncated" rule), then reports
// available=false forever -- giving a test precise control over how many
// ticks a source needs to reach a terminal build-completion mode. pages=0
// never produces a batch at all (empty_first_tick).
type lifecycleFakeSource struct {
	mu    sync.Mutex
	name  string
	pages int
	calls int
}

func (f *lifecycleFakeSource) NextProjectionBatch(_ context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls > f.pages {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	batch := validBatch(checkpoint.OrgID, f.name, checkpoint.Cursor, checkpoint.Cursor+"n")
	batch.CompleteEnumeration = f.calls == f.pages
	return batch, true, nil
}

func (f *lifecycleFakeSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// disabledSource wraps a lifecycleFakeSource and reports Enabled() == false,
// implementing contextfabric.ProjectionSourceEnablement.
type disabledSource struct{ *lifecycleFakeSource }

func (disabledSource) Enabled() bool { return false }

// fakeEpochGraphDeleter mirrors pglifecycle/executor_test.go's own
// fakeGraphDeleter -- a stand-in for falkorgraph.Adapter's DeleteEpochGraph
// so these tests exercise the retire executor's real guard logic without a
// live FalkorDB.
type fakeEpochGraphDeleter struct {
	mu      sync.Mutex
	deletes []struct {
		orgID              string
		epoch, activeEpoch int64
	}
}

func (f *fakeEpochGraphDeleter) DeleteEpochGraph(_ context.Context, orgID string, epoch, activeEpoch int64) error {
	if epoch == activeEpoch {
		return fmt.Errorf("refusing to delete the active epoch")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, struct {
		orgID              string
		epoch, activeEpoch int64
	}{orgID, epoch, activeEpoch})
	return nil
}

func (f *fakeEpochGraphDeleter) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deletes)
}

// tickUntil drives up to maxTicks Coordinator.Tick calls until check(row)
// returns true, failing the test if it never does.
func tickUntilStatus(t *testing.T, ctx context.Context, coordinator *projectionrun.Coordinator, lifecycle *pglifecycle.Store, orgID string, want contextfabric.LifecycleStatus, maxTicks int) contextfabric.OrgGraphLifecycle {
	t.Helper()
	var row contextfabric.OrgGraphLifecycle
	for i := 0; i < maxTicks; i++ {
		coordinator.Tick(ctx)
		var found bool
		var err error
		row, found, err = lifecycle.Get(ctx, orgID)
		require.NoError(t, err)
		if found && row.Status == want {
			return row
		}
	}
	t.Fatalf("organization %s never reached lifecycle status %q within %d ticks (last observed: %+v)", orgID, want, maxTicks, row)
	return row
}

func TestLifecycleBuild_RebuildDrivesTicksToFlipWithoutPurging(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	sourceB := &lifecycleFakeSource{name: "source-b", pages: 3}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-a", Source: sourceA},
			{Name: "source-b", Source: sourceB},
		},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)

	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))

	row, found, err := lifecycle.Get(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status)
	require.NotNil(t, row.TargetEpoch)
	require.Equal(t, int64(1), *row.TargetEpoch)

	// Re-running Rebuild while a build is already open must be a harmless
	// no-op, never a second BeginBuild/error.
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))

	flipped := tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 10)
	require.Equal(t, int64(1), flipped.ActiveEpoch)
	require.NotNil(t, flipped.GraceEpoch)
	require.Equal(t, int64(0), *flipped.GraceEpoch)

	progress, err := lifecycle.SourceProgress(ctx, "org-1", 1)
	require.NoError(t, err)
	require.Len(t, progress, 2)
	for _, p := range progress {
		require.NotEqual(t, contextfabric.BuildCompletionPending, p.CompletionMode, "source %s must have reached a terminal completion mode", p.Source)
		require.Positive(t, p.RowsProjected, "source %s must have accumulated rows across its paged ticks", p.Source)
	}

	// Build-aside-and-swap: the serving graph was NEVER purged.
	require.False(t, backend.purged["org-1"])

	// The target epoch's OWN checkpoint set actually advanced.
	epoch1, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-1", 1, "source-a")
	require.NoError(t, err)
	require.NotEmpty(t, epoch1.Cursor)
}

// TestChaos3826_BuildDrainAppliesTheWholeBacklogWithinASingleTick is the
// build-path half of CHAOS-3826's drain proof (chaos3826_drain_test.go
// pins the steady-state runPair path against fakes; this pins runBuildPair
// against the REAL pglifecycle.Store/pgprojection.CheckpointStore, per
// this file's own stated philosophy). Before CHAOS-3826, runBuildPair made
// exactly one RunOnce attempt per Tick, so a `pages`-page backlog needed
// `pages` ticks to reach a terminal completion mode and flip -- codex
// round-1 flagged the absence of a build-path drain test as a coverage
// gap even though the mechanism itself (runBuildPair) is shared with the
// already-tested runPair. budget = pages-1 mirrors
// TestChaos3826_DrainAppliesPendingBatchesWithinASingleTickInsteadOfOnePerTick's
// own convention: the free first attempt plus the budget's extra attempts
// exactly covers the backlog, so it flips within ONE Tick call.
func TestChaos3826_BuildDrainAppliesTheWholeBacklogWithinASingleTick(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	const pages = 20
	source := &lifecycleFakeSource{name: "source-a", pages: pages}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		GraceWindow: time.Hour, DrainBatchBudget: pages - 1, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))

	coordinator.Tick(ctx) // exactly ONE tick

	row, found, err := lifecycle.Get(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.LifecycleStatusGrace, row.Status,
		"the whole %d-page backlog should drain and flip within a single Tick call, not require %d ticks", pages, pages)
	require.Equal(t, int64(1), row.ActiveEpoch)
}

func TestLifecycleBuild_DisabledSourceCompletesWithoutTicking(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	innerB := &lifecycleFakeSource{name: "source-b", pages: 100}
	sourceB := disabledSource{innerB}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-a", Source: sourceA},
			{Name: "source-b", Source: sourceB},
		},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))

	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 10)

	progress, err := lifecycle.SourceProgress(ctx, "org-1", 1)
	require.NoError(t, err)
	byName := map[string]contextfabric.BuildSourceProgress{}
	for _, p := range progress {
		byName[p.Source] = p
	}
	require.Equal(t, contextfabric.BuildCompletionDisabledAtFreeze, byName["source-b"].CompletionMode)
	require.Zero(t, innerB.callCount(), "a disabled-at-freeze source must never be ticked")
}

func TestLifecycleBuild_EmptySourceReportsEmptyFirstTick(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 0} // zero rows, ever

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: sourceA}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))

	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 5)

	progress, err := lifecycle.SourceProgress(ctx, "org-1", 1)
	require.NoError(t, err)
	require.Len(t, progress, 1)
	require.Equal(t, contextfabric.BuildCompletionEmptyFirstTick, progress[0].CompletionMode)
}

func TestLifecycleDivergenceRecovery_OpensBuildAsideInsteadOfPurging(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: sourceA}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		GraceWindow: time.Hour, MaxBackoff: time.Millisecond, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)

	// A durable checkpoint claims a successful apply at epoch 0 (the
	// organization has never migrated), but the backend's watermark is
	// confirmed absent -- the exact CHAOS-3882 incident shape.
	require.NoError(t, checkpoints.CompareAndSwapProjectionCheckpoint(ctx,
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: "source-a"},
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: "source-a", Cursor: "c1", SourceVersion: "test.v1", BackendWatermark: "graph-watermark-1", UpdatedAt: time.Now().UTC()}))
	backend.setWatermarkErr("org-1", "source-a", contextfabric.ErrProjectionWatermarkNotFound)

	coordinator.Tick(ctx)

	require.False(t, backend.purged["org-1"], "divergence recovery must never purge a serving graph")
	row, found, err := lifecycle.Get(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status)
}

func TestCoordinatorRollback_RestoresOldEpochAndInvalidatesReuse(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	invalidator := &fakeReuseInvalidator{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: sourceA}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		ReuseInvalidator: invalidator,
		Lifecycle:        lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))
	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 10)

	require.NoError(t, coordinator.Rollback(ctx, "org-1"))

	row, found, err := lifecycle.Get(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
	require.Equal(t, int64(0), row.ActiveEpoch)
	require.Contains(t, invalidator.calls(), "org-1", "rollback must fire the same reuse-invalidation bump a flip fires")

	// A rollback outside grace is refused, not silently accepted.
	require.Error(t, coordinator.Rollback(ctx, "org-1"))
}

// fakeClock is a mutable, explicitly-advanced clock -- used here so a test
// can put an organization through a real (non-trivial) GraceWindow and
// drain bound without depending on wall-clock sleeps racing against however
// long a testcontainers-backed Tick actually takes to run.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestCoordinatorTick_RetiresAnAbandonedEpochAfterGraceAndDrain(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	graphDeleter := &fakeEpochGraphDeleter{}
	clock := newFakeClock(time.Now())
	executor := &pglifecycle.RetireExecutor{
		Store: lifecycle, Graph: graphDeleter, Checkpoints: checkpoints,
		Telemetry: contextfabric.NoopGraphLifecycleTelemetry{},
		Lease:     time.Millisecond, Deadline: time.Millisecond, Now: clock.Now,
	}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: sourceA}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch, RetireScheduler: executor,
		// A real (non-trivial) grace window: with the fake clock, "elapsing"
		// it is an explicit Advance call, never a wall-clock race against
		// however long a testcontainers-backed Tick actually takes.
		GraceWindow: time.Hour, Now: clock.Now, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))
	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 10)

	// Elapse the grace window (but not yet the retire executor's drain
	// bound -- it's evaluated relative to drain_start, which begin_retire
	// sets to the CURRENT (already-advanced) clock value, so it starts
	// fresh from here).
	clock.Advance(2 * time.Hour)
	coordinator.Tick(ctx) // sweepGraceExpirations: grace -> serving, creates the grace_expired retirement
	row, found, err := lifecycle.Get(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
	require.Equal(t, int64(1), row.ActiveEpoch)
	require.Empty(t, graphDeleter.deleteCount(), "must not delete before the drain bound elapses")

	// Now elapse the retire executor's drain bound (lease+deadline, 2ms --
	// comfortably covered).
	clock.Advance(time.Hour)
	coordinator.Tick(ctx) // sweepRetirements: drains and deletes epoch 0

	require.Equal(t, 1, graphDeleter.deleteCount())
	require.Equal(t, "org-1", graphDeleter.deletes[0].orgID)
	require.Equal(t, int64(0), graphDeleter.deletes[0].epoch)
	require.Equal(t, int64(1), graphDeleter.deletes[0].activeEpoch)

	// The retired epoch's checkpoint set was deleted TOGETHER with its graph.
	after, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-1", 0, "source-a")
	require.NoError(t, err)
	require.Empty(t, after.Cursor)
}

// fakeCheckpointStateTelemetry records only RecordCheckpointEpochState
// calls -- the one CHAOS-3898 S2a-2 signal the coordinator itself computes
// (from checkpoint cursor ages) and emits directly, independent of
// pglifecycle/falkorgraph's own telemetry wiring.
type fakeCheckpointStateTelemetry struct {
	contextfabric.NoopGraphLifecycleTelemetry
	mu     sync.Mutex
	states []struct {
		orgID string
		epoch int64
		state contextfabric.CheckpointEpochState
	}
}

func (f *fakeCheckpointStateTelemetry) RecordCheckpointEpochState(_ context.Context, orgID string, epoch int64, state contextfabric.CheckpointEpochState, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, struct {
		orgID string
		epoch int64
		state contextfabric.CheckpointEpochState
	}{orgID, epoch, state})
}

func (f *fakeCheckpointStateTelemetry) has(state contextfabric.CheckpointEpochState, epoch int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.states {
		if s.state == state && s.epoch == epoch {
			return true
		}
	}
	return false
}

func TestCoordinator_EmitsCheckpointEpochStateForBuildingThenActive(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	telemetry := &fakeCheckpointStateTelemetry{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: sourceA}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch, LifecycleTelemetry: telemetry,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))
	coordinator.Tick(ctx)
	require.True(t, telemetry.has(contextfabric.CheckpointEpochBuilding, 1), "a build tick must report the TARGET epoch as building")

	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 5)
	require.True(t, telemetry.has(contextfabric.CheckpointEpochActive, 1), "steady-state ticking after the flip must report the NEW active epoch, not epoch 0")
}

// TestLifecycleBuild_UsesTheFrozenRequiredSourcesNotLiveConfig pins the
// self-review fix: runBuildTick iterates row.RequiredSources (frozen at
// BeginBuild), not c.sourceNames (live config) -- a source configured
// AFTER a build opens must not retroactively become required for it.
func TestLifecycleBuild_UsesTheFrozenRequiredSourcesNotLiveConfig(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)

	// BeginBuild directly (bypassing Rebuild/the coordinator) with only
	// "source-a" required -- simulating a build that started before
	// "source-b" existed in configuration.
	_, err = lifecycle.BeginBuild(ctx, "org-1", []string{"source-a"}, time.Now())
	require.NoError(t, err)

	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	sourceB := &lifecycleFakeSource{name: "source-b", pages: 1} // configured, but NOT required for this already-open build

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-a", Source: sourceA},
			{Name: "source-b", Source: sourceB},
		},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)

	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 5)

	progress, err := lifecycle.SourceProgress(ctx, "org-1", 1)
	require.NoError(t, err)
	require.Len(t, progress, 1, "only the FROZEN required source must have been ticked/recorded")
	require.Equal(t, "source-a", progress[0].Source)
	require.Zero(t, sourceB.callCount(), "a source configured after the build opened must never be ticked for it")
}

func TestNewCoordinatorRequiresEpochCheckpointsWhenLifecycleIsSet(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	_ = ctx

	_, err = projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-1"},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: &lifecycleFakeSource{name: "source-a", pages: 1}}},
		Backend:        newFakeBackend(),
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle:      lifecycle, // EpochCheckpoints deliberately omitted
		Logger:         discardLogger(),
	})
	require.Error(t, err)
}
