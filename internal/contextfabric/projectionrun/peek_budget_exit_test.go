package projectionrun_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// A drain that spends its budget on the source's real final page must not
// claim budget_exceeded. One NON-APPLYING peek at budget 0 decides:
// available -> budget_exceeded, not available -> exhausted. The peek is a
// source read, never an apply, so the applied count stays at free attempt +
// budget and the checkpoint never moves because of it.
func TestPeekBeforeBudgetExceeded(t *testing.T) {
	t.Parallel()
	drain := func(t *testing.T, build bool, pages int, noPeek bool) (reason projectionrun.DrainYieldReason, sourceCalls, peeks, applied int, cursor string) {
		source := &fakeSource{name: "source-one", pages: pages}
		var wired contextfabric.ProjectionSource = source
		if noPeek {
			wired = fakeSourceNoPeek{inner: source}
		}
		observer := &recordingObserver{}
		backend := newFakeBackend()
		checkpoints := newFakeCheckpointStore()
		store := &fakeFaultyLifecycleStore{}
		if build {
			_, err := store.BeginBuild(context.Background(), "org-1", []string{"source-one"}, time.Now())
			require.NoError(t, err)
		}
		coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
			OrgIDs:  []string{"org-1"},
			Sources: []projectionrun.SourcePair{{Name: "source-one", Source: wired}},
			Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
			Lifecycle: store, EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
			GraceWindow: time.Hour, DrainBatchBudget: 1, Observer: observer, Logger: discardLogger(),
		})
		require.NoError(t, err)
		coordinator.Tick(context.Background())
		cp, err := checkpoints.LoadProjectionCheckpoint(context.Background(), "org-1", "source-one")
		require.NoError(t, err)
		return yieldReasons(observer)["source-one"], int(source.calls.Load()), int(source.peeks.Load()), backend.appliedCount(), cp.Cursor
	}
	for _, build := range []bool{false, true} {
		name := "steady"
		if build {
			name = "build"
		}
		t.Run(name+"/exact_fit_two_pages_is_exhausted", func(t *testing.T) {
			reason, calls, peeks, applied, cursor := drain(t, build, 2, false)
			require.Equal(t, projectionrun.DrainYieldExhausted, reason)
			require.Equal(t, 2, applied, "apply bound is free attempt + budget")
			require.Equal(t, 2, calls, "only the two applying reads")
			require.Equal(t, 1, peeks, "exactly one peek, at budget 0")
			require.Equal(t, "nn", cursor, "the peek does not move the checkpoint")
		})
		t.Run(name+"/three_pages_is_budget_exceeded", func(t *testing.T) {
			reason, calls, peeks, applied, cursor := drain(t, build, 3, false)
			require.Equal(t, projectionrun.DrainYieldBudgetExceeded, reason)
			require.Equal(t, 2, applied, "apply bound is free attempt + budget")
			require.Equal(t, 2, calls, "only the two applying reads")
			require.Equal(t, 1, peeks, "exactly one peek, at budget 0")
			require.Equal(t, "nn", cursor, "the peek does not move the checkpoint")
		})
		t.Run(name+"/source_without_peek_keeps_budget_exceeded", func(t *testing.T) {
			reason, calls, peeks, applied, _ := drain(t, build, 2, true)
			require.Equal(t, projectionrun.DrainYieldBudgetExceeded, reason, "a source that cannot peek side-effect free is unknown, never exhausted")
			require.Equal(t, 2, applied)
			require.Equal(t, 2, calls)
			require.Zero(t, peeks)
		})
	}
}

// fakeSourceNoPeek hides fakeSource's ProjectionPeeker capability.
type fakeSourceNoPeek struct{ inner *fakeSource }

func (f fakeSourceNoPeek) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	return f.inner.NextProjectionBatch(ctx, checkpoint)
}
