package projectionrun_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestCHAOS3882_DivergenceTriggersAutomaticRecoveryThenReplayResumes is THE
// incident this ticket closes: a FalkorDB container restart lost the
// projected graph -- and the projection-time watermark sentinel with it --
// while the Postgres checkpoint (a wholly separate durable store) stayed
// advanced, so nothing ever replayed and ACR silently served resolution
// against an empty graph.
//
// This drives that exact sequence against the fakes: tick 1 projects
// normally (a real, non-empty checkpoint AND a durable backend watermark,
// mirroring the real falkorgraph adapter's writeWatermark call inside
// ApplyProjectionBatch). Then the backend "restarts" -- its watermark for
// this (org, source) starts reporting contextfabric.ErrProjectionWatermarkNotFound,
// exactly as the real adapter does after a purge or a lost graph -- while the
// Postgres checkpoint is untouched. Tick 2 must detect the divergence,
// refuse ordinary projection that tick, and drive a full recovery (purge +
// checkpoint reset) instead. Tick 3 proves replay actually resumes
// afterward.
func TestCHAOS3882_DivergenceTriggersAutomaticRecoveryThenReplayResumes(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	rebuildMarkers := newFakeRebuildMarker()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: rebuildMarkers, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	// Tick 1: healthy initial projection. Proves (a) the sentinel is written
	// at projection time -- the fake's ApplyProjectionBatch mirrors the real
	// adapter's writeWatermark call, so a real batch apply durably populates
	// BOTH the Postgres checkpoint's BackendWatermark AND the backend's own
	// watermark.
	coordinator.Tick(ctx)
	before, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if before.Cursor == "" || before.BackendWatermark == "" {
		t.Fatalf("expected a real checkpoint with a durable backend watermark after tick 1, got %+v", before)
	}
	if backend.purged["org-1"] {
		t.Fatal("no divergence yet; the backend must not have been purged after a healthy tick")
	}
	callsAfterTick1 := source.calls.Load()

	// Simulate the FalkorDB container restart losing the graph: the durable
	// checkpoint is untouched, but the backend's own watermark lookup now
	// confirms absence -- proves (b) divergence detection.
	backend.setWatermarkErr("org-1", "source-a", contextfabric.ErrProjectionWatermarkNotFound)

	// Tick 2: must detect the divergence, skip ordinary projection this
	// tick (source.calls stays put), and drive recovery through the SAME
	// purge-and-reset sequence an explicit rebuild uses.
	coordinator.Tick(ctx)
	if source.calls.Load() != callsAfterTick1 {
		t.Fatalf("expected ordinary projection to be skipped on the divergence-detecting tick: calls after tick1=%d, after tick2=%d", callsAfterTick1, source.calls.Load())
	}
	if !backend.purged["org-1"] {
		t.Fatal("expected divergence recovery to purge the organization's backend state")
	}
	if rebuildMarkers.beginCalls != 1 || rebuildMarkers.completeCalls != 1 {
		t.Fatalf("expected exactly one begin+complete rebuild marker pair, got begin=%d complete=%d", rebuildMarkers.beginCalls, rebuildMarkers.completeCalls)
	}
	afterRecovery, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if afterRecovery.Cursor != "" || afterRecovery.SourceVersion != "" || afterRecovery.BackendWatermark != "" {
		t.Fatalf("expected divergence recovery to reset the checkpoint to its zero value, got %+v", afterRecovery)
	}
	logged := buffer.String()
	if !strings.Contains(logged, "checkpoint-store divergence detected") {
		t.Fatalf("expected a loud divergence-detected log line:\n%s", logged)
	}
	if !strings.Contains(logged, "recovery completed") {
		t.Fatalf("expected a recovery-completed log line:\n%s", logged)
	}
	if !strings.Contains(logged, `"orgs_divergence_recovered":1`) {
		t.Fatalf("expected the tick summary to count 1 divergence recovery:\n%s", logged)
	}

	// Tick 3: proves (a real, not just structural) replay resumes -- the
	// source is asked for a batch again, from the reset (empty) cursor,
	// exactly as it would be for a brand-new organization.
	coordinator.Tick(ctx)
	if source.calls.Load() <= callsAfterTick1 {
		t.Fatal("expected replay to resume and call the source again after recovery")
	}
	afterReplay, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if afterReplay.Cursor == "" || afterReplay.BackendWatermark == "" {
		t.Fatalf("expected the checkpoint to advance again with a fresh backend watermark after replay, got %+v", afterReplay)
	}
}

// TestCHAOS3882_FailedRecoveryBacksOffInsteadOfRetryingEveryTick proves (c):
// a divergence recovery attempt that itself fails (BeginRebuild erroring,
// simulating the rebuild marker store being unavailable) must not be
// retried on every immediately-following poll tick -- it backs off, reusing
// the coordinator's existing pair-scheduling gate, exactly like an ordinary
// failing (org, source) tick already does
// (TestCoordinatorBacksOffAFailingPairThenRetriesLater).
func TestCHAOS3882_FailedRecoveryBacksOffInsteadOfRetryingEveryTick(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	// Tick 1: healthy projection, establishing a real BackendWatermark.
	coordinator.Tick(ctx)

	// Simulate the restart, AND make recovery itself fail: BeginRebuild
	// errors before the marker is ever durably set, so the crash-resume path
	// (IsRebuildInProgress) never takes over on a later tick -- divergence
	// detection alone must be the thing that backs off.
	backend.setWatermarkErr("org-1", "source-a", contextfabric.ErrProjectionWatermarkNotFound)
	failingMarkers := newFakeRebuildMarker()
	failingMarkers.beginErr = errors.New("rebuild marker store unavailable")
	coordinator2, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: failingMarkers, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	// Tick 2: divergence detected, recovery attempted, fails.
	coordinator2.Tick(ctx)
	if failingMarkers.beginCalls != 1 {
		t.Fatalf("expected exactly one recovery attempt on the divergence-detecting tick, got %d", failingMarkers.beginCalls)
	}
	afterFailedAttempt, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-1", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if afterFailedAttempt.BackendWatermark == "" {
		t.Fatal("a failed recovery attempt must never reset the checkpoint -- that would discard the durable baseline before the graph is actually fixed")
	}

	// Tick 3: immediately following. Without backoff this would retry
	// BeginRebuild again; the backoff gate must suppress it (same 5s
	// baseBackoff window TestCoordinatorBacksOffAFailingPairThenRetriesLater
	// relies on).
	coordinator2.Tick(ctx)
	if failingMarkers.beginCalls != 1 {
		t.Fatalf("expected the immediately-following tick to be backed off, not retried: begin calls = %d", failingMarkers.beginCalls)
	}
}

// TestCHAOS3882_HealthyOrgNeverTriggersRecovery is the (d) regression case:
// an organization whose backend watermark always agrees with its durable
// checkpoint must never be purged or rebuilt, across repeated ticks, and
// must keep advancing its checkpoint exactly as it did before this ticket.
func TestCHAOS3882_HealthyOrgNeverTriggersRecovery(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	rebuildMarkers := newFakeRebuildMarker()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-healthy"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: rebuildMarkers, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		coordinator.Tick(ctx)
	}

	if backend.purged["org-healthy"] {
		t.Fatal("a healthy organization must never be purged")
	}
	if rebuildMarkers.beginCalls != 0 {
		t.Fatalf("a healthy organization must never trigger a rebuild, got %d begin calls", rebuildMarkers.beginCalls)
	}
	if source.calls.Load() != 3 {
		t.Fatalf("expected ordinary projection to run every tick for a healthy org, got %d calls", source.calls.Load())
	}
	checkpoint, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-healthy", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.Cursor == "" || checkpoint.BackendWatermark == "" {
		t.Fatalf("expected the checkpoint to have advanced normally, got %+v", checkpoint)
	}
}

// TestCHAOS3882_NeverProjectedOrgIsNotDivergence proves a first-ever
// organization (no durable checkpoint yet -- BackendWatermark is the zero
// value) is never mistaken for divergence, even when the backend watermark
// lookup also confirms absence. There is nothing for the graph to have
// "lost" when the checkpoint never claimed a successful apply in the first
// place; this is the same organization CHAOS-3887's own
// TestCHAOS3887_NeverProjectedOrgReportsUnknownNotStale fixture covers for
// the freshness signal, exercised here for the new recovery path instead.
func TestCHAOS3882_NeverProjectedOrgIsNotDivergence(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend()
	backend.setWatermarkErr("org-new", "source-a", contextfabric.ErrProjectionWatermarkNotFound)
	checkpoints := newFakeCheckpointStore()
	rebuildMarkers := newFakeRebuildMarker()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-new"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: rebuildMarkers, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	coordinator.Tick(ctx)

	if backend.purged["org-new"] {
		t.Fatal("a never-projected organization must never be purged")
	}
	if rebuildMarkers.beginCalls != 0 {
		t.Fatalf("a never-projected organization must never trigger a rebuild, got %d begin calls", rebuildMarkers.beginCalls)
	}
	if source.calls.Load() != 1 {
		t.Fatalf("expected ordinary (first-ever) projection to proceed normally, got %d calls", source.calls.Load())
	}
}

// TestCHAOS3882_TransientWatermarkErrorNeverTriggersRecovery proves the
// safety-critical negative case: a dependency error that is NOT the
// backend's confirmed-absent sentinel (a timeout, a generic dependency
// failure -- exactly what a flaky network blip looks like) must never be
// treated as divergence, even when the durable checkpoint has a real
// BackendWatermark. Reacting to any error the same way would mean a
// transient FalkorDB hiccup destructively purges organization data that was
// never actually lost.
func TestCHAOS3882_TransientWatermarkErrorNeverTriggersRecovery(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	rebuildMarkers := newFakeRebuildMarker()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: rebuildMarkers, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	// Establish a real, durable BackendWatermark first.
	coordinator.Tick(ctx)

	// A generic, non-sentinel dependency error -- NOT
	// contextfabric.ErrProjectionWatermarkNotFound -- must be read as
	// "unknown", never as confirmed divergence.
	backend.setWatermarkErr("org-1", "source-a", errors.New("dependency timeout"))

	coordinator.Tick(ctx)
	if backend.purged["org-1"] {
		t.Fatal("a transient, non-sentinel watermark error must never trigger a destructive purge")
	}
	if rebuildMarkers.beginCalls != 0 {
		t.Fatalf("a transient, non-sentinel watermark error must never trigger a rebuild, got %d begin calls", rebuildMarkers.beginCalls)
	}
}

// TestCHAOS3882_DivergenceLogsCarryNoRawOrgID is the corpus-safety
// assertion for the new recovery path: every log line it emits must carry
// only the one-way org_id_hash, never the raw organization identifier.
func TestCHAOS3882_DivergenceLogsCarryNoRawOrgID(t *testing.T) {
	t.Parallel()

	const rawOrgID = "org-super-secret-tenant-99"

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{rawOrgID}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	coordinator.Tick(ctx)
	backend.setWatermarkErr(rawOrgID, "source-a", contextfabric.ErrProjectionWatermarkNotFound)
	coordinator.Tick(ctx)

	logged := buffer.String()
	if !strings.Contains(logged, "checkpoint-store divergence detected") {
		t.Fatalf("expected this tick to detect divergence (sanity check on the fixture):\n%s", logged)
	}
	// Scoped to the NEW CHAOS-3882 divergence-recovery lines specifically
	// (checkpointStoreDiverged/recoverFromDivergence's own log calls) --
	// this package's OTHER, pre-existing operational logs (runPair's
	// "projection batch applied", performRebuild's "projection organization
	// rebuilt") deliberately carry the raw org_id for operator usability and
	// are out of this ticket's scope; only the freshness/divergence SIGNALS
	// are held to the one-way-hash-only discipline, matching
	// TestCHAOS3887_FreshnessSignalCarriesNoRawOrgID's own equally-scoped
	// assertion.
	var divergenceLines []string
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if strings.Contains(line, "CHAOS-3882") || strings.Contains(line, "projection-liveness") {
			divergenceLines = append(divergenceLines, line)
		}
	}
	if len(divergenceLines) == 0 {
		t.Fatal("expected at least one CHAOS-3882 divergence-recovery log line; this test would pass vacuously")
	}
	for _, line := range divergenceLines {
		if !strings.Contains(line, "org_id_hash") {
			t.Fatalf("expected every divergence-recovery log line to carry org_id_hash:\n%s", line)
		}
		if strings.Contains(line, rawOrgID) {
			t.Fatalf("the raw organization identifier %q leaked into a divergence-recovery log line:\n%s", rawOrgID, line)
		}
	}
}

// TestCHAOS3882_LivenessCheckReportsDivergenceForReadinessProbe proves the
// readiness-path leg: a healthy organization's LivenessCheck must report
// nil, and a diverged one must report a content-safe (hash-only) error --
// this is what cmd/acr-projector wires into /readyz so an operator sees
// degraded immediately, without waiting on the next tick's logs.
func TestCHAOS3882_LivenessCheckReportsDivergenceForReadinessProbe(t *testing.T) {
	t.Parallel()

	const rawOrgID = "org-liveness-probe-secret"

	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{rawOrgID}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	// Never projected yet: healthy by construction (nothing to have lost).
	if err := coordinator.LivenessCheck(ctx); err != nil {
		t.Fatalf("LivenessCheck() on a never-projected org = %v, want nil", err)
	}

	// A real, healthy projection: still must report nil.
	coordinator.Tick(ctx)
	if err := coordinator.LivenessCheck(ctx); err != nil {
		t.Fatalf("LivenessCheck() on a healthy, freshly-projected org = %v, want nil", err)
	}

	// Simulate the backend losing its state.
	backend.setWatermarkErr(rawOrgID, "source-a", contextfabric.ErrProjectionWatermarkNotFound)
	err = coordinator.LivenessCheck(ctx)
	if err == nil {
		t.Fatal("LivenessCheck() on a diverged org = nil, want a non-nil error")
	}
	if strings.Contains(err.Error(), rawOrgID) {
		t.Fatalf("LivenessCheck error leaked the raw organization identifier: %v", err)
	}
	if backend.purged[rawOrgID] {
		t.Fatal("LivenessCheck must be read-only and must never itself purge an organization")
	}
}
