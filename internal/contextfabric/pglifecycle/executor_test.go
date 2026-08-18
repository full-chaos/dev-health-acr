package pglifecycle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/stretchr/testify/require"
)

// fakeGraphDeleter records DeleteEpochGraph calls -- a stand-in for
// falkorgraph.Adapter so these tests exercise RetireExecutor's own guard
// logic without a live FalkorDB dependency.
type fakeGraphDeleter struct {
	mu      sync.Mutex
	deletes []struct {
		orgID              string
		epoch, activeEpoch int64
	}
}

func (f *fakeGraphDeleter) DeleteEpochGraph(_ context.Context, orgID string, epoch, activeEpoch int64) error {
	if epoch < 0 {
		return errFakeRefused("epoch must be non-negative")
	}
	if epoch == activeEpoch {
		return errFakeRefused("refusing to delete the active epoch")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, struct {
		orgID              string
		epoch, activeEpoch int64
	}{orgID, epoch, activeEpoch})
	return nil
}

type errFakeRefused string

func (e errFakeRefused) Error() string { return string(e) }

// TestRetireExecutor_RefusesTheActiveEpoch pins the isSweepTargetSafe-shaped
// final key guard (design brief §3.5): whatever a retirement record says,
// the executor refuses to delete an epoch that is CURRENTLY the
// organization's active epoch, and deletes nothing.
func TestRetireExecutor_RefusesTheActiveEpoch(t *testing.T) {
	ctx := context.Background()
	db := newLifecycleTestDatabase(t, ctx)
	store, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)

	// flipReady leaves active_epoch=1 with no retirement record for it at
	// all -- epoch 1 must be refused as the active key regardless of
	// whether a (draining) record even exists for it.
	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Hour)
	require.Equal(t, int64(1), flipped.ActiveEpoch)

	graph := &fakeGraphDeleter{}
	checkpointDeleter := checkpointDeleterFunc(func(ctx context.Context, orgID string, epoch int64) error {
		return checkpoints.DeleteEpochCheckpoints(ctx, orgID, epoch)
	})
	executor := &pglifecycle.RetireExecutor{
		Store: store, Graph: graph, Checkpoints: checkpointDeleter,
		Telemetry: contextfabric.NoopGraphLifecycleTelemetry{},
		Lease:     time.Millisecond, Deadline: time.Millisecond,
	}

	err = executor.RunOne(ctx, "org-1", 1)
	require.Error(t, err, "epoch 1 is the organization's active epoch and must never be retired")
	require.Empty(t, graph.deletes)
}

func TestRetireExecutor_RefusesBeforeDrainBoundElapses(t *testing.T) {
	ctx := context.Background()
	db := newLifecycleTestDatabase(t, ctx)
	store, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)

	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Hour)
	_, err = store.Rollback(ctx, "org-1", flipped.ActiveEpoch, time.Now())
	require.NoError(t, err)

	graph := &fakeGraphDeleter{}
	checkpointDeleter := checkpointDeleterFunc(func(ctx context.Context, orgID string, epoch int64) error {
		return checkpoints.DeleteEpochCheckpoints(ctx, orgID, epoch)
	})
	executor := &pglifecycle.RetireExecutor{
		Store: store, Graph: graph, Checkpoints: checkpointDeleter,
		Lease: time.Hour, Deadline: time.Hour,
	}
	err = executor.RunOne(ctx, "org-1", 1)
	require.Error(t, err, "the drain bound (lease+deadline) has not elapsed yet")
	require.Empty(t, graph.deletes)
}

func TestRetireExecutor_DeletesGraphAndCheckpointsThenMarksDeleted(t *testing.T) {
	ctx := context.Background()
	db := newLifecycleTestDatabase(t, ctx)
	store, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)

	flipped := flipReady(t, ctx, store, "org-1", []string{"a"}, time.Millisecond)
	before, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-1", flipped.ActiveEpoch, "a")
	require.NoError(t, err)
	advanced := before
	advanced.Cursor, advanced.UpdatedAt = "cursor-1", time.Now().UTC()
	require.NoError(t, checkpoints.CompareAndSwapProjectionCheckpointForEpoch(ctx, before, advanced))

	time.Sleep(5 * time.Millisecond)
	_, _, err = store.BeginRetire(ctx, "org-1", flipped.ActiveEpoch, time.Now(), false)
	require.NoError(t, err)

	graph := &fakeGraphDeleter{}
	checkpointDeleter := checkpointDeleterFunc(func(ctx context.Context, orgID string, epoch int64) error {
		return checkpoints.DeleteEpochCheckpoints(ctx, orgID, epoch)
	})
	executor := &pglifecycle.RetireExecutor{
		Store: store, Graph: graph, Checkpoints: checkpointDeleter,
		Lease: time.Millisecond, Deadline: time.Millisecond,
	}
	time.Sleep(5 * time.Millisecond)

	due, err := executor.DueRetirements(ctx)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, int64(0), due[0].Epoch)

	require.NoError(t, executor.RunOne(ctx, "org-1", due[0].Epoch))
	require.Len(t, graph.deletes, 1)
	require.Equal(t, int64(0), graph.deletes[0].epoch)
	require.Equal(t, int64(1), graph.deletes[0].activeEpoch)

	after, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-1", 0, "a")
	require.NoError(t, err)
	require.Empty(t, after.Cursor, "the retired epoch's checkpoint set must be deleted together with its graph")

	stillDue, err := executor.DueRetirements(ctx)
	require.NoError(t, err)
	require.Empty(t, stillDue, "a deleted retirement must leave the draining work queue")
}

// checkpointDeleterFunc adapts a plain function to
// contextfabric.EpochCheckpointDeleter.
type checkpointDeleterFunc func(ctx context.Context, orgID string, epoch int64) error

func (f checkpointDeleterFunc) DeleteEpochCheckpoints(ctx context.Context, orgID string, epoch int64) error {
	return f(ctx, orgID, epoch)
}
