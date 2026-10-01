package projectionrun_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

func versionRefusedTick(t *testing.T, ticks int, sources ...*fakeSource) map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	pairs := make([]projectionrun.SourcePair, 0, len(sources))
	for _, source := range sources {
		pairs = append(pairs, projectionrun.SourcePair{Name: source.name, Source: source})
	}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        pairs,
		Backend:        newFakeBackend(),
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	for i := 0; i < ticks; i++ {
		buffer.Reset()
		coordinator.Tick(context.Background())
	}
	for _, source := range sources {
		if source.calls.Load() == 0 {
			t.Fatalf("source %s was never called; the assertions would be vacuous", source.name)
		}
	}
	return freshnessSummary(t, &buffer)
}

func versionRefusedSource(name string) *fakeSource {
	return &fakeSource{name: name, err: fmt.Errorf("read: %w", contextfabric.ErrProjectionSourceVersionChanged)}
}

func failedSource(name string) *fakeSource {
	return &fakeSource{name: name, err: fmt.Errorf("read: %w", contextfabric.ErrUnavailable)}
}

func requireBuckets(t *testing.T, summary map[string]any, rebuild, failed, sourcesFailed float64) {
	t.Helper()
	if got := summaryNumber(t, summary, "orgs_rebuild_required"); got != rebuild {
		t.Errorf("orgs_rebuild_required = %v, want %v", got, rebuild)
	}
	if got := summaryNumber(t, summary, "orgs_source_failed"); got != failed {
		t.Errorf("orgs_source_failed = %v, want %v", got, failed)
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != sourcesFailed {
		t.Errorf("sources_failed = %v, want %v", got, sourcesFailed)
	}
}

func TestAVersionRefusedSourceCountsAsRebuildRequiredNotSourceFailed(t *testing.T) {
	t.Parallel()
	summary := versionRefusedTick(t, 1, versionRefusedSource("source-refused"))
	requireBuckets(t, summary, 1, 0, 0)
	if got := summaryNumber(t, summary, "orgs_stale"); got != 1 {
		t.Errorf("orgs_stale = %v, want 1", got)
	}
}

func TestAVersionRefusedSourceStaysRebuildRequiredWhileWithheldByBackoff(t *testing.T) {
	t.Parallel()
	summary := versionRefusedTick(t, 2, versionRefusedSource("source-refused"))
	requireBuckets(t, summary, 1, 0, 0)
	if got := summaryNumber(t, summary, "sources_in_failure_backoff"); got != 0 {
		t.Errorf("sources_in_failure_backoff = %v, want 0 -- a refused version is not an outage in backoff", got)
	}
}

func TestAGenuineSourceFailureStillCountsAsSourceFailed(t *testing.T) {
	t.Parallel()
	summary := versionRefusedTick(t, 1, failedSource("source-failing"))
	requireBuckets(t, summary, 0, 1, 1)
}

func TestAGenuineFailureOutranksAVersionRefusalInTheBucketButBothAreCounted(t *testing.T) {
	t.Parallel()
	summary := versionRefusedTick(t, 1, versionRefusedSource("source-refused"), failedSource("source-failing"))
	requireBuckets(t, summary, 0, 1, 1)
	if got := summaryNumber(t, summary, "orgs_stale"); got != 1 {
		t.Errorf("orgs_stale = %v, want 1 -- the rebuild owed must stay visible beside the outage", got)
	}
}
