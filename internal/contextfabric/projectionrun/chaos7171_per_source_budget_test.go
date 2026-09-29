package projectionrun_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestChaos7171_LargeFirstSourceBacklogDoesNotStarveSecondSourceWithinATick
// is CHAOS-7171's red/green proof. Prod incident (2026-09-29): after an org
// rebuild the first source (dev_health_clickhouse) had a huge backlog and
// burned the ONE shared per-org drain budget, so the second source
// (dev_health_teams_projects, ~85 small pages) got only its mandatory first
// batch per tick (~32 min per page).
//
// Fixture: source-big has far more pages than the budget; source-small has
// fewer pages than the budget. Within ONE tick source-small must finish its
// whole backlog (every page applied plus the exhaustion-confirming attempt).
func TestChaos7171_LargeFirstSourceBacklogDoesNotStarveSecondSourceWithinATick(t *testing.T) {
	t.Parallel()
	const (
		budget     = 10
		bigPages   = 1000
		smallPages = 5
	)
	big := &fakeSource{name: "source-big", pages: bigPages}
	small := &fakeSource{name: "source-small", pages: smallPages}
	observer := &recordingObserver{}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-big", Source: big}, {Name: "source-small", Source: small},
		},
		Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		Observer: observer, Logger: discardLogger(), DrainBatchBudget: budget,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	if got := small.calls.Load(); got != smallPages+1 {
		t.Fatalf("source-small must drain its whole backlog in one tick (%d calls: pages + exhaustion probe), got %d calls", smallPages+1, got)
	}
	// The big source is still bounded by its own budget: free attempt + budget.
	if got := big.calls.Load(); got != budget+1 {
		t.Fatalf("source-big must stay bounded by its own budget (%d calls), got %d", budget+1, got)
	}
	reasons := map[string]projectionrun.DrainYieldReason{}
	for _, d := range observer.snapshot() {
		reasons[d.Source] = d.YieldReason
	}
	if reasons["source-big"] != projectionrun.DrainYieldBudgetExceeded || reasons["source-small"] != projectionrun.DrainYieldExhausted {
		t.Fatalf("unexpected per-source yield reasons: %+v", reasons)
	}
}
