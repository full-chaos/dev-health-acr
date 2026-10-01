package projectionrun_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

const (
	refusedReleasedWarn = "context_fabric: refused build count released because the source version set changed"
	refusedCountWarn = "context_fabric: refused build count for organization"
	refusedCapWarn   = "context_fabric: refused build cap reached"
	recoveryHeldWarn = "context_fabric: automatic recovery withheld"
)

// stampingSource stamps every batch with epochGuardNewVersion but reports its
// own version only once reportable is set: before that it is a source whose
// builds the activation guard can never accept.
type stampingSource struct {
	inner      *epochGuardTeamSource
	reportable atomic.Bool
	other      atomic.Pointer[string]
}

func (s *stampingSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	return s.inner.NextProjectionBatch(ctx, checkpoint)
}

type reportingStampingSource struct{ *stampingSource }

func (s reportingStampingSource) CurrentProjectionSourceVersion() string {
	if other := s.other.Load(); other != nil {
		return *other
	}
	if s.reportable.Load() {
		return epochGuardNewVersion
	}
	return ""
}

type cappedRig struct {
	*abortRig
	buffer *bytes.Buffer
	next   *projectionrun.Coordinator
	source *stampingSource
	offset atomic.Int64
}

func newCappedRig(t *testing.T, ctx context.Context, rig *abortRig, maxRefused int) *cappedRig {
	t.Helper()
	servedGraph := newFakeBackend()
	old := rig.coordinator(abortCoordinatorOptions{budget: -1, backend: servedGraph},
		projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardOldVersion, 2)})
	old.Tick(ctx)
	served, err := rig.checkpoints.LoadProjectionCheckpointForEpoch(ctx, rig.org, 0, epochGuardSource)
	require.NoError(t, err)
	require.NotEmpty(t, served.BackendWatermark, "precondition: epoch 0 was durably projected")

	source := &stampingSource{inner: versioned(epochGuardNewVersion, 1)}
	buffer := &bytes.Buffer{}
	lostGraph := newFakeBackend()
	lostGraph.setWatermarkErr(rig.org, epochGuardSource, contextfabric.ErrProjectionWatermarkNotFound)
	capped := &cappedRig{abortRig: rig, buffer: buffer, source: source}
	capped.next = rig.coordinatorWithCap(abortCoordinatorOptions{budget: -1, logger: jsonLogger(buffer), backend: lostGraph,
		now: func() time.Time { return time.Now().Add(time.Duration(capped.offset.Load())) }}, maxRefused,
		projectionrun.SourcePair{Name: epochGuardSource, Source: reportingStampingSource{source}})
	return capped
}

func (c *cappedRig) tick(ctx context.Context) map[string]any {
	c.t.Helper()
	c.buffer.Reset()
	c.next.Tick(ctx)
	return freshnessSummary(c.t, c.buffer)
}

func (c *cappedRig) ticks(ctx context.Context, n int) {
	c.t.Helper()
	for i := 0; i < n; i++ {
		c.tick(ctx)
	}
}

func TestRefusedBuildCap(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)

	t.Run("a_self_refusing_build_stops_after_the_cap_and_the_summary_names_it", func(t *testing.T) {
		rig := newCappedRig(t, ctx, newAbortRig(t, ctx, db, "org-cap-stops"), 3)
		var counts, caps int
		for i := 0; i < 20; i++ {
			rig.buffer.Reset()
			rig.next.Tick(ctx)
			counts += len(logRecords(t, rig.buffer, refusedCountWarn))
			caps += len(logRecords(t, rig.buffer, refusedCapWarn))
		}
		_, aborts, _ := rig.telemetry.snapshot()
		require.Len(t, aborts, 3, "exactly the cap's worth of builds are opened and refused")
		require.Equal(t, 3, counts, "one Warn per refused build")
		require.Equal(t, 1, caps, "one distinct Warn at the cap")
		row := rig.row()
		require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
		require.Equal(t, int64(3), row.LastAllocatedEpoch, "no build is opened past the cap")

		summary := rig.tick(ctx)
		require.Equal(t, float64(1), summaryNumber(t, summary, "orgs_build_refused_capped"))
		require.Equal(t, float64(1), summaryNumber(t, summary, "orgs_divergence_recovered"))
		require.Equal(t, true, summary["tick_complete"], "the bucket identity balances on the capped path")
		require.Len(t, logRecords(t, rig.buffer, recoveryHeldWarn), 1)
		require.Equal(t, int64(3), rig.row().LastAllocatedEpoch)
	})

	t.Run("a_changed_source_version_reopens_and_the_build_flips", func(t *testing.T) {
		rig := newCappedRig(t, ctx, newAbortRig(t, ctx, db, "org-cap-reopens"), 2)
		rig.ticks(ctx, 20)
		require.Equal(t, int64(2), rig.row().LastAllocatedEpoch)

		rig.source.reportable.Store(true)
		flipped := tickUntilStatus(t, ctx, rig.next, rig.store, rig.org, contextfabric.LifecycleStatusGrace, 8)
		require.Equal(t, int64(3), flipped.ActiveEpoch)
		require.Equal(t, float64(0), summaryNumber(t, rig.tick(ctx), "orgs_build_refused_capped"))
	})

	t.Run("a_successful_flip_resets_the_count", func(t *testing.T) {
		rig := newCappedRig(t, ctx, newAbortRig(t, ctx, db, "org-cap-resets"), 2)
		var aborts []buildAbort
		for len(aborts) < 1 {
			rig.tick(ctx)
			_, aborts, _ = rig.telemetry.snapshot()
		}
		require.Len(t, aborts, 1, "one refusal, below the cap of two")
		rig.source.reportable.Store(true)
		tickUntilStatus(t, ctx, rig.next, rig.store, rig.org, contextfabric.LifecycleStatusGrace, 8)
		rig.source.reportable.Store(false)
		rig.offset.Store(int64(48 * time.Hour))
		rig.ticks(ctx, 30)
		_, aborts, _ = rig.telemetry.snapshot()
		require.GreaterOrEqual(t, len(aborts), 3, "after a flip the next refusals count from zero again, so the cap is not met by the stale count of one")
	})

	t.Run("a_different_refused_version_restarts_the_count", func(t *testing.T) {
		rig := newCappedRig(t, ctx, newAbortRig(t, ctx, db, "org-cap-restarts"), 3)
		var aborts []buildAbort
		for len(aborts) < 2 {
			rig.tick(ctx)
			_, aborts, _ = rig.telemetry.snapshot()
		}
		other := "devhealth.other.v99"
		rig.source.other.Store(&other)
		rig.ticks(ctx, 30)
		_, aborts, _ = rig.telemetry.snapshot()
		require.Len(t, aborts, 5, "two refusals under the first version set, then a full cap under the second")
	})

	t.Run("a_version_change_observed_by_recovery_drops_the_cap_for_good", func(t *testing.T) {
		rig := newCappedRig(t, ctx, newAbortRig(t, ctx, db, "org-cap-observed"), 2)
		rig.ticks(ctx, 20)
		require.Equal(t, int64(2), rig.row().LastAllocatedEpoch, "precondition: capped under the first version set")

		other := "devhealth.other.v99"
		rig.source.other.Store(&other)
		rig.buffer.Reset()
		rig.next.Tick(ctx)
		require.Len(t, logRecords(t, rig.buffer, refusedReleasedWarn), 1, "the release names its reason")
		require.Equal(t, contextfabric.LifecycleStatusBuilding, rig.row().Status, "recovery opened a build under the changed set")

		rig.source.other.Store(nil)
		rig.ticks(ctx, 1)
		summary := rig.tick(ctx)
		require.Equal(t, float64(0), summaryNumber(t, summary, "orgs_build_refused_capped"), "returning to the old set before any refusal does not revive the old cap")
	})
}
