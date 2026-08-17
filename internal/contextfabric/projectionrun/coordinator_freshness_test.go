package projectionrun_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestCHAOS3887_DormantOrgFreshnessSignalFires is THE gap this ticket
// closes: ProjectionWorker.RunOnce's SourceVersion guard (projector.go)
// only ever compares checkpoint vs batch SourceVersion INSIDE the
// available==true branch, so a dormant organization -- no new rows since
// its last checkpoint, NextProjectionBatch returns available=false with no
// error -- gets no freshness signal at all from that guard, and its
// already-projected nodes stay computed under stale producer logic
// indefinitely with no operational signal distinguishing it from a fresh
// organization.
//
// This drives a dormant organization (fakeSource.dormant=true) whose
// durable ProjectionWatermark (the CHAOS-3887 baseline, reused from the
// falkorgraph adapter's /readyz-only read) was written under an OLDER
// producer SourceVersion than the source's current one, and asserts the
// coordinator emits a freshness event marking it stale even though RunOnce
// never built (or even looked at) a batch this tick.
func TestCHAOS3887_DormantOrgFreshnessSignalFires(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	backend := newFakeBackend()
	projectedAt := time.Now().UTC().Add(-2 * time.Hour)
	backend.setWatermark("org-dormant", "source-a", contextfabric.ProjectionWatermark{
		OrgID: "org-dormant", Source: "source-a", SourceVersion: "devhealthsource.clickhouse.v4", ProjectedAt: projectedAt,
	})
	source := &fakeSource{name: "source-a", dormant: true, currentSourceVersion: "devhealthsource.clickhouse.v5"}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-dormant"},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	if source.calls.Load() != 1 {
		t.Fatalf("expected RunOnce to have run (and found the org dormant) exactly once, got %d calls", source.calls.Load())
	}
	if backend.appliedCount() != 0 {
		t.Fatalf("a dormant organization must never apply a batch; got %d applied", backend.appliedCount())
	}

	logged := buffer.String()
	if !strings.Contains(logged, `"stale":true`) {
		t.Fatalf("expected the freshness signal to report stale=true for a dormant org behind an old checkpoint_source_version:\n%s", logged)
	}
	if !strings.Contains(logged, `"checkpoint_source_version":"devhealthsource.clickhouse.v4"`) {
		t.Fatalf("expected checkpoint_source_version to reflect the durable watermark:\n%s", logged)
	}
	if !strings.Contains(logged, `"current_source_version":"devhealthsource.clickhouse.v5"`) {
		t.Fatalf("expected current_source_version to reflect the source's code-current version:\n%s", logged)
	}
	if !strings.Contains(logged, `"projected_at_age_seconds"`) {
		t.Fatalf("expected a projected_at_age_seconds field:\n%s", logged)
	}
	// The fleet aggregate (H12): one dormant, stale organization must be
	// counted as pending rebuild in the per-tick summary line.
	if !strings.Contains(logged, `"pending_rebuild_orgs_total":1`) {
		t.Fatalf("expected the tick's fleet aggregate to count 1 org pending rebuild:\n%s", logged)
	}
	if !strings.Contains(logged, `"orgs_rebuild_required":1`) {
		t.Fatalf("expected orgs_rebuild_required=1 in the tick summary:\n%s", logged)
	}
}

// TestCHAOS3887_FreshDormantOrgIsNotFalselyStale is the negative case: a
// dormant organization whose durable watermark already matches the
// source's current version must never be reported stale, and must count
// toward orgs_ok, not orgs_rebuild_required.
func TestCHAOS3887_FreshDormantOrgIsNotFalselyStale(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	backend := newFakeBackend()
	backend.setWatermark("org-dormant-fresh", "source-a", contextfabric.ProjectionWatermark{
		OrgID: "org-dormant-fresh", Source: "source-a", SourceVersion: "test.v1", ProjectedAt: time.Now().UTC(),
	})
	source := &fakeSource{name: "source-a", dormant: true, currentSourceVersion: "test.v1"}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-dormant-fresh"},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	logged := buffer.String()
	if strings.Contains(logged, `"stale":true`) {
		t.Fatalf("a dormant org whose watermark already matches the current version must not be reported stale:\n%s", logged)
	}
	if !strings.Contains(logged, `"orgs_ok":1`) {
		t.Fatalf("expected orgs_ok=1 in the tick summary:\n%s", logged)
	}
	if !strings.Contains(logged, `"pending_rebuild_orgs_total":0`) {
		t.Fatalf("expected pending_rebuild_orgs_total=0:\n%s", logged)
	}
}

// TestCHAOS3887_NeverProjectedOrgReportsUnknownNotStale asserts a first-ever
// tick (no durable watermark yet -- the real falkorgraph adapter returns
// ErrNotFound here) is reported as an unknown freshness state, never as a
// false stale=true.
func TestCHAOS3887_NeverProjectedOrgReportsUnknownNotStale(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	backend := newFakeBackend()
	backend.setWatermarkErr("org-new", "source-a", errors.New("watermark not found"))
	source := &fakeSource{name: "source-a", dormant: true, currentSourceVersion: "test.v1"}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-new"},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	logged := buffer.String()
	if strings.Contains(logged, `"stale":true`) {
		t.Fatalf("a never-projected org must never report stale=true:\n%s", logged)
	}
	if !strings.Contains(logged, `"orgs_ok":1`) {
		t.Fatalf("expected the unknown case to still be classified as ok for the fleet aggregate:\n%s", logged)
	}
}

// TestCHAOS3887_SourceWithoutVersionCapabilityReportsUnknown asserts a
// source that has not implemented the optional
// contextfabric.ProjectionSourceVersion capability degrades to "unknown",
// not to a false stale=true, even when a durable watermark exists.
func TestCHAOS3887_SourceWithoutVersionCapabilityReportsUnknown(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	backend := newFakeBackend()
	backend.setWatermark("org-1", "source-a", contextfabric.ProjectionWatermark{
		OrgID: "org-1", Source: "source-a", SourceVersion: "test.v1", ProjectedAt: time.Now().UTC(),
	})
	source := fakeSourceNoVersionCapability{inner: &fakeSource{name: "source-a"}}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-1"},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	logged := buffer.String()
	if strings.Contains(logged, `"stale":true`) {
		t.Fatalf("a source without the version capability must never report stale=true:\n%s", logged)
	}
}

// TestCHAOS3887_FreshnessSignalCarriesNoRawOrgID is the corpus-safety
// assertion: the freshness telemetry must carry only the one-way
// org_id_hash, never the raw organization identifier, and never any
// projected row content.
func TestCHAOS3887_FreshnessSignalCarriesNoRawOrgID(t *testing.T) {
	t.Parallel()

	const rawOrgID = "org-super-secret-tenant-42"

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	backend := newFakeBackend()
	backend.setWatermark(rawOrgID, "source-a", contextfabric.ProjectionWatermark{
		OrgID: rawOrgID, Source: "source-a", SourceVersion: "devhealthsource.clickhouse.v4", ProjectedAt: time.Now().UTC(),
	})
	source := &fakeSource{name: "source-a", dormant: true, currentSourceVersion: "devhealthsource.clickhouse.v5"}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{rawOrgID},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	logged := buffer.String()
	if strings.TrimSpace(logged) == "" {
		t.Fatal("expected freshness telemetry output; this test would pass vacuously")
	}
	if !strings.Contains(logged, `"stale":true`) {
		t.Fatalf("expected this dormant org to be reported stale (sanity check on the fixture):\n%s", logged)
	}
	if !strings.Contains(logged, "org_id_hash") {
		t.Fatalf("expected the freshness signal to carry org_id_hash:\n%s", logged)
	}
	if strings.Contains(logged, rawOrgID) {
		t.Fatalf("the raw organization identifier %q leaked into freshness telemetry:\n%s", rawOrgID, logged)
	}
}
