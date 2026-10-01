package projectionrun_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

const (
	abortedBuildWarn = "context_fabric: build aborted because the epoch activation guard refused its epoch"
	abortFailedWarn  = "context_fabric: abort of a build the epoch activation guard refused failed"
)

type buildAbort struct {
	orgID                     string
	activeEpoch, abortedEpoch int64
}

type resolverInvalidation struct {
	orgID      string
	transition contextfabric.LifecycleTransition
}

// recordingAbortTelemetry is the one sink production hands to both the
// lifecycle store and the coordinator.
type recordingAbortTelemetry struct {
	contextfabric.NoopGraphLifecycleTelemetry
	mu            sync.Mutex
	refusals      []contextfabric.EpochActivationRefusal
	aborts        []buildAbort
	invalidations []resolverInvalidation
}

func (r *recordingAbortTelemetry) RecordEpochActivationRefused(_ context.Context, refusal contextfabric.EpochActivationRefusal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusals = append(r.refusals, refusal)
}

func (r *recordingAbortTelemetry) RecordEpochBuildAborted(_ context.Context, orgID string, activeEpoch, abortedEpoch int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aborts = append(r.aborts, buildAbort{orgID, activeEpoch, abortedEpoch})
}

func (r *recordingAbortTelemetry) RecordEpochResolverInvalidation(_ context.Context, orgID string, transition contextfabric.LifecycleTransition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidations = append(r.invalidations, resolverInvalidation{orgID, transition})
}

func (r *recordingAbortTelemetry) snapshot() ([]contextfabric.EpochActivationRefusal, []buildAbort, []resolverInvalidation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contextfabric.EpochActivationRefusal(nil), r.refusals...), append([]buildAbort(nil), r.aborts...), append([]resolverInvalidation(nil), r.invalidations...)
}

// failingAbortLifecycle is the real store with a controllable AbortBuild
// failure.
type failingAbortLifecycle struct {
	*pglifecycle.Store
	fail atomic.Bool
}

func (f *failingAbortLifecycle) AbortBuild(ctx context.Context, orgID string, expectedTargetEpoch int64, now time.Time) (contextfabric.OrgGraphLifecycle, error) {
	if f.fail.Load() {
		return contextfabric.OrgGraphLifecycle{}, errors.New("failingAbortLifecycle: injected abort failure")
	}
	return f.Store.AbortBuild(ctx, orgID, expectedTargetEpoch, now)
}

type abortRig struct {
	t           *testing.T
	ctx         context.Context
	org         string
	store       *pglifecycle.Store
	checkpoints *pgprojection.CheckpointStore
	telemetry   *recordingAbortTelemetry
	resolver    *pglifecycle.CachedResolver
}

type abortCoordinatorOptions struct {
	budget     int
	logger     *slog.Logger
	lifecycle  contextfabric.GraphLifecycleStore
	epochViews func(int64) contextfabric.ProjectionCheckpointStore
	backend    *fakeBackend
}

func newAbortRig(t *testing.T, ctx context.Context, db *sql.DB, org string) *abortRig {
	t.Helper()
	store, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	telemetry := &recordingAbortTelemetry{}
	store.Telemetry = telemetry
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	uncached, err := pglifecycle.NewResolver(store)
	require.NoError(t, err)
	frozen := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	resolver, err := pglifecycle.NewCachedResolver(uncached, pglifecycle.MaxCachedResolverLease, pglifecycle.CachedResolverOptions{Now: func() time.Time { return frozen }})
	require.NoError(t, err)
	return &abortRig{t: t, ctx: ctx, org: org, store: store, checkpoints: checkpoints, telemetry: telemetry, resolver: resolver}
}

func (r *abortRig) coordinator(options abortCoordinatorOptions, sources ...projectionrun.SourcePair) *projectionrun.Coordinator {
	r.t.Helper()
	logger := options.logger
	if logger == nil {
		logger = discardLogger()
	}
	var lifecycle contextfabric.GraphLifecycleStore = r.store
	if options.lifecycle != nil {
		lifecycle = options.lifecycle
	}
	epochViews := r.checkpoints.ForEpoch
	if options.epochViews != nil {
		epochViews = options.epochViews
	}
	backend := options.backend
	if backend == nil {
		backend = newFakeBackend()
	}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{r.org}, Sources: sources,
		Backend: backend, Checkpoints: r.checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: epochViews, LifecycleTelemetry: r.telemetry, EpochResolverInvalidator: r.resolver,
		GraceWindow: time.Hour, MaxBackoff: time.Millisecond, DrainBatchBudget: options.budget, Logger: logger,
	})
	require.NoError(r.t, err)
	return coordinator
}

func (r *abortRig) row() contextfabric.OrgGraphLifecycle {
	r.t.Helper()
	row, found, err := r.store.Get(r.ctx, r.org)
	require.NoError(r.t, err)
	require.True(r.t, found)
	return row
}

func (r *abortRig) retirements() []contextfabric.EpochRetirement {
	r.t.Helper()
	all, err := r.store.DrainingRetirements(r.ctx, time.Now().Add(24*time.Hour))
	require.NoError(r.t, err)
	var out []contextfabric.EpochRetirement
	for _, retirement := range all {
		if retirement.OrgID == r.org {
			out = append(out, retirement)
		}
	}
	return out
}

func (r *abortRig) recordedVersion(epoch int64, source string) string {
	r.t.Helper()
	checkpoint, err := r.checkpoints.LoadProjectionCheckpointForEpoch(r.ctx, r.org, epoch, source)
	require.NoError(r.t, err)
	return checkpoint.SourceVersion
}

// openOldBuild leaves the organization building at epoch 1, holding data the
// OLD binary recorded: page 1 of 2 of every named source.
func (r *abortRig) openOldBuild(sources ...string) {
	r.t.Helper()
	pairs := make([]projectionrun.SourcePair, 0, len(sources))
	for _, source := range sources {
		pairs = append(pairs, projectionrun.SourcePair{Name: source, Source: versioned(epochGuardOldVersion, 2)})
	}
	old := r.coordinator(abortCoordinatorOptions{budget: -1}, pairs...)
	require.NoError(r.t, old.Rebuild(r.ctx, r.org))
	old.Tick(r.ctx)
	row := r.row()
	require.Equal(r.t, contextfabric.LifecycleStatusBuilding, row.Status, "precondition: the OLD binary left a build open")
	require.Equal(r.t, int64(1), *row.TargetEpoch)
	for _, source := range sources {
		require.Equal(r.t, epochGuardOldVersion, r.recordedVersion(1, source), "precondition: epoch 1 holds OLD-version data")
	}
}

func jsonLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestGuardRefusedBuildIsAborted(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)

	// The whole life of a refused build: aborted on the tick that refuses it,
	// the organization ticking its own active epoch again on the next one,
	// and a new build under this binary flipping normally after that.
	t.Run("refused_build_returns_to_serving_and_the_next_build_flips", func(t *testing.T) {
		r := newAbortRig(t, ctx, db, "org-abort-life")
		r.openOldBuild(epochGuardSource)
		buildEpoch, building, err := r.resolver.ResolveBuildEpoch(ctx, r.org)
		require.NoError(t, err)
		require.True(t, building, "precondition: the write-key resolver holds the open build under its lease")
		require.Equal(t, int64(1), buildEpoch)

		var buffer bytes.Buffer
		next := r.coordinator(abortCoordinatorOptions{budget: -1, logger: jsonLogger(&buffer)},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		next.Tick(ctx)

		row := r.row()
		require.Equal(t, contextfabric.LifecycleStatusServing, row.Status, "a build the guard refused must not stay building")
		require.Equal(t, int64(0), row.ActiveEpoch)
		require.Equal(t, int64(1), row.LastAllocatedEpoch)
		require.Nil(t, row.TargetEpoch)
		require.Nil(t, row.RequiredSources)
		require.Nil(t, row.GraceEpoch)

		retirements := r.retirements()
		require.Len(t, retirements, 1)
		require.Equal(t, int64(1), retirements[0].Epoch)
		require.Equal(t, contextfabric.RetireReasonBuildAborted, retirements[0].Reason)
		require.Equal(t, contextfabric.RetireRecordDraining, retirements[0].State)

		refusals, aborts, invalidations := r.telemetry.snapshot()
		require.Equal(t, []contextfabric.EpochActivationRefusal{{
			OrgID: r.org, Transition: contextfabric.LifecycleTransitionFlip, ActiveEpoch: 0, CandidateEpoch: 1,
			Source: epochGuardSource, Reason: contextfabric.EpochActivationRefusedSourceVersion,
			RecordedSourceVersion: epochGuardOldVersion, CurrentSourceVersion: epochGuardNewVersion,
		}}, refusals)
		require.Equal(t, []buildAbort{{r.org, 0, 1}}, aborts)
		require.Contains(t, invalidations, resolverInvalidation{r.org, contextfabric.LifecycleTransitionAbortBuild})
		_, building, err = r.resolver.ResolveBuildEpoch(ctx, r.org)
		require.NoError(t, err)
		require.False(t, building, "writes must stop resolving the aborted epoch at once, not after the lease")

		warns := logRecords(t, &buffer, abortedBuildWarn)
		require.Len(t, warns, 1)
		require.Equal(t, "WARN", warns[0]["level"])
		require.Equal(t, r.org, warns[0]["org_id"])
		require.Equal(t, float64(0), warns[0]["active_epoch"])
		require.Equal(t, float64(1), warns[0]["refused_epoch"])
		require.Equal(t, epochGuardSource, warns[0]["source"])
		require.Equal(t, "source_version_mismatch", warns[0]["reason"])
		require.Equal(t, epochGuardOldVersion, warns[0]["recorded_source_version"])
		require.Equal(t, epochGuardNewVersion, warns[0]["current_source_version"])
		require.Empty(t, logRecords(t, &buffer, abortFailedWarn))
		require.Equal(t, float64(1), summaryNumber(t, freshnessSummary(t, &buffer), "orgs_rebuild_required"))

		buffer.Reset()
		next.Tick(ctx)
		refusals, aborts, _ = r.telemetry.snapshot()
		require.Len(t, refusals, 1, "the refused epoch is not a flip candidate again")
		require.Len(t, aborts, 1)
		require.Empty(t, logRecords(t, &buffer, abortedBuildWarn))
		row = r.row()
		require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
		require.Equal(t, int64(0), row.ActiveEpoch)
		require.Nil(t, row.TargetEpoch)
		require.Equal(t, int64(1), row.LastAllocatedEpoch, "the tick after the abort opens no build by itself")
		require.Equal(t, epochGuardOldVersion, r.recordedVersion(1, epochGuardSource), "the aborted epoch is left as the build recorded it")
		require.Equal(t, epochGuardNewVersion, r.recordedVersion(0, epochGuardSource), "the active epoch is projected again")

		require.NoError(t, next.Rebuild(ctx, r.org))
		flipped := tickUntilStatus(t, ctx, next, r.store, r.org, contextfabric.LifecycleStatusGrace, 5)
		require.Equal(t, int64(2), flipped.ActiveEpoch, "the new build takes a fresh epoch and flips")
		require.Equal(t, int64(0), *flipped.GraceEpoch)
		require.Equal(t, epochGuardNewVersion, r.recordedVersion(2, epochGuardSource))
		refusals, aborts, _ = r.telemetry.snapshot()
		require.Len(t, refusals, 1)
		require.Len(t, aborts, 1)
		require.Len(t, r.retirements(), 1, "only the aborted epoch is queued while grace holds epoch 0")
	})

	// The one path that opens a build without an operator is divergence
	// recovery. When the tick after the abort takes it, the build it opens is
	// a NEW epoch under this binary, never the refused one, and it flips: the
	// abort is not repeated.
	t.Run("automatic_recovery_after_the_abort_opens_a_new_epoch_that_flips", func(t *testing.T) {
		r := newAbortRig(t, ctx, db, "org-abort-then-recovery")
		servedGraph := newFakeBackend()
		old := r.coordinator(abortCoordinatorOptions{budget: -1, backend: servedGraph},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardOldVersion, 2)})
		old.Tick(ctx)
		served, err := r.checkpoints.LoadProjectionCheckpointForEpoch(ctx, r.org, 0, epochGuardSource)
		require.NoError(t, err)
		require.NotEmpty(t, served.BackendWatermark, "precondition: epoch 0 was durably projected")
		require.NoError(t, old.Rebuild(ctx, r.org))
		old.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		require.Equal(t, epochGuardOldVersion, r.recordedVersion(1, epochGuardSource))

		var buffer bytes.Buffer
		lostGraph := newFakeBackend()
		next := r.coordinator(abortCoordinatorOptions{budget: -1, logger: jsonLogger(&buffer), backend: lostGraph},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		next.Tick(ctx)
		row := r.row()
		require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
		require.Equal(t, int64(1), row.LastAllocatedEpoch)
		_, aborts, _ := r.telemetry.snapshot()
		require.Equal(t, []buildAbort{{r.org, 0, 1}}, aborts)

		next.Tick(ctx)
		row = r.row()
		require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status, "precondition: divergence recovery opened a build on the tick after the abort")
		require.Equal(t, int64(2), *row.TargetEpoch, "the automatic build takes a new epoch, never the refused one")
		require.Len(t, logRecords(t, &buffer, "context_fabric: projection checkpoint-store divergence detected"), 1)

		flipped := tickUntilStatus(t, ctx, next, r.store, r.org, contextfabric.LifecycleStatusGrace, 5)
		require.Equal(t, int64(2), flipped.ActiveEpoch)
		require.Equal(t, epochGuardNewVersion, r.recordedVersion(2, epochGuardSource))
		refusals, aborts, _ := r.telemetry.snapshot()
		require.Len(t, refusals, 1, "only the OLD binary's epoch was ever refused")
		require.Equal(t, []buildAbort{{r.org, 0, 1}}, aborts, "the abort is not repeated")
		require.Len(t, logRecords(t, &buffer, abortedBuildWarn), 1)
		var abortedEpochs []int64
		for _, retirement := range r.retirements() {
			if retirement.Reason == contextfabric.RetireReasonBuildAborted {
				abortedEpochs = append(abortedEpochs, retirement.Epoch)
			}
		}
		require.Equal(t, []int64{1}, abortedEpochs)
	})

	t.Run("each_refusing_source_is_named_and_the_build_is_aborted_once", func(t *testing.T) {
		r := newAbortRig(t, ctx, db, "org-abort-two-sources")
		r.openOldBuild("alpha", "beta")

		var buffer bytes.Buffer
		next := r.coordinator(abortCoordinatorOptions{budget: -1, logger: jsonLogger(&buffer)},
			projectionrun.SourcePair{Name: "alpha", Source: versioned(epochGuardNewVersion, 1)},
			projectionrun.SourcePair{Name: "beta", Source: versioned(epochGuardNewVersion, 1)})
		next.Tick(ctx)

		require.Equal(t, contextfabric.LifecycleStatusServing, r.row().Status)
		_, aborts, _ := r.telemetry.snapshot()
		require.Equal(t, []buildAbort{{r.org, 0, 1}}, aborts)
		warns := logRecords(t, &buffer, abortedBuildWarn)
		require.Len(t, warns, 2)
		named := []any{warns[0]["source"], warns[1]["source"]}
		require.ElementsMatch(t, []any{"alpha", "beta"}, named)
		require.Len(t, r.retirements(), 1)
	})

	// A guard that could not read the epoch's rows decided nothing about the
	// epoch: the build stays open and flips once the rows read again.
	t.Run("unreadable_checkpoint_rows_do_not_abort", func(t *testing.T) {
		r := newAbortRig(t, ctx, db, "org-abort-unreadable")
		armed := &atomic.Bool{}
		armed.Store(true)
		var buffer bytes.Buffer
		binary := r.coordinator(abortCoordinatorOptions{
			logger: jsonLogger(&buffer),
			epochViews: func(epoch int64) contextfabric.ProjectionCheckpointStore {
				return unreadableCheckpoints{ProjectionCheckpointStore: r.checkpoints.ForEpoch(epoch), armed: armed}
			},
		}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		require.NoError(t, binary.Rebuild(ctx, r.org))
		binary.Tick(ctx)

		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		refusals, aborts, _ := r.telemetry.snapshot()
		require.Len(t, refusals, 1)
		require.Equal(t, contextfabric.EpochActivationRefusedCheckpointUnreadable, refusals[0].Reason)
		require.Empty(t, aborts)
		require.Empty(t, r.retirements())
		require.Empty(t, logRecords(t, &buffer, abortedBuildWarn))
		require.Empty(t, logRecords(t, &buffer, abortFailedWarn), "no abort was attempted")

		armed.Store(false)
		binary.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusGrace, r.row().Status)
	})

	t.Run("unlistable_checkpoint_view_does_not_abort", func(t *testing.T) {
		r := newAbortRig(t, ctx, db, "org-abort-unlistable")
		var buffer bytes.Buffer
		binary := r.coordinator(abortCoordinatorOptions{
			logger: jsonLogger(&buffer),
			epochViews: func(epoch int64) contextfabric.ProjectionCheckpointStore {
				return unlistableCheckpoints{inner: r.checkpoints.ForEpoch(epoch)}
			},
		}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		require.NoError(t, binary.Rebuild(ctx, r.org))
		binary.Tick(ctx)

		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		refusals, aborts, _ := r.telemetry.snapshot()
		require.Len(t, refusals, 1)
		require.Equal(t, contextfabric.EpochActivationRefusedSourcesUnlistable, refusals[0].Reason)
		require.Empty(t, aborts)
		require.Empty(t, r.retirements())
		require.Empty(t, logRecords(t, &buffer, abortFailedWarn), "no abort was attempted")
	})

	// A tick cut short while the guard reads reached no verdict, so it must
	// not abort either. The next whole tick does.
	t.Run("tick_cancelled_inside_the_guard_does_not_abort", func(t *testing.T) {
		r := newAbortRig(t, ctx, db, "org-abort-cancelled")
		r.openOldBuild(epochGuardSource)
		armed := &atomic.Bool{}
		armed.Store(true)
		tickCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var buffer bytes.Buffer
		next := r.coordinator(abortCoordinatorOptions{
			budget: -1, logger: jsonLogger(&buffer),
			epochViews: func(epoch int64) contextfabric.ProjectionCheckpointStore {
				return cancellingCheckpoints{ProjectionCheckpointStore: r.checkpoints.ForEpoch(epoch), armed: armed, cancel: cancel}
			},
		}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		next.Tick(tickCtx)

		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		refusals, aborts, _ := r.telemetry.snapshot()
		require.Empty(t, refusals)
		require.Empty(t, aborts)
		require.Empty(t, r.retirements())
		require.Empty(t, logRecords(t, &buffer, abortFailedWarn), "no abort was attempted")

		armed.Store(false)
		next.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusServing, r.row().Status)
		_, aborts, _ = r.telemetry.snapshot()
		require.Equal(t, []buildAbort{{r.org, 0, 1}}, aborts)
	})

	// An abort that fails claims nothing: no abort Warn, no signal, no
	// resolver invalidation. The failure is its own Warn, and the next tick
	// refuses the same epoch and aborts it.
	t.Run("failed_abort_is_loud_and_the_next_tick_aborts", func(t *testing.T) {
		r := newAbortRig(t, ctx, db, "org-abort-fails")
		r.openOldBuild(epochGuardSource)
		lifecycle := &failingAbortLifecycle{Store: r.store}
		lifecycle.fail.Store(true)
		var buffer bytes.Buffer
		next := r.coordinator(abortCoordinatorOptions{budget: -1, logger: jsonLogger(&buffer), lifecycle: lifecycle},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		next.Tick(ctx)

		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		failures := logRecords(t, &buffer, abortFailedWarn)
		require.Len(t, failures, 1)
		require.Equal(t, "WARN", failures[0]["level"])
		require.Equal(t, r.org, failures[0]["org_id"])
		require.Equal(t, float64(1), failures[0]["refused_epoch"])
		require.NotEmpty(t, failures[0]["failure_class"])
		require.Empty(t, logRecords(t, &buffer, abortedBuildWarn))
		refusals, aborts, invalidations := r.telemetry.snapshot()
		require.Len(t, refusals, 1)
		require.Empty(t, aborts)
		require.NotContains(t, invalidations, resolverInvalidation{r.org, contextfabric.LifecycleTransitionAbortBuild})
		require.Empty(t, r.retirements())
		require.Equal(t, float64(1), summaryNumber(t, freshnessSummary(t, &buffer), "orgs_rebuild_required"))

		lifecycle.fail.Store(false)
		buffer.Reset()
		next.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusServing, r.row().Status)
		refusals, aborts, _ = r.telemetry.snapshot()
		require.Len(t, refusals, 2)
		require.Equal(t, []buildAbort{{r.org, 0, 1}}, aborts)
		require.Len(t, logRecords(t, &buffer, abortedBuildWarn), 1)
	})
}
