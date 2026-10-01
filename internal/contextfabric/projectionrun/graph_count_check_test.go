package projectionrun_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type countingSource struct {
	*fakeSource
	counts   map[contextfabric.SubjectKind]int64
	err      error
	callsFor atomic.Int64
	onCount  func()
}

func (s *countingSource) ProjectionSourceCounts(context.Context, string) (map[contextfabric.SubjectKind]int64, error) {
	s.callsFor.Add(1)
	if s.onCount != nil {
		s.onCount()
	}
	return s.counts, s.err
}

type countingBackend struct {
	*fakeBackend
	graph map[contextfabric.SubjectKind]int64
	err   error
}

func (b *countingBackend) CountKind(_ context.Context, _ string, kind contextfabric.SubjectKind) (int64, error) {
	return b.graph[kind], b.err
}

type belowObserver struct {
	mu     sync.Mutex
	events []projectionrun.GraphBelowSource
	checks []projectionrun.GraphCountCheck
}

func (o *belowObserver) ObserveGraphCountCheck(c projectionrun.GraphCountCheck) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.checks = append(o.checks, c)
}

func (o *belowObserver) ObserveProjectionOutcome(projectionrun.Outcome)    {}
func (o *belowObserver) ObserveProjectionDrain(projectionrun.DrainOutcome) {}

func (o *belowObserver) ObserveGraphBelowSource(e projectionrun.GraphBelowSource) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

type countHarness struct {
	source   *countingSource
	observer *belowObserver
	log      *bytes.Buffer
	marker   *fakeRebuildMarker
	run      func(now time.Time)
	cancel   context.CancelFunc
}

func newCountHarness(t *testing.T, sourceCounts, graphCounts map[contextfabric.SubjectKind]int64, sourceErr, graphErr error, interval time.Duration, fetchErr ...error) *countHarness {
	t.Helper()
	var sourceFetchErr error
	if len(fetchErr) > 0 {
		sourceFetchErr = fetchErr[0]
	}
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source := &countingSource{fakeSource: &fakeSource{name: "source-a", dormant: true, err: sourceFetchErr}, counts: sourceCounts, err: sourceErr}
	backend := &countingBackend{fakeBackend: newFakeBackend(), graph: graphCounts, err: graphErr}
	observer := &belowObserver{}
	marker := newFakeRebuildMarker()
	var clock atomic.Pointer[time.Time]
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	clock.Store(&start)
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:                  []string{"org-a"},
		Sources:                 []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend:                 backend,
		Checkpoints:             newFakeCheckpointStore(),
		RebuildMarkers:          marker,
		Observer:                observer,
		Logger:                  logger,
		GraphCountCheckInterval: interval,
		Now:                     func() time.Time { return *clock.Load() },
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &countHarness{source: source, observer: observer, log: buffer, marker: marker, cancel: cancel, run: func(now time.Time) {
		clock.Store(&now)
		coordinator.Tick(ctx)
	}}
}

var testStart = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func TestGraphBelowSourceWarnsWhenASourceRowWasSkipped(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 900},
		nil, nil, time.Minute)
	h.run(testStart)
	if len(h.observer.events) != 0 {
		t.Fatalf("a gap seen once may be rows in flight and must not warn yet")
	}
	h.run(testStart.Add(time.Minute))
	logged := h.log.String()
	for _, want := range []string{`"check":"graph_below_source"`, `"kind":"pull_request"`, `"source_count":1000`, `"graph_count":900`, `"tolerance":20`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("missing %s in:\n%s", want, logged)
		}
	}
	if len(h.observer.events) != 1 || h.observer.events[0].SourceCount != 1000 || h.observer.events[0].GraphCount != 900 {
		t.Fatalf("expected one counter event, got %+v", h.observer.events)
	}
}

func TestGraphBelowSourceSilentWhenHealthy(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000, contractsv1.ContextFabricSubjectRepository: 5},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000, contractsv1.ContextFabricSubjectRepository: 5},
		nil, nil, time.Minute)
	h.run(testStart)
	if h.source.callsFor.Load() != 1 {
		t.Fatalf("the check must have run once, ran %d", h.source.callsFor.Load())
	}
	if strings.Contains(h.log.String(), "graph_below_source") || strings.Contains(h.log.String(), "count check failed") || len(h.observer.events) != 0 {
		t.Fatalf("healthy run must not warn:\n%s", h.log.String())
	}
}

func TestGraphBelowSourceToleratesRowsOmittedByDesign(t *testing.T) {
	t.Parallel()
	// 3 absolute on a small kind, 2% on a large one: exactly at tolerance is silent, one past it warns.
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectRepository: 10, contractsv1.ContextFabricSubjectPullRequest: 1000, contractsv1.ContextFabricSubjectDeployment: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectRepository: 7, contractsv1.ContextFabricSubjectPullRequest: 980, contractsv1.ContextFabricSubjectDeployment: 979},
		nil, nil, time.Minute)
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	if len(h.observer.events) != 1 || h.observer.events[0].Kind != contractsv1.ContextFabricSubjectDeployment {
		t.Fatalf("only the kind beyond tolerance may warn, got %+v", h.observer.events)
	}
}

func TestGraphCountErrorWarnsAndTickCompletes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sourceErr, graphErr error
		stage               string
	}{
		"source": {sourceErr: errors.New("clickhouse down"), stage: `"stage":"source_count"`},
		"graph":  {graphErr: errors.New("falkor down"), stage: `"stage":"graph_count"`},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newCountHarness(t,
				map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
				map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
				tc.sourceErr, tc.graphErr, time.Minute)
			h.run(testStart)
			logged := h.log.String()
			if !strings.Contains(logged, "projection count check failed") || !strings.Contains(logged, tc.stage) {
				t.Fatalf("count error must be its own Warn:\n%s", logged)
			}
			if strings.Contains(logged, "graph_below_source") {
				t.Fatalf("a count error must never read as a gap:\n%s", logged)
			}
			if !strings.Contains(logged, "projection tick freshness summary") {
				t.Fatalf("tick must still complete:\n%s", logged)
			}
		})
	}
}

func TestGraphCountCheckIsRateLimitedPerInterval(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
		nil, nil, 10*time.Minute)
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	h.run(testStart.Add(9 * time.Minute))
	if got := h.source.callsFor.Load(); got != 1 {
		t.Fatalf("within one interval the check must run once, ran %d", got)
	}
	h.run(testStart.Add(10 * time.Minute))
	if got := h.source.callsFor.Load(); got != 2 {
		t.Fatalf("after the interval the check must run again, total %d", got)
	}
}

func TestGraphCountCheckDisabledByNegativeInterval(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
		nil, nil, -1)
	h.run(testStart)
	if h.source.callsFor.Load() != 0 || len(h.observer.events) != 0 {
		t.Fatalf("disabled check must not run")
	}
}

func TestGraphCountCheckSkippedWhileASourceIsFailing(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
		nil, nil, time.Minute, errors.New("source down"))
	h.run(testStart)
	if h.source.callsFor.Load() != 0 || len(h.observer.events) != 0 {
		t.Fatalf("a pair that did not end drained must not be compared (graph is catching up by design)")
	}
}

func TestGraphBelowSourceTransientGapDoesNotWarn(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 900},
		nil, nil, time.Minute)
	h.run(testStart)
	h.source.counts = map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 900}
	h.run(testStart.Add(time.Minute))
	h.source.counts = map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000}
	h.run(testStart.Add(2 * time.Minute))
	if len(h.observer.events) != 0 || strings.Contains(h.log.String(), "graph_below_source") {
		t.Fatalf("a gap that closed between checks must not warn, and its first sighting must not carry over:\n%s", h.log.String())
	}
}

func TestGraphCountCheckNotAuthorizedByAPreviousTicksDrain(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
		nil, nil, time.Minute)
	h.run(testStart)
	if h.source.callsFor.Load() != 1 {
		t.Fatalf("first tick must check once, ran %d", h.source.callsFor.Load())
	}
	h.marker.mu.Lock()
	h.marker.inProgress["org-a"] = true
	h.marker.mu.Unlock()
	h.run(testStart.Add(time.Minute))
	if got := h.source.callsFor.Load(); got != 1 {
		t.Fatalf("a tick that only resumed a rebuild drained nothing and must not check against the purged graph, checks=%d", got)
	}
}

func TestGraphBelowSourceFirstSightingDoesNotSurviveAnUndrainedTick(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 900},
		nil, nil, time.Minute)
	h.run(testStart)
	h.source.fakeSource.err = errors.New("source down")
	h.run(testStart.Add(time.Minute))
	h.source.fakeSource.err = nil
	h.run(testStart.Add(24 * time.Hour))
	if len(h.observer.events) != 0 {
		t.Fatalf("a sighting from before an undrained tick must not confirm a gap after it: %+v", h.observer.events)
	}
	if h.source.callsFor.Load() != 2 {
		t.Fatalf("the post-recovery tick must have checked (calls=%d)", h.source.callsFor.Load())
	}
}

func TestGraphBelowSourceWarnsWhenASmallKindIsWhollyMissing(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectTeam: 1},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectTeam: 0},
		nil, nil, time.Minute)
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	if len(h.observer.events) != 1 || h.observer.events[0].Tolerance != 0 {
		t.Fatalf("one source team with none in the graph is a gap, got %+v", h.observer.events)
	}
}
