package projectionrun_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestCoordinatorRebuildPurgesResetsThenNextTickReplaysFromScratch proves the
// full rebuild acceptance scenario: purge the backend, reset every source's
// checkpoint, then the next Tick re-projects from an empty cursor exactly as
// it would for a brand-new organization (initial projection).
func TestCoordinatorRebuildPurgesResetsThenNextTickReplaysFromScratch(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	// Initial projection: the org has a real, non-empty checkpoint.
	coordinator.Tick(ctx)
	before, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if before.Cursor == "" {
		t.Fatal("expected a real checkpoint after the initial tick")
	}

	if err := coordinator.Rebuild(ctx, "org-1"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !backend.purged["org-1"] {
		t.Fatal("rebuild must purge the organization's backend state")
	}
	afterRebuild, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if afterRebuild.Cursor != "" {
		t.Fatalf("rebuild must reset the checkpoint to the zero cursor, got %q", afterRebuild.Cursor)
	}

	// The next tick replays from scratch: source.calls increments again
	// (the source is asked for a batch with an empty checkpoint, same as an
	// organization that has never been projected).
	callsBeforeReplay := source.calls.Load()
	coordinator.Tick(ctx)
	if source.calls.Load() <= callsBeforeReplay {
		t.Fatal("expected the next tick to call the source again after rebuild")
	}
	afterReplay, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if afterReplay.Cursor == "" {
		t.Fatal("expected the checkpoint to advance again after the post-rebuild tick")
	}
}

// TestCoordinatorRebuildDoesNotResetCheckpointsWhenPurgeFails is the purge
// scenario's own dedicated test (rebuild's happy path also exercises purge,
// but doesn't prove failure handling): if the backend purge fails, Rebuild
// must return the error and leave every checkpoint exactly where it was --
// resetting checkpoints to the zero cursor when the backend was never
// actually purged would make the next tick treat stale, un-purged backend
// state as if it were fresh, corrupting the rebuild instead of aborting it.
func TestCoordinatorRebuildDoesNotResetCheckpointsWhenPurgeFails(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	coordinator.Tick(ctx) // initial projection: a real, non-empty checkpoint
	before, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if before.Cursor == "" {
		t.Fatal("expected a real checkpoint after the initial tick")
	}

	backend.purgeErr = errors.New("backend purge failed")
	if err := coordinator.Rebuild(ctx, "org-1"); err == nil {
		t.Fatal("expected Rebuild to surface the purge failure")
	}
	after, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if after.Cursor != before.Cursor {
		t.Fatalf("a failed purge must leave the checkpoint untouched: before=%q after=%q", before.Cursor, after.Cursor)
	}
}

func TestCoordinatorRebuildRejectsEmptyOrganization(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	if err := coordinator.Rebuild(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty organization")
	}
}
