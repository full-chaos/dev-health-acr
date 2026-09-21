package projectionrun_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

// perOrgLifecycleSource is lifecycleFakeSource with a PER-ORGANIZATION page
// budget. lifecycleFakeSource keeps one shared counter, which is fine when
// every organization is configured up front and consumes its pages in the
// same tick -- but under discovery, an organization that appears on tick 2
// would find the shared budget already spent by the organization that
// ticked on tick 1 and report available=false, which looks exactly like the
// defect this test exists to rule out. Production's real sources are
// per-organization (they page from each organization's own checkpoint), so
// the shared counter is the fake's artifact, not the system's behavior.
type perOrgLifecycleSource struct {
	mu    sync.Mutex
	name  string
	pages int
	calls map[string]int
}

func newPerOrgLifecycleSource(name string, pages int) *perOrgLifecycleSource {
	return &perOrgLifecycleSource{name: name, pages: pages, calls: map[string]int{}}
}

func (f *perOrgLifecycleSource) NextProjectionBatch(_ context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[checkpoint.OrgID]++
	calls := f.calls[checkpoint.OrgID]
	if calls > f.pages {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	batch := validBatch(checkpoint.OrgID, f.name, checkpoint.Cursor, checkpoint.Cursor+"n")
	batch.CompleteEnumeration = calls == f.pages
	return batch, true, nil
}

// TestOrgDiscovery_ANewlyDiscoveredOrganizationBuildsUnderTheLifecycleMachine
// is CHAOS-6182's lifecycle leg, against the REAL pglifecycle.Store and
// pgprojection.CheckpointStore (this file's sibling,
// lifecycle_integration_test.go, states why a fake lifecycle store would be
// false confidence).
//
// The question it answers is the one the design could not answer by
// argument: an organization that has NEVER been seen has no lifecycle row
// at all, so when it first appears mid-run, does it build from epoch 0 the
// way an organization present at startup does -- or does the epoch
// machinery treat an absent row as an error and leave it unprojected?
func TestOrgDiscovery_ANewlyDiscoveredOrganizationBuildsUnderTheLifecycleMachine(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()

	orgs := &fakeOrgSource{results: []fakeOrgResult{
		{orgs: []string{"org-initial"}},
		{orgs: []string{"org-initial", "org-late"}},
	}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: newPerOrgLifecycleSource("source-a", 1)}},
		Backend:   backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)

	// Tick 1: only org-initial is discoverable, and it projects at epoch 0
	// (no build has ever been opened, so ActiveEpoch stays 0).
	coordinator.Tick(ctx)
	initialCursor, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-initial", 0, "source-a")
	require.NoError(t, err)
	require.NotEmpty(t, initialCursor.Cursor, "an organization present from the start projects at epoch 0")
	require.Empty(t, appliedOrgs(backend)["org-late"], "org-late was not discoverable on tick 1")

	// Tick 2: org-late appears. It has no lifecycle row and no checkpoint,
	// so it must take exactly the same epoch-0 path.
	coordinator.Tick(ctx)
	lateCursor, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, "org-late", 0, "source-a")
	require.NoError(t, err)
	require.NotEmpty(t, lateCursor.Cursor, "a newly discovered organization must project at epoch 0 like any initial one")
	require.Positive(t, appliedOrgs(backend)["org-late"], "a newly discovered organization's entities must reach the backend")

	// Neither organization has opened a build, so neither has a lifecycle
	// row: discovery must not implicitly begin one.
	for _, orgID := range []string{"org-initial", "org-late"} {
		_, found, err := lifecycle.Get(ctx, orgID)
		require.NoError(t, err)
		require.False(t, found, "organization %s must not have a lifecycle row until a rebuild opens one", orgID)
	}

	// And an explicit rebuild of the LATE organization -- admitted only
	// because discovery widened the effective set -- drives the ordinary
	// build-aside-and-swap path to a flip, exactly as it would for an
	// organization that had been configured from the start.
	require.NoError(t, coordinator.Rebuild(ctx, "org-late"))
	row, found, err := lifecycle.Get(ctx, "org-late")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status)
	require.NotNil(t, row.TargetEpoch)
	require.Equal(t, int64(1), *row.TargetEpoch, "a first build for a newly discovered organization targets epoch 1 from epoch 0")

	flipped := tickUntilStatus(t, ctx, coordinator, lifecycle, "org-late", contextfabric.LifecycleStatusGrace, 10)
	require.Equal(t, int64(1), flipped.ActiveEpoch)
	require.NotNil(t, flipped.GraceEpoch)
	require.Equal(t, int64(0), *flipped.GraceEpoch)
	require.False(t, backend.purged["org-late"], "build-aside-and-swap never purges the serving graph")
}
