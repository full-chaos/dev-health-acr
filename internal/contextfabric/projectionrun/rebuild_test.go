package projectionrun_test

import (
	"context"
	"errors"
	"testing"
	"time"

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
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
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
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
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
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	if err := coordinator.Rebuild(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty organization")
	}
}

// TestCoordinatorRebuildClearsASurvivingClaimSoANewVersionCanProceed is
// CHAOS-3779 codex round-4 finding M2's regression test. Probed first
// (temporary reproduction, deleted before this commit): reproduced
// codex's exact sequence against pre-fix code -- a surviving M1 claim
// (Cursor="", SourceVersion="v1", left behind by a first-ever apply that
// failed partway) was NOT cleared by an explicit Rebuild, because
// resetAllCheckpoints skipped any checkpoint with Cursor == "" outright,
// treating it as "nothing to reset." That skip predates the M1 claim
// mechanism, when Cursor == "" only ever meant a genuinely untouched
// checkpoint; once a claim can carry a non-empty SourceVersion at that
// same empty cursor, the skip instead makes the wedge PERMANENT: every
// future batch, regardless of version, hits the H2-residual mismatch
// guard forever, and rebuild -- the guard's own documented recovery path
// -- could never clear it.
//
// This proves both halves in one sequence: the wedge holds with no
// rebuild (the H2-residual guard doing exactly what it should), and an
// explicit rebuild clears it so projection can proceed again.
func TestCoordinatorRebuildClearsASurvivingClaimSoANewVersionCanProceed(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.failOrgs["org-1"] = true // tick 1's apply fails partway (simulated)
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a", sourceVersion: "v1"}
	// Injected, manually-advanced clock: this test drives three ticks
	// back to back, and the coordinator's per-(org,source) backoff would
	// otherwise make the third tick a same-instant no-op after two
	// consecutive failures (tick1's apply error, tick2's refusal) --
	// nothing to do with M2 itself, just the ordinary backoff any two
	// failures in a row would trigger.
	clock := time.Now().UTC()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	// Tick 1: v1's apply fails. The M1 claim must still be saved.
	coordinator.Tick(ctx)
	claimed, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if claimed.Cursor != "" || claimed.SourceVersion != "v1" {
		t.Fatalf("setup invalid: want a surviving claim (Cursor=\"\", SourceVersion=\"v1\"), got %#v", claimed)
	}

	// The backend is now healthy and a NEW version (v2) is arriving --
	// but without a rebuild, the surviving v1 claim must still refuse it.
	// This is the H2-residual guard working correctly; M2 is specifically
	// about what happens AFTER a rebuild, not this refusal itself. The
	// clock advances well past the one-failure backoff window so this
	// tick actually runs rather than being skipped as not-yet-due --
	// ordinary backoff behavior, unrelated to M2.
	backend.failOrgs["org-1"] = false
	source.sourceVersion = "v2"
	appliedBeforeRebuild := backend.appliedCount()
	callsBeforeSecondTick := source.calls.Load()
	clock = clock.Add(time.Minute)
	coordinator.Tick(ctx)
	if source.calls.Load() <= callsBeforeSecondTick {
		t.Fatal("tick2 setup invalid: the source was never called -- backoff likely skipped this tick rather than the guard refusing it, which would prove nothing")
	}
	stillWedged, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if stillWedged != claimed {
		t.Fatalf("checkpoint changed without a rebuild -- want it to stay wedged at the surviving v1 claim: before=%#v after=%#v", claimed, stillWedged)
	}
	if backend.appliedCount() != appliedBeforeRebuild {
		t.Fatalf("backend.appliedCount() = %d, want unchanged at %d -- the v2 batch must never reach the backend while the v1 claim is unresolved", backend.appliedCount(), appliedBeforeRebuild)
	}

	// Rebuild must clear the surviving claim -- the actual M2 fix.
	if err := coordinator.Rebuild(ctx, "org-1"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	afterRebuild, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if afterRebuild.Cursor != "" || afterRebuild.SourceVersion != "" {
		t.Fatalf("rebuild did not clear the surviving claim: %#v", afterRebuild)
	}

	// The v2 batch now proceeds on the very next tick (clock advanced
	// past backoff again, for the same reason as tick2 above).
	clock = clock.Add(time.Minute)
	coordinator.Tick(ctx)
	afterReplay, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if afterReplay.SourceVersion != "v2" || afterReplay.Cursor == "" {
		t.Fatalf("expected the v2 batch to apply cleanly after rebuild, got %#v", afterReplay)
	}
	if backend.appliedCount() != appliedBeforeRebuild+1 {
		t.Fatalf("backend.appliedCount() = %d, want %d -- the post-rebuild v2 batch must have reached the backend exactly once", backend.appliedCount(), appliedBeforeRebuild+1)
	}
}
