package projectionrun_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

// CHAOS-7171's per-source drain budget was pinned only on runOrgLegacy.
// Restoring the shared counter in runOrgLifecycle or runBuildTick kept the
// suite green. These two tests pin the other two drain loops: a large first
// source must not starve a small second source within ONE tick.

const (
	fairnessBudget     = 10
	fairnessBigPages   = 1000
	fairnessSmallPages = 5
)

func newFairnessCoordinator(t *testing.T, store *fakeFaultyLifecycleStore, big, small *lifecycleFakeSource, observer *recordingObserver) *projectionrun.Coordinator {
	t.Helper()
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-big", Source: big}, {Name: "source-small", Source: small},
		},
		Backend: newFakeBackend(), Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: store, EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		GraceWindow: time.Hour, DrainBatchBudget: fairnessBudget, Observer: observer, Logger: discardLogger(),
	})
	require.NoError(t, err)
	return coordinator
}

func yieldReasons(observer *recordingObserver) map[string]projectionrun.DrainYieldReason {
	reasons := map[string]projectionrun.DrainYieldReason{}
	for _, d := range observer.snapshot() {
		reasons[d.Source] = d.YieldReason
	}
	return reasons
}

// Steady-state lifecycle tick (runOrgLifecycle): no lifecycle row = serving.
func TestChaos7179_LifecycleTickLargeFirstSourceDoesNotStarveSecondSource(t *testing.T) {
	t.Parallel()
	big := &lifecycleFakeSource{name: "source-big", pages: fairnessBigPages}
	small := &lifecycleFakeSource{name: "source-small", pages: fairnessSmallPages}
	observer := &recordingObserver{}
	coordinator := newFairnessCoordinator(t, &fakeFaultyLifecycleStore{}, big, small, observer)

	coordinator.Tick(context.Background())

	require.Equal(t, fairnessSmallPages+1, small.callCount(), "source-small must drain its whole backlog in one tick (pages + exhaustion probe)")
	require.Equal(t, fairnessBudget+1, big.callCount(), "source-big stays bounded by its own budget (free attempt + budget)")
	reasons := yieldReasons(observer)
	require.Equal(t, projectionrun.DrainYieldBudgetExceeded, reasons["source-big"])
	require.Equal(t, projectionrun.DrainYieldExhausted, reasons["source-small"])
}

// Build tick (runBuildTick): lifecycle row is building with both sources required.
func TestChaos7179_BuildTickLargeFirstSourceDoesNotStarveSecondSource(t *testing.T) {
	t.Parallel()
	big := &lifecycleFakeSource{name: "source-big", pages: fairnessBigPages}
	small := &lifecycleFakeSource{name: "source-small", pages: fairnessSmallPages}
	observer := &recordingObserver{}
	store := &fakeFaultyLifecycleStore{}
	_, err := store.BeginBuild(context.Background(), "org-1", []string{"source-big", "source-small"}, time.Now())
	require.NoError(t, err)
	coordinator := newFairnessCoordinator(t, store, big, small, observer)

	coordinator.Tick(context.Background())

	// Build drains stop at CompleteEnumeration on the last page: no probe call.
	require.Equal(t, fairnessSmallPages, small.callCount(), "source-small must drain its whole backlog in one build tick")
	require.Equal(t, fairnessBudget+1, big.callCount(), "source-big stays bounded by its own budget (free attempt + budget)")
	reasons := yieldReasons(observer)
	require.Equal(t, projectionrun.DrainYieldBudgetExceeded, reasons["source-big"])
	require.Equal(t, projectionrun.DrainYieldExhausted, reasons["source-small"])
}

// Disabled extra draining (negative budget) yields budget_exceeded after the
// one mandatory attempt by design; the coordinator must flag it so the
// observer does not report starvation.
func TestChaos7179_NegativeBudgetFlagsExtraDrainDisabledOnTheOutcome(t *testing.T) {
	t.Parallel()
	source := &fakeSource{name: "source-one", pages: 1}
	observer := &recordingObserver{}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-one", Source: source}},
		Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		Observer: observer, Logger: discardLogger(), DrainBatchBudget: -1,
	})
	require.NoError(t, err)
	coordinator.Tick(context.Background())
	drains := observer.snapshot()
	require.Len(t, drains, 1)
	require.True(t, drains[0].ExtraDrainDisabled)
}
