package projectionrun_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestCoordinatorRebuildRejectsAnOrganizationOutsideTheAllowlist is C1's
// (codex finding C3) probe promoted to a permanent regression test:
// Rebuild must never act on an organization the coordinator wasn't
// configured to project, even though the caller supplies an arbitrary ID
// directly (cmd/acr-projector's `rebuild --org` flag). Before the fix this
// purged the backend for org-attacker despite only org-allowed being
// configured.
func TestCoordinatorRebuildRejectsAnOrganizationOutsideTheAllowlist(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-allowed"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	if err := coordinator.Rebuild(context.Background(), "org-attacker"); err == nil {
		t.Fatal("expected Rebuild to reject an organization outside the allowlist")
	}
	if backend.purged["org-attacker"] {
		t.Fatal("an out-of-allowlist Rebuild call must never purge the backend")
	}
}

func TestCoordinatorRebuildAllowsAnOrganizationInsideTheAllowlist(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-allowed"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	if err := coordinator.Rebuild(context.Background(), "org-allowed"); err != nil {
		t.Fatalf("expected Rebuild to succeed for an allowlisted organization: %v", err)
	}
	if !backend.purged["org-allowed"] {
		t.Fatal("expected the allowlisted organization to be purged")
	}
}
