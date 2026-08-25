package projectionrun_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestCoordinatorRefusesIncrementalProjectionAfterACrashBetweenPurgeAndReset
// is C2's fault-injection probe: simulate a crash between PurgeOrganization
// succeeding and the checkpoint reset completing (by driving the fakes
// directly through the same two steps performRebuild would take, then
// stopping -- exactly what a process crash there would leave behind: a
// marker present, a purged backend, and a checkpoint that STILL has its old
// real cursor). The invariant: no code path may run incremental projection
// against a purged-but-not-reset graph. A tick must detect the marker,
// refuse the ordinary per-source loop, and resume the rebuild instead --
// never call RunOnce against the stale, un-reset checkpoint.
func TestCoordinatorRefusesIncrementalProjectionAfterACrashBetweenPurgeAndReset(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	marker := newFakeRebuildMarker()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: marker, Logger: discardLogger(),
		// fakeSource is an unbounded stream; this test's call-count
		// bookkeeping is orthogonal to CHAOS-3826 draining.
		DrainBatchBudget: -1,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	// Get the org to a real, non-empty checkpoint (an ordinary prior projection).
	coordinator.Tick(ctx)
	staleCheckpoint, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if staleCheckpoint.Cursor == "" {
		t.Fatal("expected a real checkpoint before simulating the crash")
	}
	callsBeforeCrash := source.calls.Load()

	// Simulate the crash: mark the rebuild, purge the backend -- then stop.
	// The checkpoint is deliberately left at its stale, pre-purge cursor,
	// exactly as a real crash between these two steps would leave it.
	if err := marker.BeginRebuild(ctx, "org-1"); err != nil {
		t.Fatalf("begin rebuild: %v", err)
	}
	if err := backend.PurgeOrganization(ctx, "org-1"); err != nil {
		t.Fatalf("purge organization: %v", err)
	}
	stillStale, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if stillStale.Cursor != staleCheckpoint.Cursor {
		t.Fatal("sanity: the checkpoint must still be stale after the simulated crash")
	}

	// The next tick must NOT run incremental projection against this stale
	// checkpoint (the graph behind it was just purged) -- it must detect the
	// marker and resume the rebuild instead.
	coordinator.Tick(ctx)

	if source.calls.Load() != callsBeforeCrash {
		t.Fatalf("BUG: the source was asked for a batch using the stale, un-reset checkpoint against a purged graph (calls before=%d after=%d)",
			callsBeforeCrash, source.calls.Load())
	}
	resumed, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if resumed.Cursor != "" {
		t.Fatalf("expected the resumed rebuild to reset the checkpoint to the empty cursor, got %q", resumed.Cursor)
	}
	inProgress, err := marker.IsRebuildInProgress(ctx, "org-1")
	if err != nil {
		t.Fatalf("check rebuild marker: %v", err)
	}
	if inProgress {
		t.Fatal("expected the resumed rebuild to clear the marker")
	}

	// A FOLLOWING tick now proceeds normally (full snapshot from the reset
	// checkpoint), proving the org is fully back in service, not stuck.
	coordinator.Tick(ctx)
	if source.calls.Load() <= callsBeforeCrash {
		t.Fatal("expected the org to resume ordinary projection after the rebuild was resumed")
	}
}

// TestCoordinatorSkipsTheOrganizationWhenTheRebuildMarkerCheckItselfFails is
// the fail-safe direction: if the coordinator can't even determine whether
// a rebuild is in progress (a transient DB error reading the marker), it
// must not guess -- it must skip the tick for that organization rather than
// either running incremental projection unprotected or getting stuck.
func TestCoordinatorSkipsTheOrganizationWhenTheRebuildMarkerCheckItselfFails(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	marker := newFakeRebuildMarker()
	marker.checkErr = errors.New("marker store unavailable")
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: marker, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	if source.calls.Load() != 0 {
		t.Fatal("expected the tick to skip the organization when the rebuild marker check itself fails")
	}
}
