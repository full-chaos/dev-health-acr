package projectionrun_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

type refusalHarness struct {
	t           *testing.T
	buffer      bytes.Buffer
	clock       *fakeClock
	coordinator *projectionrun.Coordinator
}

func newRefusalHarness(t *testing.T, lifecycle bool, sources ...*fakeSource) *refusalHarness {
	t.Helper()
	h := &refusalHarness{t: t, clock: newFakeClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))}
	logger := slog.New(slog.NewJSONHandler(&h.buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	pairs := make([]projectionrun.SourcePair, 0, len(sources))
	for _, source := range sources {
		pairs = append(pairs, projectionrun.SourcePair{Name: source.name, Source: source})
	}
	checkpoints := newFakeCheckpointStore()
	cfg := projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        pairs,
		Backend:        newFakeBackend(),
		Checkpoints:    checkpoints,
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
		Now:            h.clock.Now,
	}
	if lifecycle {
		cfg.Lifecycle = &buildFailingLifecycleStore{epoch: 1}
		cfg.EpochCheckpoints = func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints }
		cfg.GraceWindow = time.Hour
	}
	coordinator, err := projectionrun.NewCoordinator(cfg)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	h.coordinator = coordinator
	return h
}

func (h *refusalHarness) tick() map[string]any {
	h.t.Helper()
	h.buffer.Reset()
	h.coordinator.Tick(context.Background())
	summary := freshnessSummary(h.t, &h.buffer)
	requireBucketIdentity(h.t, summary)
	return summary
}

func (h *refusalHarness) pastBackoff() { h.clock.Advance(time.Hour) }

func versionRefusedSource(name string) *fakeSource {
	return &fakeSource{name: name, err: fmt.Errorf("read: %w", contextfabric.ErrProjectionSourceVersionChanged)}
}

func failedSource(name string) *fakeSource {
	return &fakeSource{name: name, err: fmt.Errorf("read: %w", contextfabric.ErrUnavailable)}
}

func requireBuckets(t *testing.T, summary map[string]any, rebuild, failed, sourcesFailed, stale float64) {
	t.Helper()
	for key, want := range map[string]float64{
		"orgs_rebuild_required": rebuild,
		"orgs_source_failed":    failed,
		"sources_failed":        sourcesFailed,
		"orgs_stale":            stale,
	} {
		if got := summaryNumber(t, summary, key); got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

func TestAVersionRefusedSourceCountsAsRebuildRequiredNotSourceFailed(t *testing.T) {
	t.Parallel()
	source := versionRefusedSource("source-refused")
	h := newRefusalHarness(t, false, source)
	requireBuckets(t, h.tick(), 1, 0, 0, 1)
}

func TestAVersionRefusedSourceStaysRebuildRequiredWhileWithheldByBackoff(t *testing.T) {
	t.Parallel()
	source := versionRefusedSource("source-refused")
	h := newRefusalHarness(t, false, source)
	h.tick()
	summary := h.tick()
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("source calls = %d, want 1 -- the second tick must be withheld by backoff, or this test does not cover the withheld state", got)
	}
	requireBuckets(t, summary, 1, 0, 0, 1)
	if got := summaryNumber(t, summary, "sources_in_failure_backoff"); got != 0 {
		t.Errorf("sources_in_failure_backoff = %v, want 0 -- a refused version is not an outage in backoff", got)
	}
}

func TestAGenuineSourceFailureStillCountsAsSourceFailed(t *testing.T) {
	t.Parallel()
	h := newRefusalHarness(t, false, failedSource("source-failing"))
	requireBuckets(t, h.tick(), 0, 1, 1, 0)
}

func TestAGenuineFailureOutranksAVersionRefusalInTheBucketButBothAreCounted(t *testing.T) {
	t.Parallel()
	h := newRefusalHarness(t, false, versionRefusedSource("source-refused"), failedSource("source-failing"))
	requireBuckets(t, h.tick(), 0, 1, 1, 1)
}

func TestAnOutageAfterARefusalDoesNotEraseTheRebuildOwed(t *testing.T) {
	t.Parallel()
	source := versionRefusedSource("source-s")
	h := newRefusalHarness(t, false, source)
	h.tick()
	h.pastBackoff()
	source.err = fmt.Errorf("read: %w", contextfabric.ErrUnavailable)
	requireBuckets(t, h.tick(), 0, 1, 1, 1)
	if got := source.calls.Load(); got != 2 {
		t.Fatalf("source calls = %d, want 2 after the backoff elapsed", got)
	}
	summary := h.tick()
	if got := source.calls.Load(); got != 2 {
		t.Fatalf("source calls = %d, want 2 -- the outage must now be withheld", got)
	}
	requireBuckets(t, summary, 0, 1, 0, 1)
	if got := summaryNumber(t, summary, "sources_in_failure_backoff"); got != 1 {
		t.Errorf("sources_in_failure_backoff = %v, want 1 -- the outage is still in force", got)
	}
}

func TestASuccessfulAttemptClearsTheRebuildOwed(t *testing.T) {
	t.Parallel()
	source := versionRefusedSource("source-s")
	h := newRefusalHarness(t, false, source)
	h.tick()
	h.pastBackoff()
	source.err = nil
	source.pages = 1
	summary := h.tick()
	requireBuckets(t, summary, 0, 0, 0, 0)
	if got := summaryNumber(t, summary, "orgs_ok"); got != 1 {
		t.Errorf("orgs_ok = %v, want 1", got)
	}
}

func TestABuildPhaseVersionRefusalCountsAsRebuildRequiredNotSourceFailed(t *testing.T) {
	t.Parallel()
	source := versionRefusedSource("dev_health_teams_projects")
	h := newRefusalHarness(t, true, source)
	for tick := 1; tick <= 3; tick++ {
		summary := h.tick()
		if source.calls.Load() != 1 {
			t.Fatalf("tick %d: source calls = %d, want 1 -- ticks 2 and 3 must be withheld", tick, source.calls.Load())
		}
		requireBuckets(t, summary, 1, 0, 0, 1)
		if got := summaryNumber(t, summary, "build_sources_failed"); got != 0 {
			t.Errorf("tick %d: build_sources_failed = %v, want 0", tick, got)
		}
	}
}

func TestABuildPhaseOutageAfterARefusalKeepsBothSignals(t *testing.T) {
	t.Parallel()
	source := versionRefusedSource("dev_health_teams_projects")
	h := newRefusalHarness(t, true, source)
	h.tick()
	h.pastBackoff()
	source.err = fmt.Errorf("read: %w", contextfabric.ErrUnavailable)
	requireBuckets(t, h.tick(), 0, 1, 1, 1)
}

func TestACancelledTickDoesNotClaimAVersionRefusalBucket(t *testing.T) {
	t.Parallel()
	source := versionRefusedSource("source-refused")
	h := newRefusalHarness(t, false, source)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.buffer.Reset()
	h.coordinator.Tick(ctx)
	for _, summary := range allFreshnessSummaries(t, &h.buffer) {
		if got, _ := summary["tick_complete"].(bool); got {
			t.Errorf("tick_complete = true on a cancelled tick; line: %v", summary)
		}
		if got := summaryNumber(t, summary, "orgs_source_failed"); got != 0 {
			t.Errorf("orgs_source_failed = %v, want 0 -- a cancelled tick is not a source failure", got)
		}
	}
}
