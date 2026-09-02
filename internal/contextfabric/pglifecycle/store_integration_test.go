package pglifecycle_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func newLifecycleTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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

func TestGet_AbsentOrganizationReportsNotFound(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)

	row, found, err := store.Get(ctx, "org-never-migrated")
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, contextfabric.OrgGraphLifecycle{}, row)
}

func TestBeginBuild_AllocatesEpochOneOnFirstBuild(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)

	row, err := store.BeginBuild(ctx, "org-1", []string{"dev-health-ops"}, time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(0), row.ActiveEpoch)
	require.Equal(t, int64(1), row.LastAllocatedEpoch)
	require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status)
	require.NotNil(t, row.TargetEpoch)
	require.Equal(t, int64(1), *row.TargetEpoch)
	require.Equal(t, []string{"dev-health-ops"}, row.RequiredSources)
}

func TestBeginBuild_RefusedWhileABuildIsAlreadyOpen(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	_, err = store.BeginBuild(ctx, "org-1", []string{"dev-health-ops"}, time.Now())
	require.NoError(t, err)

	_, err = store.BeginBuild(ctx, "org-1", []string{"dev-health-ops"}, time.Now())
	require.ErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused)
}

func TestFlip_RefusedUntilEverySourceReportsCompletion(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	_, err = store.BeginBuild(ctx, "org-1", []string{"a", "b"}, time.Now())
	require.NoError(t, err)

	_, err = store.Flip(ctx, "org-1", 1, time.Hour, time.Now())
	require.ErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused)

	require.NoError(t, store.RecordSourceProgress(ctx, "org-1", 1, "a", contextfabric.BuildCompletionPagedFinal, 10, time.Now()))
	_, err = store.Flip(ctx, "org-1", 1, time.Hour, time.Now())
	require.ErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused, "source b still pending must block the flip")

	// A source that can never report exhaustion (still 'pending') must
	// fail the gate forever -- it must never silently pass (design brief
	// §9 item 3). Recording it as 'pending' explicitly changes nothing.
	require.NoError(t, store.RecordSourceProgress(ctx, "org-1", 1, "b", contextfabric.BuildCompletionPending, 0, time.Now()))
	_, err = store.Flip(ctx, "org-1", 1, time.Hour, time.Now())
	require.ErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused)

	require.NoError(t, store.RecordSourceProgress(ctx, "org-1", 1, "b", contextfabric.BuildCompletionCursorExhausted, 0, time.Now()))
	row, err := store.Flip(ctx, "org-1", 1, time.Hour, time.Now())
	require.NoError(t, err)
	require.Equal(t, contextfabric.LifecycleStatusGrace, row.Status)
	require.Equal(t, int64(1), row.ActiveEpoch)
	require.NotNil(t, row.GraceEpoch)
	require.Equal(t, int64(0), *row.GraceEpoch)
	require.NotNil(t, row.GraceDeadline)
	require.Nil(t, row.TargetEpoch)
}

func flipReady(t *testing.T, ctx context.Context, store *pglifecycle.Store, orgID string, sources []string, graceWindow time.Duration) contextfabric.OrgGraphLifecycle {
	t.Helper()
	built, err := store.BeginBuild(ctx, orgID, sources, time.Now())
	require.NoError(t, err)
	for _, source := range sources {
		require.NoError(t, store.RecordSourceProgress(ctx, orgID, *built.TargetEpoch, source, contextfabric.BuildCompletionPagedFinal, 1, time.Now()))
	}
	flipped, err := store.Flip(ctx, orgID, *built.TargetEpoch, graceWindow, time.Now())
	require.NoError(t, err)
	return flipped
}

func TestRollback_RestoresGraceEpochAndRecordsRollbackAbandonedRetirement(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Hour)
	require.Equal(t, int64(1), flipped.ActiveEpoch)

	rolled, err := store.Rollback(ctx, "org-1", 1, time.Now())
	require.NoError(t, err)
	require.Equal(t, contextfabric.LifecycleStatusServing, rolled.Status)
	require.Equal(t, int64(0), rolled.ActiveEpoch)
	require.Nil(t, rolled.GraceEpoch)
	require.Nil(t, rolled.GraceDeadline)

	retirements, err := store.DrainingRetirements(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, retirements, 1)
	require.Equal(t, "org-1", retirements[0].OrgID)
	require.Equal(t, int64(1), retirements[0].Epoch)
	require.Equal(t, contextfabric.RetireReasonRollbackAbandoned, retirements[0].Reason)
	require.Equal(t, contextfabric.RetireRecordDraining, retirements[0].State)
}

func TestRollback_RefusedOutsideGrace(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)

	_, err = store.Rollback(ctx, "org-never-built", 1, time.Now())
	require.ErrorIs(t, err, contextfabric.ErrLifecycleConflict)

	_, err = store.BeginBuild(ctx, "org-1", []string{"a"}, time.Now())
	require.NoError(t, err)
	// status is 'building', not 'grace' -- rollback must refuse, regardless
	// of what epoch is named (a syntactically valid, positive epoch that
	// simply does not match the row's current 'grace' state/active_epoch).
	_, err = store.Rollback(ctx, "org-1", 1, time.Now())
	require.ErrorIs(t, err, contextfabric.ErrLifecycleConflict)
}

// TestMonotonicAllocation_BuildRollbackBuildYieldsPlusTwo pins design brief
// §5 pin 4: "epoch allocation never reuses an allocated N (pin:
// build->rollback->build yields N+2)".
func TestMonotonicAllocation_BuildRollbackBuildYieldsPlusTwo(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)

	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Hour)
	require.Equal(t, int64(1), flipped.ActiveEpoch)
	_, err = store.Rollback(ctx, "org-1", 1, time.Now())
	require.NoError(t, err)

	second, err := store.BeginBuild(ctx, "org-1", []string{"a"}, time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(2), *second.TargetEpoch, "epoch 1 was abandoned by rollback and must never be reallocated")
	require.Equal(t, int64(2), second.LastAllocatedEpoch)
}

// TestBeginRetire_RefusedBeforeGraceDeadlineWithoutForce pins the
// cf_epoch_retire refused_drain_pending-shaped guard at the lifecycle-row
// layer (the retire executor's OWN drain-bound guard is pinned separately
// in retire_executor_test.go).
func TestBeginRetire_RefusedBeforeGraceDeadlineWithoutForce(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Hour)

	_, _, err = store.BeginRetire(ctx, "org-1", flipped.ActiveEpoch, time.Now(), false)
	require.ErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused)

	// force=true bypasses the deadline gate -- the CAS/state guard still
	// applies.
	row, retirement, err := store.BeginRetire(ctx, "org-1", flipped.ActiveEpoch, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
	require.Equal(t, contextfabric.RetireReasonGraceExpired, retirement.Reason)
	require.Equal(t, int64(0), retirement.Epoch)
}

func TestBeginRetire_SucceedsAfterGraceDeadlineElapses(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	row, retirement, err := store.BeginRetire(ctx, "org-1", flipped.ActiveEpoch, time.Now(), false)
	require.NoError(t, err)
	require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
	require.Equal(t, contextfabric.RetireReasonGraceExpired, retirement.Reason)
}

// TestConcurrentRollbackAndBeginRetire_ExactlyOneWinner pins design brief
// §5 pin 4: "each lifecycle transition is a CAS -- concurrent
// flip/rollback/retire admit EXACTLY ONE winner (pin the race pairwise)".
// Rollback and BeginRetire are the two transitions legal from the SAME
// 'grace' state, competing for the identical row version -- exactly the
// race §3.5 names as structurally foreclosed.
func TestConcurrentRollbackAndBeginRetire_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Hour)

	var wg sync.WaitGroup
	var rollbackErr, retireErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, rollbackErr = store.Rollback(ctx, "org-1", flipped.ActiveEpoch, time.Now())
	}()
	go func() {
		defer wg.Done()
		_, _, retireErr = store.BeginRetire(ctx, "org-1", flipped.ActiveEpoch, time.Now(), true)
	}()
	wg.Wait()

	rollbackWon := rollbackErr == nil
	retireWon := retireErr == nil
	require.NotEqual(t, rollbackWon, retireWon, "exactly one of rollback/begin_retire must win the race")
	if rollbackWon {
		require.ErrorIs(t, retireErr, contextfabric.ErrLifecycleConflict)
	} else {
		require.ErrorIs(t, rollbackErr, contextfabric.ErrLifecycleConflict)
	}

	final, found, err := store.Get(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.LifecycleStatusServing, final.Status, "the loser must never leave the row in a half-transitioned state")
}

// TestRollback_ResumesOwnCheckpoints_NoRowSkipped pins design brief §5's
// "rollback resumes the restored epoch's OWN checkpoint set and replays
// the gap (pin: no skipped row after flip -> rollback -> tick)" AT THE
// STORAGE LAYER: epoch 0's checkpoint set is frozen at flip time and is
// never written by anything that happens under epoch 1, so a rollback
// finds it EXACTLY as it stood before the flip -- the structural guarantee
// that makes the projector-level replay (S2/conversion-slice wiring)
// sound. The checkpoint re-key migration (0020) is what makes the two
// epochs' cursor sets independently addressable at all.
func TestRollback_ResumesOwnCheckpoints_NoRowSkipped(t *testing.T) {
	ctx := context.Background()
	db := newLifecycleTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)

	// Epoch 0 is serving; ordinary ticks advance its checkpoint.
	epoch0Before, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-1", 0, "dev-health-ops")
	require.NoError(t, err)
	epoch0Advanced := epoch0Before
	epoch0Advanced.Cursor, epoch0Advanced.SourceVersion, epoch0Advanced.UpdatedAt = "cursor-pre-flip", "v1", time.Now().UTC()
	require.NoError(t, checkpoints.CompareAndSwapProjectionCheckpointForEpoch(ctx, epoch0Before, epoch0Advanced))

	flipped := flipReady(t, ctx, lifecycle, "org-1", []string{"dev-health-ops"}, time.Hour)
	require.Equal(t, int64(1), flipped.ActiveEpoch)

	// The build's own ticks write epoch 1's checkpoint set -- epoch 0's is
	// left untouched, "frozen at flip time" (design brief §3.4).
	epoch1Before, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-1", 1, "dev-health-ops")
	require.NoError(t, err)
	epoch1Advanced := epoch1Before
	epoch1Advanced.Cursor, epoch1Advanced.SourceVersion, epoch1Advanced.UpdatedAt = "cursor-post-flip", "v1", time.Now().UTC()
	require.NoError(t, checkpoints.CompareAndSwapProjectionCheckpointForEpoch(ctx, epoch1Before, epoch1Advanced))

	_, err = lifecycle.Rollback(ctx, "org-1", 1, time.Now())
	require.NoError(t, err)

	// Epoch 0's checkpoint is EXACTLY as it stood at flip time -- nothing
	// wrote it during epoch 1's serving window, so the restored epoch's
	// projector can resume from precisely this cursor and replay everything
	// that arrived since, with nothing skipped.
	epoch0AfterRollback, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-1", 0, "dev-health-ops")
	require.NoError(t, err)
	require.Equal(t, "cursor-pre-flip", epoch0AfterRollback.Cursor)

	// Epoch 1's checkpoint set is untouched by the rollback itself (deleted
	// only later, by the retire executor, together with its graph).
	epoch1AfterRollback, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-1", 1, "dev-health-ops")
	require.NoError(t, err)
	require.Equal(t, "cursor-post-flip", epoch1AfterRollback.Cursor)
}

func TestAdvanceRetirement_ConflictOnWrongExpectedState(t *testing.T) {
	ctx := context.Background()
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Hour)
	_, err = store.Rollback(ctx, "org-1", flipped.ActiveEpoch, time.Now())
	require.NoError(t, err)

	_, err = store.AdvanceRetirement(ctx, "org-1", 1, contextfabric.RetireRecordDeleting, contextfabric.RetireRecordDeleted, time.Now())
	require.ErrorIs(t, err, contextfabric.ErrLifecycleConflict)

	advanced, err := store.AdvanceRetirement(ctx, "org-1", 1, contextfabric.RetireRecordDraining, contextfabric.RetireRecordDeleting, time.Now())
	require.NoError(t, err)
	require.Equal(t, contextfabric.RetireRecordDeleting, advanced.State)
}
