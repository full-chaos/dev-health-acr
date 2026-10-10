package projectionrun_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// windowReportingSource is a source that reports nothing available and says
// where its trailing-window re-read stands.
type windowReportingSource struct {
	*fakeSource
	pass contextfabric.ProjectionWindowPass
}

func (s *windowReportingSource) ProjectionWindowPass(contextfabric.ProjectionCheckpoint) contextfabric.ProjectionWindowPass {
	return s.pass
}

// tickSummaryFor runs one tick over the sources and returns its summary line,
// on the legacy per-organization path or, with lifecycle set, on the steady
// lifecycle path (the one a deployment with Config.Lifecycle takes).
func tickSummaryFor(t *testing.T, lifecycle bool, sources ...projectionrun.SourcePair) map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	backend := newFakeBackend()
	for _, pair := range sources {
		backend.setWatermark("org-1", pair.Name, contextfabric.ProjectionWatermark{
			OrgID: "org-1", Source: pair.Name, SourceVersion: "test.v1", ProjectedAt: time.Now().UTC(),
		})
	}
	checkpoints := newFakeCheckpointStore()
	config := projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: sources, Backend: backend, Checkpoints: checkpoints,
		RebuildMarkers: newFakeRebuildMarker(), Logger: slog.New(slog.NewJSONHandler(&buffer, nil)),
	}
	if lifecycle {
		config.Lifecycle = servingLifecycleStore{}
		config.EpochCheckpoints = func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints }
	}
	coordinator, err := projectionrun.NewCoordinator(config)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	for _, pair := range sources {
		if pair.Source.(*windowReportingSource).calls.Load() == 0 {
			t.Fatalf("source %s was not read this tick: the summary says nothing about its pass", pair.Name)
		}
	}
	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	return summary
}

// A source that stops at its per-call bound answers "nothing available", the
// same answer a caught-up source gives. The summary must tell them apart: an
// open pass is counted, and an overdue one takes the organization out of ok.
func TestTickSummaryReportsTheTrailingWindowPass(t *testing.T) {
	t.Parallel()
	open := contextfabric.ProjectionWindowPass{Open: true, Age: time.Minute, Bound: 15 * time.Minute}
	overdue := contextfabric.ProjectionWindowPass{Open: true, Age: 16 * time.Minute, Bound: 15 * time.Minute, Overdue: true}
	for _, tc := range []struct {
		name                          string
		passes                        []contextfabric.ProjectionWindowPass
		currentVersion                string
		ok, behind, open, rebuildOwed float64
	}{
		{name: "no pass open", passes: []contextfabric.ProjectionWindowPass{{}}, ok: 1},
		{name: "a pass open inside its bound", passes: []contextfabric.ProjectionWindowPass{open}, ok: 1, open: 1},
		{name: "a pass open past its bound", passes: []contextfabric.ProjectionWindowPass{overdue}, behind: 1, open: 1},
		{name: "one source caught up, the next overdue", passes: []contextfabric.ProjectionWindowPass{{}, overdue}, behind: 1, open: 1},
		{name: "one source overdue, the next caught up", passes: []contextfabric.ProjectionWindowPass{overdue, {}}, behind: 1, open: 1},
		{name: "one source overdue, the next open", passes: []contextfabric.ProjectionWindowPass{overdue, open}, behind: 1, open: 1},
		{name: "one source open, the next caught up", passes: []contextfabric.ProjectionWindowPass{open, {}}, ok: 1, open: 1},
		{name: "a rebuild owed outranks an overdue pass", passes: []contextfabric.ProjectionWindowPass{overdue}, currentVersion: "test.v2", open: 1, rebuildOwed: 1},
	} {
		for _, lifecycle := range []bool{false, true} {
			path := map[bool]string{false: "legacy path", true: "lifecycle path"}[lifecycle]
			t.Run(tc.name+" ("+path+")", func(t *testing.T) {
				t.Parallel()
				var pairs []projectionrun.SourcePair
				for i, pass := range tc.passes {
					name := "source-" + string(rune('a'+i))
					pairs = append(pairs, projectionrun.SourcePair{Name: name, Source: &windowReportingSource{
						fakeSource: &fakeSource{name: name, dormant: true, currentSourceVersion: tc.currentVersion}, pass: pass,
					}})
				}
				summary := tickSummaryFor(t, lifecycle, pairs...)
				for key, want := range map[string]float64{
					"orgs_ok": tc.ok, "orgs_window_behind": tc.behind, "orgs_window_pass_open": tc.open,
					"orgs_rebuild_required": tc.rebuildOwed, "orgs_stale": tc.rebuildOwed, "pending_rebuild_orgs_total": tc.rebuildOwed,
				} {
					if got := summaryNumber(t, summary, key); got != want {
						t.Errorf("%s = %v, want %v; line: %v", key, got, want, summary)
					}
				}
				if complete, _ := summary["tick_complete"].(bool); !complete {
					t.Errorf("tick_complete = false: the window_behind bucket is not in the identity the flag is derived from; line: %v", summary)
				}
			})
		}
	}
}

// The pass state is where the drain STOPPED. A source that applied a batch on
// its last attempt reports none, whatever its capability would say.
func TestAnAppliedRunCarriesNoWindowPass(t *testing.T) {
	t.Parallel()
	overdue := contextfabric.ProjectionWindowPass{Open: true, Age: 16 * time.Minute, Bound: 15 * time.Minute, Overdue: true}
	var buffer bytes.Buffer
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &windowReportingSource{
			fakeSource: &fakeSource{name: "source-a"}, pass: overdue,
		}}},
		Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: slog.New(slog.NewJSONHandler(&buffer, nil)),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	if ok, behind, open := summaryNumber(t, summary, "orgs_ok"), summaryNumber(t, summary, "orgs_window_behind"), summaryNumber(t, summary, "orgs_window_pass_open"); ok != 1 || behind != 0 || open != 0 {
		t.Fatalf("a tick that ended on an applied batch: orgs_ok=%v orgs_window_behind=%v orgs_window_pass_open=%v, want 1/0/0", ok, behind, open)
	}
}
