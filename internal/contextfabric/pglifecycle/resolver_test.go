package pglifecycle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/stretchr/testify/require"
)

// CHAOS-4208: CachedResolver's own bounded-lease caching and Invalidate had
// no dedicated test coverage before this file. A fake lifecycleReader is
// the right tool here (unlike CAS-transition tests elsewhere in this
// package, which deliberately use a real Postgres store to avoid
// re-implementing CAS semantics -- see lifecycle_integration_test.go's own
// doc comment): caching is pure Go logic over whatever the reader returns,
// so a fake that lets a test control exactly what changes underneath the
// cache, and when, is what actually isolates the behavior under test.

type fakeLifecycleReader struct {
	mu    sync.Mutex
	row   contextfabric.OrgGraphLifecycle
	found bool
	reads int
}

func (f *fakeLifecycleReader) Get(context.Context, string) (contextfabric.OrgGraphLifecycle, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.row, f.found, nil
}

func (f *fakeLifecycleReader) set(row contextfabric.OrgGraphLifecycle, found bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.row, f.found = row, found
}

func (f *fakeLifecycleReader) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// TestCachedResolver_InvalidateForcesImmediateRefresh pins CHAOS-4208's
// core fix: without a call to Invalidate, CachedResolver keeps serving a
// pre-transition ActiveEpoch for the rest of its lease even after the
// underlying store has moved on -- exactly the read-side staleness that
// produced the false CHAOS-3882 divergence storm after every epoch flip.
// Invalidate must force the very next resolve to see the new state
// immediately, without waiting out the lease.
func TestCachedResolver_InvalidateForcesImmediateRefresh(t *testing.T) {
	reader := &fakeLifecycleReader{}
	reader.set(contextfabric.OrgGraphLifecycle{}, false) // no row yet: implicit serving at epoch 0
	inner, err := pglifecycle.NewResolver(reader)
	require.NoError(t, err)
	now := time.Now()
	resolver, err := pglifecycle.NewCachedResolver(inner, time.Minute, pglifecycle.CachedResolverOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	ctx := context.Background()

	// The priming read -- mirrors checkpointStoreDiverged's own
	// ProjectionWatermark call, taken just before a flip commits. A cache
	// miss triggers one refresh, which reads the store twice (once each
	// for ResolveActiveEpoch and ResolveBuildEpoch -- CachedResolver.refresh
	// populates both fields of one cache entry together).
	epoch, err := resolver.ResolveActiveEpoch(ctx, "org-1")
	require.NoError(t, err)
	require.Equal(t, int64(0), epoch)
	require.Equal(t, 2, reader.readCount())

	// The underlying store advances (a Flip just committed) while the
	// cache entry is still well within its lease.
	reader.set(contextfabric.OrgGraphLifecycle{ActiveEpoch: 1, Status: contextfabric.LifecycleStatusGrace}, true)

	// Sanity: without Invalidate, the cache keeps serving the stale value
	// -- this IS the CHAOS-4208 bug shape when nothing calls Invalidate.
	stale, err := resolver.ResolveActiveEpoch(ctx, "org-1")
	require.NoError(t, err)
	require.Equal(t, int64(0), stale, "sanity: still cached, not yet invalidated")
	require.Equal(t, 2, reader.readCount(), "sanity: a cached read must not hit the store again")

	// The fix: Invalidate immediately after the transition commits.
	resolver.Invalidate("org-1")
	fresh, err := resolver.ResolveActiveEpoch(ctx, "org-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), fresh, "the active epoch must be fresh immediately after Invalidate, not after the lease expires")
	require.Equal(t, 4, reader.readCount())
}

// TestCachedResolver_InvalidateClosesThePrimeThenBeginBuildRace reproduces
// the exact CHAOS-4208 race at the resolver level: checkpointStoreDiverged
// primes the cache (no build open yet) an instant before BeginBuild opens
// one. Without a call to Invalidate right after BeginBuild succeeds, every
// write for the rest of the lease would resolve the stale pre-build state
// via resolveWriteKey -> ResolveBuildEpoch (falkorgraph/lifecycle.go),
// landing in the WRONG FalkorDB graph key instead of the newly opened
// target epoch's.
func TestCachedResolver_InvalidateClosesThePrimeThenBeginBuildRace(t *testing.T) {
	reader := &fakeLifecycleReader{}
	reader.set(contextfabric.OrgGraphLifecycle{}, false) // no row yet: no build open
	inner, err := pglifecycle.NewResolver(reader)
	require.NoError(t, err)
	now := time.Now()
	resolver, err := pglifecycle.NewCachedResolver(inner, time.Minute, pglifecycle.CachedResolverOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	ctx := context.Background()

	// The divergence check's priming read, taken BEFORE BeginBuild commits.
	_, ok, err := resolver.ResolveBuildEpoch(ctx, "org-1")
	require.NoError(t, err)
	require.False(t, ok, "sanity: no build open yet at priming time")

	// BeginBuild commits in Postgres: target_epoch=1, status=building.
	target := int64(1)
	reader.set(contextfabric.OrgGraphLifecycle{Status: contextfabric.LifecycleStatusBuilding, TargetEpoch: &target}, true)

	// Sanity: without Invalidate, a write attempted right now would still
	// resolve the stale pre-build state and misroute to the active epoch's
	// key instead of the target epoch's -- proving the race is real.
	_, staleOK, err := resolver.ResolveBuildEpoch(ctx, "org-1")
	require.NoError(t, err)
	require.False(t, staleOK, "sanity: still serving the pre-BeginBuild cache entry")

	// The fix: beginLifecycleBuild (projectionrun/coordinator.go) calls
	// Invalidate right after BeginBuild succeeds -- the next write resolves
	// the real target epoch immediately, landing in the correct graph key.
	resolver.Invalidate("org-1")
	epoch, ok, err := resolver.ResolveBuildEpoch(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, target, epoch, "a write immediately after BeginBuild must resolve the NEW build epoch, not the stale pre-build state")
}
