package pglifecycle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/stretchr/testify/require"
)

// fakeLifecycleTelemetry records every call, guarded by a mutex so the
// concurrent-CAS tests (which race two transitions against the same store)
// can read it safely after both goroutines finish.
type fakeLifecycleTelemetry struct {
	mu sync.Mutex

	casConflicts []struct {
		orgID    string
		losing   contextfabric.LifecycleTransition
		observed contextfabric.LifecycleStatus
	}
	flips []struct {
		orgID            string
		from, to         int64
		buildDuration    time.Duration
		sourcesCompleted int
	}
	rollbacks []struct {
		orgID          string
		from, to       int64
		graceRemaining time.Duration
	}
	sourceProgress []struct {
		orgID  string
		epoch  int64
		source string
		mode   contextfabric.BuildCompletionMode
	}
}

func (f *fakeLifecycleTelemetry) RecordResolvedGraphKey(context.Context, string, int64, contextfabric.GraphKeyRole, string) {
}
func (f *fakeLifecycleTelemetry) RecordGraphKeyDivergence(context.Context, string, int64, contextfabric.GraphKeyRole) {
}
func (f *fakeLifecycleTelemetry) RecordStartupPrefixAssertion(context.Context, bool) {}

func (f *fakeLifecycleTelemetry) RecordEpochFlip(_ context.Context, orgID string, fromEpoch, toEpoch int64, buildDuration time.Duration, sourcesCompleted int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flips = append(f.flips, struct {
		orgID            string
		from, to         int64
		buildDuration    time.Duration
		sourcesCompleted int
	}{orgID, fromEpoch, toEpoch, buildDuration, sourcesCompleted})
}

func (f *fakeLifecycleTelemetry) RecordEpochRollback(_ context.Context, orgID string, fromEpoch, toEpoch int64, graceRemaining time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbacks = append(f.rollbacks, struct {
		orgID          string
		from, to       int64
		graceRemaining time.Duration
	}{orgID, fromEpoch, toEpoch, graceRemaining})
}

func (f *fakeLifecycleTelemetry) RecordEpochRetire(context.Context, string, int64, contextfabric.RetireGuardVerdict, time.Duration) {
}

func (f *fakeLifecycleTelemetry) RecordLifecycleCASConflict(_ context.Context, orgID string, losing contextfabric.LifecycleTransition, observedStatus contextfabric.LifecycleStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.casConflicts = append(f.casConflicts, struct {
		orgID    string
		losing   contextfabric.LifecycleTransition
		observed contextfabric.LifecycleStatus
	}{orgID, losing, observedStatus})
}

func (f *fakeLifecycleTelemetry) RecordCheckpointEpochState(context.Context, string, int64, contextfabric.CheckpointEpochState, time.Duration) {
}

func (f *fakeLifecycleTelemetry) RecordBuildSourceProgress(_ context.Context, orgID string, epoch int64, source string, mode contextfabric.BuildCompletionMode, _ int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sourceProgress = append(f.sourceProgress, struct {
		orgID  string
		epoch  int64
		source string
		mode   contextfabric.BuildCompletionMode
	}{orgID, epoch, source, mode})
}

var _ contextfabric.GraphLifecycleTelemetry = (*fakeLifecycleTelemetry)(nil)

// TestTelemetry_BeginBuildConflict pins design brief v4.1 F6:
// cf_lifecycle_cas_conflict fires with the losing transition AND the
// current-state enum observed at failure, not merely "someone lost".
func TestTelemetry_BeginBuildConflict(t *testing.T) {
	ctx := context.Background()
	telemetry := &fakeLifecycleTelemetry{}
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	store.Telemetry = telemetry

	_, err = store.BeginBuild(ctx, "org-1", []string{"a"}, time.Now())
	require.NoError(t, err)
	_, err = store.BeginBuild(ctx, "org-1", []string{"a"}, time.Now())
	require.Error(t, err)

	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	require.Len(t, telemetry.casConflicts, 1)
	require.Equal(t, contextfabric.LifecycleTransitionBeginBuild, telemetry.casConflicts[0].losing)
	require.Equal(t, contextfabric.LifecycleStatusBuilding, telemetry.casConflicts[0].observed)
}

// TestTelemetry_FlipEmitsEpochFlipAndSourceProgress pins design brief §5b's
// cf_epoch_flip and cf_build_source_progress firing on the actual
// transitions, not merely existing as unused interface methods.
func TestTelemetry_FlipEmitsEpochFlipAndSourceProgress(t *testing.T) {
	ctx := context.Background()
	telemetry := &fakeLifecycleTelemetry{}
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	store.Telemetry = telemetry

	built, err := store.BeginBuild(ctx, "org-1", []string{"a", "b"}, time.Now())
	require.NoError(t, err)
	require.NoError(t, store.RecordSourceProgress(ctx, "org-1", *built.TargetEpoch, "a", contextfabric.BuildCompletionPagedFinal, 5, time.Now()))
	require.NoError(t, store.RecordSourceProgress(ctx, "org-1", *built.TargetEpoch, "b", contextfabric.BuildCompletionCursorExhausted, 7, time.Now()))

	_, err = store.Flip(ctx, "org-1", *built.TargetEpoch, time.Hour, time.Now())
	require.NoError(t, err)

	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	require.Len(t, telemetry.sourceProgress, 2)
	require.Len(t, telemetry.flips, 1)
	require.Equal(t, "org-1", telemetry.flips[0].orgID)
	require.Equal(t, int64(0), telemetry.flips[0].from)
	require.Equal(t, int64(1), telemetry.flips[0].to)
	require.Equal(t, 2, telemetry.flips[0].sourcesCompleted)
	require.GreaterOrEqual(t, telemetry.flips[0].buildDuration, time.Duration(0))
}

// TestTelemetry_RollbackEmitsEpochRollbackWithGraceRemaining pins
// cf_epoch_rollback firing with a positive graceRemaining when rollback
// happens well before the grace deadline.
func TestTelemetry_RollbackEmitsEpochRollbackWithGraceRemaining(t *testing.T) {
	ctx := context.Background()
	telemetry := &fakeLifecycleTelemetry{}
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	store.Telemetry = telemetry

	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Hour)
	_, err = store.Rollback(ctx, "org-1", flipped.ActiveEpoch, time.Now())
	require.NoError(t, err)

	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	require.Len(t, telemetry.rollbacks, 1)
	require.Equal(t, int64(1), telemetry.rollbacks[0].from)
	require.Equal(t, int64(0), telemetry.rollbacks[0].to)
	require.Greater(t, telemetry.rollbacks[0].graceRemaining, time.Duration(0), "rollback happened well inside the one-hour grace window")
}

// TestTelemetry_RollbackConflictReportsObservedStatus pins the F6
// current-state field for a rollback attempted outside grace.
func TestTelemetry_RollbackConflictReportsObservedStatus(t *testing.T) {
	ctx := context.Background()
	telemetry := &fakeLifecycleTelemetry{}
	store, err := pglifecycle.NewStore(newLifecycleTestDatabase(t, ctx))
	require.NoError(t, err)
	store.Telemetry = telemetry

	_, err = store.BeginBuild(ctx, "org-1", []string{"a"}, time.Now())
	require.NoError(t, err)
	_, err = store.Rollback(ctx, "org-1", 1, time.Now())
	require.Error(t, err)

	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	require.Len(t, telemetry.casConflicts, 1)
	require.Equal(t, contextfabric.LifecycleTransitionRollback, telemetry.casConflicts[0].losing)
	require.Equal(t, contextfabric.LifecycleStatusBuilding, telemetry.casConflicts[0].observed)
}
