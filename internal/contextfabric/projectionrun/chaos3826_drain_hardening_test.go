package projectionrun_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

// fakeFaultyLifecycleStore is a minimal contextfabric.GraphLifecycleStore
// fake for CHAOS-3826 codex round-1 F2's regression coverage: a real
// Postgres store (lifecycle_integration_test.go's own stated philosophy)
// can't cheaply inject "RecordSourceProgress fails on exactly the Nth
// call, mid-drain" without racing real CAS/write timing -- this fake
// controls it directly. Deliberately minimal: only the methods
// runBuildTick's drain path actually exercises do real bookkeeping: the
// rest return "not implemented" the same way fakeRefusalLifecycleStore
// (begin_lifecycle_build_test.go) does for methods outside its own scope.
type fakeFaultyLifecycleStore struct {
	mu               sync.Mutex
	row              contextfabric.OrgGraphLifecycle
	found            bool
	nextEpoch        int64
	failRecordAtCall int // 1-indexed RecordSourceProgress call to fail; 0 = never
	recordCalls      int
	progress         map[string]contextfabric.BuildSourceProgress
}

func (f *fakeFaultyLifecycleStore) Get(context.Context, string) (contextfabric.OrgGraphLifecycle, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.row, f.found, nil
}

func (f *fakeFaultyLifecycleStore) BeginBuild(_ context.Context, _ string, requiredSources []string, _ time.Time) (contextfabric.OrgGraphLifecycle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.found && f.row.Status == contextfabric.LifecycleStatusBuilding {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.ErrLifecycleTransitionRefused
	}
	f.nextEpoch++
	target := f.nextEpoch
	f.row = contextfabric.OrgGraphLifecycle{ActiveEpoch: 0, Status: contextfabric.LifecycleStatusBuilding, TargetEpoch: &target, RequiredSources: requiredSources}
	f.found = true
	f.progress = map[string]contextfabric.BuildSourceProgress{}
	return f.row, nil
}

func (f *fakeFaultyLifecycleStore) RecordSourceProgress(_ context.Context, _ string, _ int64, source string, mode contextfabric.BuildCompletionMode, rows int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCalls++
	if f.failRecordAtCall > 0 && f.recordCalls == f.failRecordAtCall {
		return errors.New("fakeFaultyLifecycleStore: injected progress write failure")
	}
	f.progress[source] = contextfabric.BuildSourceProgress{Source: source, CompletionMode: mode, RowsProjected: rows}
	return nil
}

func (f *fakeFaultyLifecycleStore) SourceProgress(context.Context, string, int64) ([]contextfabric.BuildSourceProgress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]contextfabric.BuildSourceProgress, 0, len(f.progress))
	for _, p := range f.progress {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeFaultyLifecycleStore) Flip(_ context.Context, _ string, expectedTargetEpoch int64, _ time.Duration, _ time.Time) (contextfabric.OrgGraphLifecycle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.row.RequiredSources {
		p, ok := f.progress[s]
		if !ok || p.CompletionMode == contextfabric.BuildCompletionPending {
			return contextfabric.OrgGraphLifecycle{}, contextfabric.ErrLifecycleTransitionRefused
		}
	}
	f.row.Status = contextfabric.LifecycleStatusGrace
	f.row.ActiveEpoch = expectedTargetEpoch
	f.row.TargetEpoch = nil
	return f.row, nil
}

func (f *fakeFaultyLifecycleStore) Rollback(context.Context, string, int64, time.Time) (contextfabric.OrgGraphLifecycle, error) {
	return contextfabric.OrgGraphLifecycle{}, errors.New("fakeFaultyLifecycleStore: not implemented")
}

func (f *fakeFaultyLifecycleStore) BeginRetire(context.Context, string, int64, time.Time, bool) (contextfabric.OrgGraphLifecycle, contextfabric.EpochRetirement, error) {
	return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, errors.New("fakeFaultyLifecycleStore: not implemented")
}

func (f *fakeFaultyLifecycleStore) DrainingRetirements(context.Context, time.Time) ([]contextfabric.EpochRetirement, error) {
	return nil, errors.New("fakeFaultyLifecycleStore: not implemented")
}

func (f *fakeFaultyLifecycleStore) AdvanceRetirement(context.Context, string, int64, contextfabric.RetireRecordState, contextfabric.RetireRecordState, time.Time) (contextfabric.EpochRetirement, error) {
	return contextfabric.EpochRetirement{}, errors.New("fakeFaultyLifecycleStore: not implemented")
}

var _ contextfabric.GraphLifecycleStore = (*fakeFaultyLifecycleStore)(nil)

// TestChaos3826_RecordSourceProgressFailureStopsTheBuildDrainInsteadOfBurningBudget
// is codex round-1 F2's red/green proof: before the fix, runBuildPair
// logged a RecordSourceProgress failure and kept draining, spending up to
// the WHOLE per-tick budget on top of a batch whose durable progress
// never got recorded (widening the pre-CHAOS-3826 blast radius -- at most
// one unrecorded batch per tick, since there was only ever one attempt --
// to up to `budget` unrecorded batches in a single tick). With the fix,
// the drain stops the instant a progress write fails.
func TestChaos3826_RecordSourceProgressFailureStopsTheBuildDrainInsteadOfBurningBudget(t *testing.T) {
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &lifecycleFakeSource{name: "source-a", pages: 20}
	store := &fakeFaultyLifecycleStore{failRecordAtCall: 2} // fails on the SECOND applied batch's progress write
	observer := &recordingObserver{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: store, EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		GraceWindow: time.Hour, DrainBatchBudget: 50, Observer: observer, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(context.Background(), "org-1"))

	coordinator.Tick(context.Background())

	// The backlog has 20 pages and a budget of 50 -- if the drain kept
	// going after the injected failure, it would consume most/all of the
	// backlog in this ONE tick despite it. With the fix, exactly 2 RunOnce
	// attempts happen: the batch that applied cleanly, then the one whose
	// RecordSourceProgress write failed (which itself DID apply to the
	// backend -- see the Applied assertion below -- only its durable
	// lifecycle bookkeeping failed).
	if got := source.callCount(); got != 2 {
		t.Fatalf("expected the drain to stop after exactly 2 RunOnce attempts, got %d -- a RecordSourceProgress failure did not stop the drain", got)
	}

	drains := observer.snapshot()
	if len(drains) != 1 {
		t.Fatalf("expected exactly one DrainOutcome for (org-1, source-a), got %d: %+v", len(drains), drains)
	}
	if drains[0].Batches != 2 || drains[0].Applied != 2 {
		t.Fatalf("expected Batches=2, Applied=2 (both attempts applied; only the second's progress write failed), got %+v", drains[0])
	}
	if drains[0].YieldReason != projectionrun.DrainYieldError {
		t.Fatalf("expected DrainYieldError from the progress-write failure, got %q", drains[0].YieldReason)
	}
}

// TestChaos3826_DrainAppliedCountExcludesTheConfirmExhaustedProbe is codex
// round-1 F1's red/green proof: DrainOutcome.Applied must count only the
// attempts that actually applied a batch, distinct from Batches (which
// also counts the mandatory confirm-exhausted attempt every drain makes
// once it runs dry -- see DrainOutcome's own doc comment). Without this
// distinction, SlogObserver logs an ordinary single-new-batch tick as
// "drained multiple batches" at Info, because Batches==2 for that case
// even though only one batch genuinely applied.
func TestChaos3826_DrainAppliedCountExcludesTheConfirmExhaustedProbe(t *testing.T) {
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	const pages = 5
	source := &fakeSource{name: "source-a", pages: pages}
	observer := &recordingObserver{}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Observer: observer, DrainBatchBudget: 10, Logger: discardLogger(),
	})
	require.NoError(t, err)

	coordinator.Tick(context.Background())

	drains := observer.snapshot()
	if len(drains) != 1 {
		t.Fatalf("expected exactly one DrainOutcome, got %d: %+v", len(drains), drains)
	}
	// Batches == pages+1 (the whole backlog plus the confirm-exhausted
	// attempt); Applied == pages (only the attempts that actually applied).
	if drains[0].Batches != pages+1 {
		t.Fatalf("expected Batches=%d, got %d", pages+1, drains[0].Batches)
	}
	if drains[0].Applied != pages {
		t.Fatalf("expected Applied=%d (excludes the confirm-exhausted probe), got %d", pages, drains[0].Applied)
	}

	// The steady-state second tick: one due()-gated attempt discovers
	// nothing new. Batches==1, Applied==0 -- routine, must stay at Debug
	// (asserted directly against SlogObserver in observer_test.go-style
	// unit coverage below; here we only pin the counts DrainOutcome
	// reports so a future regression in the counting itself is caught
	// independent of the logging-level test).
	coordinator.Tick(context.Background())
	drains = observer.snapshot()
	if len(drains) != 2 {
		t.Fatalf("expected a second DrainOutcome, got %d total: %+v", len(drains), drains)
	}
	if drains[1].Batches != 1 || drains[1].Applied != 0 {
		t.Fatalf("expected the steady-state tick to report Batches=1, Applied=0, got %+v", drains[1])
	}
}

// fakeCancelledSource applies exactly one batch, then on its SECOND call
// cancels the caller-supplied ctx and fails with ctx.Err() -- CHAOS-3826
// codex round-1 F3's fixture, reproducing the shape a real
// ClickHouse/Postgres round-trip failing on cancellation takes (cancel,
// then the in-flight query returns context.Canceled), which fakeSource's
// own unconditional-success stub cannot. The first call must succeed
// (and ctx must still be live afterward) so the drain loop actually
// makes a second attempt instead of stopping after the first for an
// unrelated reason.
type fakeCancelledSource struct {
	cancel context.CancelFunc
	calls  int
}

func (f *fakeCancelledSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	f.calls++
	if f.calls == 1 {
		next := checkpoint.Cursor + "n"
		return validBatch(checkpoint.OrgID, "source-a", checkpoint.Cursor, next), true, nil
	}
	f.cancel()
	return contextfabric.ProjectionBatch{}, false, ctx.Err()
}

// TestChaos3826_DrainClassifiesACancelledAttemptAsContextDoneNotError is
// codex round-1 F3's red/green proof: a RunOnce failure caused by ctx
// cancellation must classify as DrainYieldContextDone, not
// DrainYieldError -- before the fix, the `failed` case in runPair's
// switch was checked before ctx.Err(), making DrainYieldContextDone
// unreachable from a cancelled RunOnce call.
func TestChaos3826_DrainClassifiesACancelledAttemptAsContextDoneNotError(t *testing.T) {
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeCancelledSource{cancel: cancel}
	observer := &recordingObserver{}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Observer: observer, Logger: discardLogger(),
	})
	require.NoError(t, err)

	coordinator.Tick(ctx)

	drains := observer.snapshot()
	if len(drains) != 1 {
		t.Fatalf("expected exactly one DrainOutcome, got %d: %+v", len(drains), drains)
	}
	if drains[0].Batches != 2 {
		t.Fatalf("expected exactly 2 attempts (the applying one, then the one that observed cancellation), got %+v", drains[0])
	}
	if drains[0].YieldReason != projectionrun.DrainYieldContextDone {
		t.Fatalf("expected DrainYieldContextDone for a RunOnce call that failed because ctx was cancelled, got %q", drains[0].YieldReason)
	}
}
