package projectionrun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// catchUpSource is a source that places its cursor on its clock and may also
// report a trailing-window pass.
type catchUpSource struct {
	*fakeSource
	catchUp contextfabric.ProjectionCatchUp
	window  contextfabric.ProjectionWindowPass
}

func (s *catchUpSource) ProjectionCatchUp(contextfabric.ProjectionCheckpoint) contextfabric.ProjectionCatchUp {
	return s.catchUp
}

func (s *catchUpSource) ProjectionWindowPass(contextfabric.ProjectionCheckpoint) contextfabric.ProjectionWindowPass {
	return s.window
}

const workLeftMessage = "context_fabric: projection pair ended the tick with work left"

func stringList(value any) []string {
	list, _ := value.([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out
}

// A drain that spends its budget while the source still offers a batch ends
// the tick with work left. The summary must say so: the organization is in
// catching_up, not ok; the tick is not complete; the lag of the cursor is on
// the line. A drain that ran dry is ok and complete.
func TestTickSummaryReportsAPairThatEndedWithWorkLeft(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	known := func(lag time.Duration) contextfabric.ProjectionCatchUp {
		return contextfabric.ProjectionCatchUp{CursorKnown: true, CursorAt: now.Add(-lag)}
	}
	ahead := func(catchUp contextfabric.ProjectionCatchUp) contextfabric.ProjectionCatchUp {
		catchUp.WorkAhead = true
		return catchUp
	}
	overdue := contextfabric.ProjectionWindowPass{Open: true, Age: 16 * time.Minute, Bound: 15 * time.Minute, Overdue: true}
	type source struct {
		pages   int // 0: the stream never ends
		plain   bool
		catchUp contextfabric.ProjectionCatchUp
		window  contextfabric.ProjectionWindowPass
	}
	for _, tc := range []struct {
		name                        string
		sources                     []source
		budget                      int
		ok, catchingUp, pairs       float64
		names                       []string
		lagMin, lagMax              float64
		complete                    bool
		windowBehind, windowPassOpn float64
	}{
		{name: "the stream never ends inside the budget", sources: []source{{catchUp: known(time.Hour)}}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-a"}, lagMin: 3600, lagMax: 3660},
		{name: "the source runs dry inside the budget", sources: []source{{pages: 2, catchUp: known(time.Hour)}}, budget: 2, ok: 1, names: []string{}, complete: true},
		{name: "the source cannot place its cursor", sources: []source{{plain: true}}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-a"}, lagMin: -1, lagMax: -1},
		{name: "two sources with work left, one lag unknown", sources: []source{{plain: true}, {catchUp: known(2 * time.Hour)}}, budget: 2, catchingUp: 1, pairs: 2, names: []string{"source-a", "source-b"}, lagMin: 7200, lagMax: 7260},
		{name: "two sources with work left, the larger lag first", sources: []source{{catchUp: known(3 * time.Hour)}, {catchUp: known(time.Hour)}}, budget: 2, catchingUp: 1, pairs: 2, names: []string{"source-a", "source-b"}, lagMin: 10800, lagMax: 10860},
		{name: "one source with work left, the next ran dry", sources: []source{{catchUp: known(time.Hour)}, {pages: 1, catchUp: known(time.Hour)}}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-a"}, lagMin: 3600, lagMax: 3660},
		{name: "one source ran dry, the next has work left", sources: []source{{pages: 1, catchUp: known(time.Hour)}, {catchUp: known(time.Hour)}}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-b"}, lagMin: 3600, lagMax: 3660},
		{name: "a cursor ahead of the clock has no negative lag", sources: []source{{catchUp: known(-time.Hour)}}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-a"}},
		{name: "work left outranks an overdue window pass", sources: []source{{catchUp: known(time.Hour), window: overdue}}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-a"}, lagMin: 3600, lagMax: 3660, windowPassOpn: 1},
		{name: "extra draining disabled, the source does not say rows lie ahead", sources: []source{{catchUp: known(time.Hour)}}, budget: -1, ok: 1, names: []string{}, complete: true},
		{name: "extra draining disabled, the source says rows lie ahead", sources: []source{{catchUp: ahead(known(time.Hour))}}, budget: -1, catchingUp: 1, pairs: 1, names: []string{"source-a"}, lagMin: 3600, lagMax: 3660},
		{name: "the source stops handing out batches but says rows lie ahead", sources: []source{{pages: 1, catchUp: ahead(known(time.Hour))}}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-a"}, lagMin: 3600, lagMax: 3660},
		{name: "a known lag of zero beside an unknown lag is zero", sources: []source{{catchUp: known(-time.Hour)}, {plain: true}}, budget: 2, catchingUp: 1, pairs: 2, names: []string{"source-a", "source-b"}},
	} {
		for _, lifecycle := range []bool{false, true} {
			path := map[bool]string{false: "legacy path", true: "lifecycle path"}[lifecycle]
			t.Run(tc.name+" ("+path+")", func(t *testing.T) {
				t.Parallel()
				var buffer bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&buffer, nil))
				backend := newFakeBackend()
				checkpoints := newFakeCheckpointStore()
				var pairs []projectionrun.SourcePair
				for i, src := range tc.sources {
					name := "source-" + string(rune('a'+i))
					fake := &fakeSource{name: name, pages: src.pages}
					var ps contextfabric.ProjectionSource = &catchUpSource{fakeSource: fake, catchUp: src.catchUp, window: src.window}
					if src.plain {
						ps = fake
					}
					pairs = append(pairs, projectionrun.SourcePair{Name: name, Source: ps})
				}
				config := projectionrun.Config{
					OrgIDs: []string{"org-1"}, Sources: pairs, Backend: backend, Checkpoints: checkpoints,
					RebuildMarkers: newFakeRebuildMarker(), DrainBatchBudget: tc.budget, Logger: logger,
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
				if backend.appliedCount() == 0 {
					t.Fatal("precondition: nothing was applied; the tick did not drain")
				}
				summary := freshnessSummary(t, &buffer)
				requireBucketIdentity(t, summary)
				for key, want := range map[string]float64{
					"orgs_ok": tc.ok, "orgs_catching_up": tc.catchingUp, "sources_catching_up": tc.pairs,
					"orgs_window_behind": tc.windowBehind, "orgs_window_pass_open": tc.windowPassOpn,
				} {
					if got := summaryNumber(t, summary, key); got != want {
						t.Errorf("%s = %v, want %v; line: %v", key, got, want, summary)
					}
				}
				if lag := summaryNumber(t, summary, "catch_up_lag_seconds_max"); lag < tc.lagMin || lag > tc.lagMax {
					t.Errorf("catch_up_lag_seconds_max = %v, want %v..%v; line: %v", lag, tc.lagMin, tc.lagMax, summary)
				}
				if names := stringList(summary["catching_up_sources"]); !reflect.DeepEqual(names, tc.names) {
					t.Errorf("catching_up_sources = %v, want %v", names, tc.names)
				}
				if complete, _ := summary["tick_complete"].(bool); complete != tc.complete {
					t.Errorf("tick_complete = %v, want %v: a tick that left work unread is not complete; line: %v", complete, tc.complete, summary)
				}
			})
		}
	}
}

// The pair's own line says how far its cursor is behind and how far it is
// from the edge of its catch-up pass. -1 and empty mean the source did not
// say.
func TestWorkLeftLine(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cursorAt := now.Add(-2 * time.Hour)
	for _, tc := range []struct {
		name    string
		catchUp contextfabric.ProjectionCatchUp
		want    map[string]any
		lag     [2]float64
	}{
		{"cursor and pass known", contextfabric.ProjectionCatchUp{CursorKnown: true, CursorAt: cursorAt, PassOpen: true, PassEdge: cursorAt.Add(30 * time.Minute)},
			map[string]any{"cursor_known": true, "cursor_at": cursorAt.Format(time.RFC3339Nano), "pass_open": true, "pass_edge": cursorAt.Add(30 * time.Minute).Format(time.RFC3339Nano), "remaining_to_edge_seconds": float64(1800)}, [2]float64{7200, 7260}},
		{"the cursor is past the pass edge", contextfabric.ProjectionCatchUp{CursorKnown: true, CursorAt: cursorAt, PassOpen: true, PassEdge: cursorAt.Add(-time.Minute)},
			map[string]any{"remaining_to_edge_seconds": float64(0)}, [2]float64{7200, 7260}},
		{"no pass open", contextfabric.ProjectionCatchUp{CursorKnown: true, CursorAt: cursorAt},
			map[string]any{"pass_open": false, "pass_edge": "", "remaining_to_edge_seconds": float64(-1)}, [2]float64{7200, 7260}},
		{"cursor unknown, pass open", contextfabric.ProjectionCatchUp{PassOpen: true, PassEdge: now},
			map[string]any{"cursor_known": false, "cursor_at": "", "pass_open": true, "remaining_to_edge_seconds": float64(-1)}, [2]float64{-1, -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
				OrgIDs:  []string{"org-1"},
				Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &catchUpSource{fakeSource: &fakeSource{name: "source-a"}, catchUp: tc.catchUp}}},
				Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
				DrainBatchBudget: 2, Logger: slog.New(slog.NewJSONHandler(&buffer, nil)),
			})
			if err != nil {
				t.Fatalf("new coordinator: %v", err)
			}
			coordinator.Tick(context.Background())
			var lines []map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
				var line map[string]any
				if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == workLeftMessage {
					lines = append(lines, line)
				}
			}
			if len(lines) != 1 {
				t.Fatalf("work-left lines = %d, want 1:\n%s", len(lines), buffer.String())
			}
			line := lines[0]
			if line["source"] != "source-a" || line["applied"].(float64) != 3 || line["batches"].(float64) != 3 {
				t.Errorf("line = %v, want source-a with the 3 applied batches of a budget of 2 extra", line)
			}
			for key, want := range tc.want {
				if line[key] != want {
					t.Errorf("%s = %v, want %v", key, line[key], want)
				}
			}
			if lag := line["lag_seconds"].(float64); lag < tc.lag[0] || lag > tc.lag[1] {
				t.Errorf("lag_seconds = %v, want %v..%v", lag, tc.lag[0], tc.lag[1])
			}
		})
	}
}

// buildingLifecycleStore puts the organization in a build of the given
// required sources that never flips.
type buildingLifecycleStore struct {
	contextfabric.GraphLifecycleStore
	required []string
}

func (b buildingLifecycleStore) Get(context.Context, string) (contextfabric.OrgGraphLifecycle, bool, error) {
	target := int64(1)
	return contextfabric.OrgGraphLifecycle{Status: contextfabric.LifecycleStatusBuilding, ActiveEpoch: 0, TargetEpoch: &target, RequiredSources: b.required}, true, nil
}

func (buildingLifecycleStore) SourceProgress(context.Context, string, int64) ([]contextfabric.BuildSourceProgress, error) {
	return nil, nil
}

func (buildingLifecycleStore) RecordSourceProgress(context.Context, string, int64, string, contextfabric.BuildCompletionMode, int64, time.Time) error {
	return nil
}

func (buildingLifecycleStore) Flip(context.Context, string, int64, time.Duration, time.Time) (contextfabric.OrgGraphLifecycle, error) {
	return contextfabric.OrgGraphLifecycle{}, errors.New("this fixture does not flip")
}

// aheadThenFailing says rows lie ahead after its first batch and fails on the
// next read.
type aheadThenFailing struct{ *catchUpSource }

func (s aheadThenFailing) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if checkpoint.Cursor != "" {
		return contextfabric.ProjectionBatch{}, false, contextfabric.ErrUnavailable
	}
	return s.catchUpSource.NextProjectionBatch(ctx, checkpoint)
}

// A build whose source ended the tick with rows still to read left work. The
// organization is catching up, not "building, nothing to say" (backoff), and
// the tick is not complete. A required source that finished its read is not
// named.
func TestTickSummaryReportsABuildThatEndedWithWorkLeft(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	behind := contextfabric.ProjectionCatchUp{CursorKnown: true, CursorAt: now.Add(-time.Hour)}
	ahead := behind
	ahead.WorkAhead = true
	endless := func(name string) contextfabric.ProjectionSource {
		return &catchUpSource{fakeSource: &fakeSource{name: name}, catchUp: behind}
	}
	for _, tc := range []struct {
		name                               string
		sources                            []contextfabric.ProjectionSource
		budget                             int
		catchingUp, backoff, failed, pairs float64
		names                              []string
		lagMin, lagMax                     float64
		complete                           bool
	}{
		{name: "the drain budget is spent with batches left", sources: []contextfabric.ProjectionSource{endless("source-a")}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-a"}, lagMin: 3600, lagMax: 3660},
		{name: "one source finished, the next has batches left", sources: []contextfabric.ProjectionSource{&fakeSource{name: "source-a", pages: 1}, endless("source-b")}, budget: 2, catchingUp: 1, pairs: 1, names: []string{"source-b"}, lagMin: 3600, lagMax: 3660},
		{name: "one attempt per tick, the source says rows lie ahead", sources: []contextfabric.ProjectionSource{&catchUpSource{fakeSource: &fakeSource{name: "source-a"}, catchUp: ahead}}, budget: -1, catchingUp: 1, pairs: 1, names: []string{"source-a"}, lagMin: 3600, lagMax: 3660},
		{name: "one attempt per tick, the source does not say", sources: []contextfabric.ProjectionSource{endless("source-a")}, budget: -1, backoff: 1, names: []string{}, complete: true},
		{name: "a read that fails after rows lay ahead is a failure, not work left", sources: []contextfabric.ProjectionSource{aheadThenFailing{&catchUpSource{fakeSource: &fakeSource{name: "source-a"}, catchUp: ahead}}}, budget: 2, failed: 1, names: []string{}, complete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			checkpoints := newFakeCheckpointStore()
			var pairs []projectionrun.SourcePair
			var required []string
			for i, src := range tc.sources {
				name := "source-" + string(rune('a'+i))
				pairs = append(pairs, projectionrun.SourcePair{Name: name, Source: src})
				required = append(required, name)
			}
			backend := newFakeBackend()
			coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
				OrgIDs: []string{"org-1"}, Sources: pairs, Backend: backend, Checkpoints: checkpoints,
				RebuildMarkers: newFakeRebuildMarker(), DrainBatchBudget: tc.budget, Logger: slog.New(slog.NewJSONHandler(&buffer, nil)),
				Lifecycle:        buildingLifecycleStore{required: required},
				EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
			})
			if err != nil {
				t.Fatalf("new coordinator: %v", err)
			}
			coordinator.Tick(context.Background())
			if backend.appliedCount() == 0 {
				t.Fatal("precondition: nothing was applied; the build tick did not drain")
			}
			summary := freshnessSummary(t, &buffer)
			requireBucketIdentity(t, summary)
			for key, want := range map[string]float64{
				"orgs_catching_up": tc.catchingUp, "orgs_backoff": tc.backoff, "orgs_source_failed": tc.failed, "sources_catching_up": tc.pairs, "orgs_ok": 0,
			} {
				if got := summaryNumber(t, summary, key); got != want {
					t.Errorf("%s = %v, want %v; line: %v", key, got, want, summary)
				}
			}
			if lag := summaryNumber(t, summary, "catch_up_lag_seconds_max"); lag < tc.lagMin || lag > tc.lagMax {
				t.Errorf("catch_up_lag_seconds_max = %v, want %v..%v", lag, tc.lagMin, tc.lagMax)
			}
			if names := stringList(summary["catching_up_sources"]); !reflect.DeepEqual(names, tc.names) {
				t.Errorf("catching_up_sources = %v, want %v", names, tc.names)
			}
			if complete, _ := summary["tick_complete"].(bool); complete != tc.complete {
				t.Errorf("tick_complete = %v, want %v; line: %v", complete, tc.complete, summary)
			}
		})
	}
}
