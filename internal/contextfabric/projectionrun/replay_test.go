package projectionrun_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestCoordinatorReplayOfTheSameCheckpointProducesTheSameIdempotentBatch is
// the replay scenario's dedicated proof: rewinding an organization's
// checkpoint back to a cursor it has already passed through (as a manual
// replay, or a crash-recovery re-read, would) and ticking again must
// reapply a batch with the SAME deterministic BatchID
// (ProjectionBackend.ApplyProjectionBatch's documented idempotency
// contract -- ports.go) and converge back to the same next cursor, not
// drift to a different state or error.
func TestCoordinatorReplayOfTheSameCheckpointProducesTheSameIdempotentBatch(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	coordinator.Tick(ctx)
	first, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if first.Cursor == "" {
		t.Fatal("expected a real checkpoint after the initial tick")
	}
	if got := backend.appliedCount(); got != 1 {
		t.Fatalf("applied count after first tick = %d, want 1", got)
	}
	firstBatchID := backend.applied[0].BatchID

	// Simulate a replay: rewind the durable checkpoint back to the state it
	// was in before that tick (a rebuild, an out-of-band cursor rewind, or a
	// crash-recovery re-read of a stale snapshot would all produce this).
	checkpoints.mu.Lock()
	checkpoints.data[checkpoints.key("org-1", "source-a")] = contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: "source-a", Cursor: ""}
	checkpoints.mu.Unlock()

	coordinator.Tick(ctx)
	second, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if second.Cursor != first.Cursor {
		t.Fatalf("replay must converge back to the same cursor: first=%q second=%q", first.Cursor, second.Cursor)
	}
	if got := backend.appliedCount(); got != 2 {
		t.Fatalf("applied count after replay tick = %d, want 2", got)
	}
	secondBatchID := backend.applied[1].BatchID
	if secondBatchID != firstBatchID {
		t.Fatalf("replaying the same checkpoint must reapply the same deterministic batch ID: first=%q second=%q", firstBatchID, secondBatchID)
	}
}
