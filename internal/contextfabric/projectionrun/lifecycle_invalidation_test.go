package projectionrun_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

// CHAOS-4208 regression coverage: Coordinator must notify a wired
// EpochResolverInvalidator the instant a lifecycle transition commits
// (beginLifecycleBuild success, and a successful Flip), and the automatic
// CHAOS-3882 divergence recovery path must never claim it "opened a
// build-aside epoch" when BeginBuild was refused for a reason that has
// nothing to do with a build already running (e.g. the organization is
// legitimately in its post-flip grace window).

type fakeEpochResolverInvalidator struct {
	mu          sync.Mutex
	invalidated []string
}

func (f *fakeEpochResolverInvalidator) Invalidate(orgID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, orgID)
}

func (f *fakeEpochResolverInvalidator) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.invalidated...)
}

// fakeInvalidationTelemetry records only RecordEpochResolverInvalidation
// calls -- the cf_epoch_resolver_invalidation signal CHAOS-4208 adds
// alongside the Invalidate wiring itself.
type fakeInvalidationTelemetry struct {
	contextfabric.NoopGraphLifecycleTelemetry
	mu          sync.Mutex
	transitions []contextfabric.LifecycleTransition
}

func (f *fakeInvalidationTelemetry) RecordEpochResolverInvalidation(_ context.Context, _ string, transition contextfabric.LifecycleTransition) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, transition)
}

func (f *fakeInvalidationTelemetry) recorded() []contextfabric.LifecycleTransition {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contextfabric.LifecycleTransition(nil), f.transitions...)
}

func TestCoordinator_InvalidatesEpochResolverOnBeginBuildAndFlip(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	invalidator := &fakeEpochResolverInvalidator{}
	telemetry := &fakeInvalidationTelemetry{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: sourceA}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		EpochResolverInvalidator: invalidator, LifecycleTelemetry: telemetry,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)

	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))
	require.Equal(t, []string{"org-1"}, invalidator.calls(), "a successful BeginBuild must invalidate the epoch resolver's cache immediately")
	require.Equal(t, []contextfabric.LifecycleTransition{contextfabric.LifecycleTransitionBeginBuild}, telemetry.recorded(), "the invalidation must be reported via cf_epoch_resolver_invalidation")

	// Re-running Rebuild while a build is already open (BeginBuild refused,
	// nothing NEW opened) must NOT invalidate again.
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))
	require.Equal(t, []string{"org-1"}, invalidator.calls(), "a refused BeginBuild (already building) must not invalidate again")

	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 10)
	require.Equal(t, []string{"org-1", "org-1"}, invalidator.calls(), "a successful Flip must also invalidate the epoch resolver's cache immediately")
	require.Equal(t, []contextfabric.LifecycleTransition{contextfabric.LifecycleTransitionBeginBuild, contextfabric.LifecycleTransitionFlip}, telemetry.recorded())
}

// TestCoordinator_InvalidatesEpochResolverOnRollback pins CHAOS-4208
// round-2 finding 2: Rollback changes ActiveEpoch exactly like Flip does,
// so it must invalidate the epoch resolver's cache exactly like Flip does
// -- the resolver's own Invalidate doc comment already promised "flip/
// rollback", but only Flip and beginLifecycleBuild were actually wired in
// round 1.
func TestCoordinator_InvalidatesEpochResolverOnRollback(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	invalidator := &fakeEpochResolverInvalidator{}
	telemetry := &fakeInvalidationTelemetry{}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: sourceA}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		EpochResolverInvalidator: invalidator, LifecycleTelemetry: telemetry,
		GraceWindow: time.Hour, Concurrency: 4, Logger: discardLogger(),
	})
	require.NoError(t, err)

	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))
	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 10)
	callsBeforeRollback := len(invalidator.calls())

	require.NoError(t, coordinator.Rollback(ctx, "org-1"))

	calls := invalidator.calls()
	require.Len(t, calls, callsBeforeRollback+1, "Rollback must invalidate the epoch resolver's cache immediately, exactly like Flip does")
	require.Equal(t, "org-1", calls[len(calls)-1])
	recorded := telemetry.recorded()
	require.Equal(t, contextfabric.LifecycleTransitionRollback, recorded[len(recorded)-1])
}

func TestCoordinator_GraceRefusalDoesNotClaimItOpenedAnEpoch(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	backend := newFakeBackend()
	sourceA := &lifecycleFakeSource{name: "source-a", pages: 1}
	invalidator := &fakeEpochResolverInvalidator{}

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, sanitizationTestHandlerOptions()))

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: sourceA}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: lifecycle, EpochCheckpoints: checkpoints.ForEpoch,
		EpochResolverInvalidator: invalidator,
		GraceWindow:              time.Hour, MaxBackoff: time.Millisecond, Concurrency: 4, Logger: logger,
	})
	require.NoError(t, err)

	// Drive the organization through a real build/flip -- it now sits in
	// LifecycleStatusGrace at ActiveEpoch 1, a legitimate steady state.
	require.NoError(t, coordinator.Rebuild(ctx, "org-1"))
	tickUntilStatus(t, ctx, coordinator, lifecycle, "org-1", contextfabric.LifecycleStatusGrace, 10)
	buffer.Reset()
	preTickInvalidations := len(invalidator.calls())

	// Force the SAME false-positive divergence CHAOS-4208 reproduces: the
	// backend's watermark read fails for the organization's now-active
	// epoch, even though the durable Postgres checkpoint (real, from the
	// build that just completed) claims a successful apply.
	backend.setWatermarkErr("org-1", "source-a", contextfabric.ErrProjectionWatermarkNotFound)

	coordinator.Tick(ctx)

	logs := buffer.String()
	require.Contains(t, logs, "checkpoint-store divergence detected", "the divergence check itself must still fire")
	require.NotContains(t, logs, "opened a build-aside epoch",
		"BeginBuild was refused because the organization is in grace, not because a build is open -- nothing was opened, so this WARN must not fire")
	require.Contains(t, logs, `"observed_status":"grace"`, "the refusal reason must be observable")

	row, found, err := lifecycle.Get(ctx, "org-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.LifecycleStatusGrace, row.Status, "the refused recovery attempt must not have mutated the lifecycle row")

	require.Len(t, invalidator.calls(), preTickInvalidations, "a refused, no-op recovery attempt must not invalidate the epoch resolver's cache")

	// Confirm the SAME divergence keeps getting detected on a later tick
	// (bounded by the due()/backoff gate, not a single-shot check) --
	// pinning that this refusal path is a legitimate, repeatable no-op
	// rather than a one-time fluke.
	time.Sleep(2 * time.Millisecond)
	buffer.Reset()
	coordinator.Tick(ctx)
	require.NotContains(t, buffer.String(), "opened a build-aside epoch")
}
