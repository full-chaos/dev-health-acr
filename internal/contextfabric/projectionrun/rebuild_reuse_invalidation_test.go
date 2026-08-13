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

// TestCoordinatorRebuildSucceedsEvenWhenReuseInvalidationFails proves the
// hook is best-effort: InvalidateOrganizationReuse is not part of the
// rebuild's own success/failure contract. A rebuild that purged the
// backend and reset every checkpoint has already fully succeeded: an
// unrelated failure recording the (separate, mutable) reuse-invalidation
// fact must not be reported back as a rebuild failure, or retried forever
// by whatever retries a failed Rebuild.
func TestCoordinatorRebuildSucceedsEvenWhenReuseInvalidationFails(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	invalidator := &fakeReuseInvalidator{failWith: errors.New("invalidation store unavailable")}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		ReuseInvalidator: invalidator, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	if err := coordinator.Rebuild(context.Background(), "org-1"); err != nil {
		t.Fatalf("rebuild: %v, want the reuse-invalidation failure to degrade silently, not fail the rebuild", err)
	}
	if !backend.purged["org-1"] {
		t.Fatal("rebuild must still purge the organization's backend state")
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
