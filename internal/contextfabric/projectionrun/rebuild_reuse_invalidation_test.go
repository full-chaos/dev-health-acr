package projectionrun_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// fakeReuseInvalidator is a fake contextfabric.ReuseInvalidator: it records
// every organization it was asked to invalidate, and can be configured to
// fail on demand.
type fakeReuseInvalidator struct {
	mu          sync.Mutex
	invalidated []string
	failWith    error
}

func (f *fakeReuseInvalidator) InvalidateOrganizationReuse(_ context.Context, orgID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	f.invalidated = append(f.invalidated, orgID)
	return nil
}

func (f *fakeReuseInvalidator) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.invalidated...)
}

// TestAC_3782_4_CompletedRebuildInvalidatesReuseForTheOrganization binds
// AC-3782-4: a completed rebuild for an organization invalidates every
// reusable result for that organization -- proved here at the Coordinator
// level, where the invalidation hook actually lives, independent of which
// store backs ReuseInvalidator.
func TestAC_3782_4_CompletedRebuildInvalidatesReuseForTheOrganization(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	invalidator := &fakeReuseInvalidator{}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		ReuseInvalidator: invalidator, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	if err := coordinator.Rebuild(context.Background(), "org-1"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if got, want := invalidator.calls(), []string{"org-1"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("invalidator.calls() = %v, want %v", got, want)
	}
}

// TestAC_3782_4_RebuildFailsAndMarkerStaysSetWhenReuseInvalidationFails is
// the Codex round-1 F5 regression. The prior behavior (invalidate AFTER
// clearing the marker, best-effort/log-and-continue) had a silent crash
// window: a failure recording the invalidation left the marker already
// cleared, so nothing would ever retry it and AC-3782-4's guarantee went
// permanently unmet for that organization. The fix: invalidate BEFORE
// clearing the marker, and an invalidator error fails the whole rebuild
// -- so the marker stays set, and the crash-resume path (runOrg's
// IsRebuildInProgress check) retries the entire idempotent sequence,
// including invalidation, on the next tick.
func TestAC_3782_4_RebuildFailsAndMarkerStaysSetWhenReuseInvalidationFails(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	markers := newFakeRebuildMarker()
	invalidator := &fakeReuseInvalidator{failWith: errors.New("invalidation store unavailable")}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: markers,
		ReuseInvalidator: invalidator, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	if err := coordinator.Rebuild(context.Background(), "org-1"); err == nil {
		t.Fatal("rebuild: want an error when reuse invalidation fails, got nil")
	}
	if !backend.purged["org-1"] {
		t.Fatal("rebuild must still have purged the organization's backend state before the invalidation step")
	}
	inProgress, err := markers.IsRebuildInProgress(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("IsRebuildInProgress: %v", err)
	}
	if !inProgress {
		t.Fatal("rebuild marker was cleared despite the invalidation failure -- a crash-resume would never retry it")
	}
}

// TestAC_3782_4_RebuildSucceedsOnceInvalidationSucceedsAfterAResumedRetry
// completes the F5 story: once the invalidator stops failing (as it
// would on a resumed tick after a transient outage), retrying the SAME
// rebuild (idempotent per performRebuild's doc comment) succeeds and
// clears the marker.
func TestAC_3782_4_RebuildSucceedsOnceInvalidationSucceedsAfterAResumedRetry(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	markers := newFakeRebuildMarker()
	invalidator := &fakeReuseInvalidator{failWith: errors.New("invalidation store unavailable")}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: markers,
		ReuseInvalidator: invalidator, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	if err := coordinator.Rebuild(context.Background(), "org-1"); err == nil {
		t.Fatal("rebuild: want the first attempt to fail")
	}

	invalidator.mu.Lock()
	invalidator.failWith = nil
	invalidator.mu.Unlock()

	if err := coordinator.Rebuild(context.Background(), "org-1"); err != nil {
		t.Fatalf("rebuild (retry): %v, want success once invalidation stops failing", err)
	}
	inProgress, err := markers.IsRebuildInProgress(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("IsRebuildInProgress: %v", err)
	}
	if inProgress {
		t.Fatal("rebuild marker should be cleared after a successful retry")
	}
	if got, want := invalidator.calls(), []string{"org-1"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("invalidator.calls() = %v, want %v (the failed first attempt must not count)", got, want)
	}
}

// TestCoordinatorRebuildWithoutReuseInvalidatorConfiguredStillSucceeds
// proves the hook is fully optional: Config.ReuseInvalidator left nil
// (its zero value, matching every production Coordinator until this is
// wired) must not panic or otherwise change Rebuild's behavior.
func TestCoordinatorRebuildWithoutReuseInvalidatorConfiguredStillSucceeds(t *testing.T) {
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

	if err := coordinator.Rebuild(context.Background(), "org-1"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
}
