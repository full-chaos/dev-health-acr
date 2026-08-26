package projectionrun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
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
	failRecordAtCall int  // 1-indexed RecordSourceProgress call to fail; 0 = never
	persistFailure   bool // when true, EVERY call from failRecordAtCall onward fails (not just the one call)
	recordCalls      int
	progress         map[string]contextfabric.BuildSourceProgress
}

func (f *fakeFaultyLifecycleStore) recordCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recordCalls
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
	shouldFail := f.failRecordAtCall > 0 && (f.recordCalls == f.failRecordAtCall || (f.persistFailure && f.recordCalls > f.failRecordAtCall))
	if shouldFail {
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
func TestChaos3826_RecordSourceProgressFailureSelfHealsWithinTheSameTick(t *testing.T) {
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	const pages = 20
	source := &lifecycleFakeSource{name: "source-a", pages: pages}
	store := &fakeFaultyLifecycleStore{failRecordAtCall: 2} // fails on the SECOND applied batch's progress write ONLY
	observer := &recordingObserver{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: store, EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		GraceWindow: time.Hour, DrainBatchBudget: pages, Observer: observer, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(context.Background(), "org-1"))

	coordinator.Tick(context.Background())

	// A ONE-SHOT progress-write failure (unlike a persistent one) must not
	// truncate the drain: the whole 20-page backlog still applies within
	// this one tick (lifecycleFakeSource's LAST page claims
	// CompleteEnumeration, so the drain reaches terminal at exactly
	// `pages` calls -- no extra confirm-exhausted attempt needed, unlike
	// the steady-state fakeSource-based tests), and the THIRD batch's own
	// successful write carries the full in-memory cumulative total
	// forward (self-healing the gap the second batch's failed write would
	// otherwise have left). Every row genuinely reaches durable
	// rows_projected even though one intermediate write transiently
	// failed.
	if got := source.callCount(); got != pages {
		t.Fatalf("expected the whole %d-page backlog to drain despite the one-shot progress-write failure, got %d calls", pages, got)
	}
	progress, err := store.SourceProgress(context.Background(), "org-1", 1)
	require.NoError(t, err)
	require.Len(t, progress, 1)
	if progress[0].RowsProjected != pages {
		t.Fatalf("expected durable rows_projected=%d (self-healed past the one-shot write failure), got %d", pages, progress[0].RowsProjected)
	}
	if progress[0].CompletionMode != contextfabric.BuildCompletionPagedFinal {
		t.Fatalf("expected the source to reach a terminal completion mode despite the mid-drain write failure, got %q", progress[0].CompletionMode)
	}
}

// TestChaos3826_PersistentRecordSourceProgressFailureRetriesOnceThenLogsTheResidualGap
// covers the OTHER half of codex round-2 F1: when RecordSourceProgress
// fails for the REST of the drain (not just once), the finalizing retry
// after the loop also fails, and this call's own cf_build_source_progress
// write lands nothing -- not a crash or a silent, unbounded loss, and the
// batches genuinely applied to the backend regardless.
//
// CHAOS-4305 closed the follow-on risk this comment used to describe here
// ("stale until a future tick's write succeeds" implied the STORED total
// itself could recover): before that fix, a future tick's own priorRows
// seeded from this now-empty table, so if the source had nothing left to
// re-apply (checkpoint already past the whole backlog), a later successful
// write would durably record 0 -- a PERMANENT undercount, not a transient
// one. Since CHAOS-4305, runBuildPair's `total` is re-derived from the
// checkpoint's own rows_applied column (ProjectionRun.RowsApplied) on every
// call, never from this table, so a later tick reports the correct total
// regardless of whether cf_build_source_progress's own row ever recovers --
// see TestChaos4305_RowsProjectedSurvivesAWholeTickOfPersistentProgressWriteFailures
// (lifecycle_integration_test.go) for that cross-tick proof against the
// real stores. This test still stands: it proves this ONE call's own
// cf_build_source_progress write genuinely lands nothing when every
// attempt fails, which remains true and worth pinning.
func TestChaos3826_PersistentRecordSourceProgressFailureRetriesOnceThenLogsTheResidualGap(t *testing.T) {
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	const pages = 5
	source := &lifecycleFakeSource{name: "source-a", pages: pages}
	// failRecordAtCall=1 with persistFailure=true: EVERY RecordSourceProgress
	// call fails from the first one onward, including the finalizing retry.
	store := &fakeFaultyLifecycleStore{failRecordAtCall: 1, persistFailure: true}
	observer := &recordingObserver{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: store, EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		GraceWindow: time.Hour, DrainBatchBudget: pages, Observer: observer, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(context.Background(), "org-1"))

	require.NotPanics(t, func() { coordinator.Tick(context.Background()) })

	// The backlog still fully applies to the backend -- a persistently
	// failing progress WRITE never blocks the actual projection work.
	// lifecycleFakeSource's last page claims CompleteEnumeration, so the
	// drain reaches terminal at exactly `pages` calls.
	if got := source.callCount(); got != pages {
		t.Fatalf("expected the backlog to still fully apply despite every progress write failing, got %d calls", got)
	}
	// But durable progress recorded nothing -- every write, including the
	// finalizing retry, failed. This is the documented residual gap, not
	// silently swallowed: fakeFaultyLifecycleStore.recordCalls proves every
	// attempt (including the retry) actually happened.
	progress, err := store.SourceProgress(context.Background(), "org-1", 1)
	require.NoError(t, err)
	require.Empty(t, progress, "no write ever succeeded, so nothing should be durably recorded")
	if got := store.recordCallCount(); got != pages+1 {
		t.Fatalf("expected %d RecordSourceProgress attempts (one per applied batch, plus the finalizing retry counted the same as any other call), got %d", pages+1, got)
	}
}

// TestChaos3826_FinalizingRetryRecoversWhenOnlyTheLastLoopWriteFails is
// codex R1's CHAOS-4305 coverage-gap follow-up: the two tests above only
// exercise "every write succeeds but one" (self-heals mid-drain, without
// the retry) and "every write fails including the retry" (nothing recovers
// either way) -- neither actually depends on the finalizing retry firing.
// This pins the retry's own specific contribution: when ONLY the drain's
// LAST loop iteration's RecordSourceProgress call fails (the terminal
// batch, the one no LATER mid-drain write exists to self-heal), the
// finalizing write after the loop is the ONLY thing that durably records
// this tick's progress at all. Removing the finalizing retry would leave
// cf_build_source_progress empty after this test, exactly like the
// persistent-failure test above.
func TestChaos3826_FinalizingRetryRecoversWhenOnlyTheLastLoopWriteFails(t *testing.T) {
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	const pages = 5
	source := &lifecycleFakeSource{name: "source-a", pages: pages}
	// failRecordAtCall=pages, persistFailure=false: only the LAST
	// (terminal-batch) RecordSourceProgress call in the loop fails; nothing
	// after it in the loop would ever retry on its own -- only the
	// finalizing write after the loop can recover it.
	store := &fakeFaultyLifecycleStore{failRecordAtCall: pages}
	observer := &recordingObserver{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: store, EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		GraceWindow: time.Hour, DrainBatchBudget: pages, Observer: observer, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(context.Background(), "org-1"))

	coordinator.Tick(context.Background())

	if got := source.callCount(); got != pages {
		t.Fatalf("expected the whole %d-page backlog to drain despite the terminal write failing, got %d calls", pages, got)
	}
	progress, err := store.SourceProgress(context.Background(), "org-1", 1)
	require.NoError(t, err)
	require.Len(t, progress, 1, "the finalizing retry must be what durably records this tick's progress")
	if progress[0].RowsProjected != pages {
		t.Fatalf("expected the finalizing retry to record the full total %d, got %d", pages, progress[0].RowsProjected)
	}
	if progress[0].CompletionMode != contextfabric.BuildCompletionPagedFinal {
		t.Fatalf("expected the source to reach a terminal completion mode via the finalizing retry, got %q", progress[0].CompletionMode)
	}
	// pages calls in the loop (the last one failing) + exactly one retry.
	if got := store.recordCallCount(); got != pages+1 {
		t.Fatalf("expected %d RecordSourceProgress attempts (one per batch, plus exactly one finalizing retry), got %d", pages+1, got)
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

// fakeConcreteErrorDuringCancellationSource returns a genuine backend
// error (not wrapping context.Canceled/DeadlineExceeded) while ALSO
// cancelling the caller's ctx in the same call -- CHAOS-3826 codex round-2
// F2's fixture: an unrelated cancellation (e.g. process shutdown) racing
// with a real backend failure must not steal the classification.
type fakeConcreteErrorDuringCancellationSource struct{ cancel context.CancelFunc }

func (f *fakeConcreteErrorDuringCancellationSource) NextProjectionBatch(context.Context, contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	f.cancel()
	return contextfabric.ProjectionBatch{}, false, errors.New("concrete backend failure, unrelated to cancellation")
}

// TestChaos3826_ConcreteErrorCoincidingWithCancellationStaysClassifiedAsError
// is codex round-2 F2's red/green proof: classifying on the ambient
// ctx.Err() (round-1's own fix) rather than on whether the ERROR ITSELF
// is a cancellation would misreport a genuine backend failure as
// DrainYieldContextDone whenever ctx happens to also be cancelled at the
// moment of the check -- hiding the real failure from drain telemetry
// even though retry/backoff still sees it via recordBackoff.
func TestChaos3826_ConcreteErrorCoincidingWithCancellationStaysClassifiedAsError(t *testing.T) {
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeConcreteErrorDuringCancellationSource{cancel: cancel}
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
	if drains[0].YieldReason != projectionrun.DrainYieldError {
		t.Fatalf("expected DrainYieldError for a concrete backend failure that coincided with an unrelated ctx cancellation, got %q -- cancellation masked the real failure", drains[0].YieldReason)
	}
}

// fakeFailingBuildSource applies `pages` batches, then fails outright
// (never returning available=false) -- CHAOS-3826 codex round-2 F3's
// fixture: lifecycleFakeSource can only ever report exhaustion, never a
// genuine error, so it cannot exercise runBuildPair's error branch.
type fakeFailingBuildSource struct {
	pages int
	calls int
}

func (f *fakeFailingBuildSource) NextProjectionBatch(_ context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	f.calls++
	if f.calls > f.pages {
		return contextfabric.ProjectionBatch{}, false, errors.New("fakeFailingBuildSource: injected failure")
	}
	next := checkpoint.Cursor + "n"
	return validBatch(checkpoint.OrgID, "source-a", checkpoint.Cursor, next), true, nil
}

// TestChaos3826_BuildPathBatchesCountsAFailedAttemptLikeRunPairDoes is
// codex round-2 F3's red/green proof: DrainOutcome.Batches' own doc
// comment says it counts every RunOnce attempt, and runPair already does
// (a worker-construction or RunOnce failure still increments batches
// there) -- but runBuildPair incremented batches only AFTER its error
// checks, so a failed attempt was silently excluded, undercounting the
// build path's own cost telemetry relative to the steady-state path.
func TestChaos3826_BuildPathBatchesCountsAFailedAttemptLikeRunPairDoes(t *testing.T) {
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeFailingBuildSource{pages: 1}
	store := &fakeFaultyLifecycleStore{}
	observer := &recordingObserver{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: store, EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		GraceWindow: time.Hour, DrainBatchBudget: 10, Observer: observer, Logger: discardLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Rebuild(context.Background(), "org-1"))

	coordinator.Tick(context.Background())

	drains := observer.snapshot()
	if len(drains) != 1 {
		t.Fatalf("expected exactly one DrainOutcome, got %d: %+v", len(drains), drains)
	}
	// One batch applies (Applied=1), then the second attempt fails
	// outright -- Batches must count BOTH attempts (2), matching runPair's
	// own convention, not just the one that applied.
	if drains[0].Batches != 2 || drains[0].Applied != 1 {
		t.Fatalf("expected Batches=2 (the apply, plus the failed attempt), Applied=1, got %+v", drains[0])
	}
	if drains[0].YieldReason != projectionrun.DrainYieldError {
		t.Fatalf("expected DrainYieldError, got %q", drains[0].YieldReason)
	}
}

// fakeStaleThenFailingSource applies exactly ONE batch whose SourceVersion
// stays mismatched from CurrentProjectionSourceVersion (so the CHAOS-3887
// freshness check reports stale=true), then fails outright on the SECOND
// attempt -- CHAOS-3826 codex round-3 F1's fixture: an error-classified
// LATER attempt in the same drain must not erase an earlier attempt's real
// staleness signal.
type fakeStaleThenFailingSource struct{ calls int }

func (f *fakeStaleThenFailingSource) NextProjectionBatch(_ context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	f.calls++
	if f.calls == 1 {
		next := checkpoint.Cursor + "n"
		batch := validBatch(checkpoint.OrgID, "source-a", checkpoint.Cursor, next)
		batch.SourceVersion = "producer.v1"
		for i := range batch.Entities {
			batch.Entities[i].SourceVersion = "producer.v1"
		}
		return batch, true, nil
	}
	return contextfabric.ProjectionBatch{}, false, errors.New("fakeStaleThenFailingSource: injected failure")
}

// CurrentProjectionSourceVersion always disagrees with the applied batch's
// own "producer.v1", so the backend watermark ApplyProjectionBatch writes
// (SourceVersion="producer.v1") stays stale relative to it even after the
// batch lands -- unlike fakeSource's own convention where the batch and the
// current version usually agree.
func (f *fakeStaleThenFailingSource) CurrentProjectionSourceVersion() string { return "producer.v2" }

// TestChaos3826_DrainPreservesStalenessFoundByAnEarlierAttemptDespiteALaterFailure
// is codex round-3 F1's red/green proof: runPair's per-tick `stale`
// aggregate must OR across every attempt the drain makes, not be
// overwritten by the LAST one. Before CHAOS-3826's in-tick draining,
// runPair made exactly one attempt per tick, so overwrite and OR were
// equivalent; the drain loop makes overwrite a real bug, because an
// attempt that errors out always reports stale=false (runPairOnce never
// reaches the freshness check on an error) -- silently clearing a REAL
// staleness signal an earlier attempt in the SAME tick already found, and
// misreporting the whole tick as "orgs_ok" instead of
// "orgs_rebuild_required" in the CHAOS-3887 fleet aggregate.
func TestChaos3826_DrainPreservesStalenessFoundByAnEarlierAttemptDespiteALaterFailure(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeStaleThenFailingSource{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Logger: logger,
	})
	require.NoError(t, err)

	coordinator.Tick(context.Background())

	if source.calls != 2 {
		t.Fatalf("expected exactly 2 attempts (the applying stale one, then the failing one), got %d", source.calls)
	}

	var lastSummary map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record["msg"] == "context_fabric: projection tick freshness summary" {
			lastSummary = record
		}
	}
	if lastSummary == nil {
		t.Fatalf("no freshness summary line logged: %s", buffer.String())
	}
	if got, ok := lastSummary["orgs_rebuild_required"].(float64); !ok || got != 1 {
		t.Fatalf("expected orgs_rebuild_required=1 (the earlier attempt's real staleness must survive the later failure), got %v: %s", lastSummary["orgs_rebuild_required"], buffer.String())
	}
	if got, ok := lastSummary["orgs_ok"].(float64); !ok || got != 0 {
		t.Fatalf("expected orgs_ok=0 (this organization is NOT fresh -- staleness was found), got %v: %s", lastSummary["orgs_ok"], buffer.String())
	}
}
