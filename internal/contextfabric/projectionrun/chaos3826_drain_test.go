package projectionrun_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestChaos3826_DrainAppliesPendingBatchesWithinASingleTickInsteadOfOnePerTick
// is CHAOS-3826's red/green proof for the coordinator-level fix: a source
// with a bounded backlog of `pages` pending batches (matching a real org
// rebuild, which always converges -- unlike fakeSource's default unbounded
// stream) must apply the WHOLE backlog in far fewer ticks than one-per-page.
//
// budget = pages-1 is chosen so the free first attempt plus the budget's
// extra attempts exactly covers the backlog: it applies in ONE tick. The
// "undrained" subtest runs the IDENTICAL fixture with
// Config.DrainBatchBudget: -1 (extra draining explicitly off) and proves
// that is genuinely the pre-CHAOS-3826 behavior -- one batch per tick, so
// the same backlog needs `pages` ticks, not one.
func TestChaos3826_DrainAppliesPendingBatchesWithinASingleTickInsteadOfOnePerTick(t *testing.T) {
	t.Parallel()
	const pages = 20

	t.Run("drained", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		checkpoints := newFakeCheckpointStore()
		source := &fakeSource{name: "source-a", pages: pages}
		coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
			OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
			Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
			DrainBatchBudget: pages - 1, // free attempt + (pages-1) budget == pages
		})
		if err != nil {
			t.Fatalf("new coordinator: %v", err)
		}
		coordinator.Tick(context.Background())
		if got := backend.appliedCount(); got != pages {
			t.Fatalf("expected all %d pending batches to apply within a single tick, got %d applied", pages, got)
		}
	})

	t.Run("undrained (pre-CHAOS-3826 behavior, same fixture)", func(t *testing.T) {
		t.Parallel()
		backend := newFakeBackend()
		checkpoints := newFakeCheckpointStore()
		source := &fakeSource{name: "source-a", pages: pages}
		coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
			OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
			Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
			DrainBatchBudget: -1,
		})
		if err != nil {
			t.Fatalf("new coordinator: %v", err)
		}
		ctx := context.Background()
		for i := 0; i < pages-1; i++ {
			coordinator.Tick(ctx)
			if got := backend.appliedCount(); got != i+1 {
				t.Fatalf("tick %d: expected exactly %d applied (one batch per tick, undrained), got %d", i+1, i+1, got)
			}
		}
		coordinator.Tick(ctx)
		if got := backend.appliedCount(); got != pages {
			t.Fatalf("expected the %dth tick to finish the backlog, got %d applied", pages, got)
		}
	})
}

// orgAwareFakeSource is a per-org-bounded fakeSource variant for the
// fairness test below, where two organizations sharing ONE source
// instance (matching production, where one devhealthsource instance
// serves every org) need INDEPENDENT backlog sizes -- fakeSource's own
// `pages` bound is a single count shared across whichever org calls it,
// which cannot express "org-big has a huge backlog, org-small has one
// page."
type orgAwareFakeSource struct {
	mu    sync.Mutex
	name  string
	pages map[string]int
	calls map[string]int
	delay time.Duration
}

func (f *orgAwareFakeSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return contextfabric.ProjectionBatch{}, false, ctx.Err()
		}
	}
	f.mu.Lock()
	f.calls[checkpoint.OrgID]++
	calls := f.calls[checkpoint.OrgID]
	limit := f.pages[checkpoint.OrgID]
	f.mu.Unlock()
	if limit > 0 && calls > limit {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	next := checkpoint.Cursor + "n"
	return validBatch(checkpoint.OrgID, f.name, checkpoint.Cursor, next), true, nil
}

func (f *orgAwareFakeSource) callsFor(orgID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[orgID]
}

// TestChaos3826_LargeBacklogOrgDoesNotStarveASiblingOrgWithinTheSameTick is
// the fairness half of CHAOS-3826's proof: org-big has a backlog far
// larger than the drain budget, org-small has exactly one page. Both
// share ONE Tick() call (Concurrency=2, so both organizations' runOrg
// goroutines are dispatched concurrently, not queued behind each other).
// org-small must still get its own due()-gated attempt this tick despite
// org-big's drain -- and org-big's own drain must itself be bounded by
// the configured budget, not unbounded.
func TestChaos3826_LargeBacklogOrgDoesNotStarveASiblingOrgWithinTheSameTick(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	const budget = 50
	source := &orgAwareFakeSource{
		name:  "source-a",
		pages: map[string]int{"org-big": 1_000_000, "org-small": 1},
		calls: map[string]int{},
	}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-big", "org-small"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Concurrency: 2, DrainBatchBudget: budget, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	// org-small's own tiny backlog (1 page) takes 2 calls to fully drain --
	// the applying call, then the one that discovers nothing is left
	// (returns available=false, counted in DrainOutcome.Batches too -- see
	// TestChaos3826_DrainTelemetryReportsBatchesAndYieldReason). The point
	// under test is that this happens AT ALL within the same Tick() call
	// org-big's massive backlog is being drained in, not that it is
	// starved down to zero.
	if got := source.callsFor("org-small"); got != 2 {
		t.Fatalf("expected org-small to fully drain its own tiny backlog (2 calls: apply + confirm-exhausted) within the SAME Tick() call as org-big's drain, got %d calls -- starved by a sibling's backlog", got)
	}
	bigCalls := source.callsFor("org-big")
	if bigCalls <= 1 {
		t.Fatalf("setup invalid: expected org-big to actually drain multiple batches this tick, got %d calls", bigCalls)
	}
	if bigCalls > budget+1 { // free attempt + budget
		t.Fatalf("expected org-big's drain to be bounded by the budget (<=%d calls this tick), got %d -- fairness bound violated", budget+1, bigCalls)
	}
}

// recordingObserver captures every DrainOutcome for CHAOS-3826's telemetry
// proof below -- batches-per-tick and drain-yield-reason must actually
// reach the Observer, not just be computed and discarded.
type recordingObserver struct {
	mu     sync.Mutex
	drains []projectionrun.DrainOutcome
}

func (o *recordingObserver) ObserveProjectionOutcome(projectionrun.Outcome) {}

func (o *recordingObserver) ObserveProjectionDrain(outcome projectionrun.DrainOutcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.drains = append(o.drains, outcome)
}

func (o *recordingObserver) snapshot() []projectionrun.DrainOutcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]projectionrun.DrainOutcome(nil), o.drains...)
}

// TestChaos3826_DrainTelemetryReportsBatchesAndYieldReason proves the
// diagnosability bar (root AGENTS.md's telemetry-same-change rule): a
// drained tick's batch count and the reason it stopped draining must be
// readable from the run's own emitted telemetry, not re-derivable only by
// reading source.
func TestChaos3826_DrainTelemetryReportsBatchesAndYieldReason(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	const pages = 5
	source := &fakeSource{name: "source-a", pages: pages}
	observer := &recordingObserver{}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Observer: observer, DrainBatchBudget: 10, Logger: discardLogger(), // free + 10 budget comfortably covers 5 pages in one tick
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	drains := observer.snapshot()
	if len(drains) != 1 {
		t.Fatalf("expected exactly one DrainOutcome for (org-1, source-a) this tick, got %d: %+v", len(drains), drains)
	}
	// pages+1: the whole backlog applies, PLUS the one extra attempt that
	// discovers the source is now exhausted (available=false) -- Batches
	// counts every RunOnce attempt this tick, not only the applying ones,
	// so the reported count matches the real work (and real ClickHouse/
	// Postgres round-trips) the tick actually did.
	if drains[0].Batches != pages+1 {
		t.Fatalf("expected DrainOutcome.Batches = %d (the whole backlog plus the exhaustion-confirming attempt), got %d", pages+1, drains[0].Batches)
	}
	if drains[0].YieldReason != projectionrun.DrainYieldExhausted {
		t.Fatalf("expected DrainYieldExhausted (the backlog genuinely ran out), got %q", drains[0].YieldReason)
	}

	// A second tick must report a routine, single-attempt, exhausted drain
	// -- nothing left to pull, no false "budget_exceeded".
	coordinator.Tick(context.Background())
	drains = observer.snapshot()
	if len(drains) != 2 {
		t.Fatalf("expected a second DrainOutcome after the next tick, got %d total: %+v", len(drains), drains)
	}
	if drains[1].Batches != 1 || drains[1].YieldReason != projectionrun.DrainYieldExhausted {
		t.Fatalf("expected the steady-state tick to report exactly 1 batch, exhausted, got %+v", drains[1])
	}
}
