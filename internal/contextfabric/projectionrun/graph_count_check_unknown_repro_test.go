package projectionrun_test

import (
	"bytes"
	"context"
	"encoding/json"
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

// The owned marker is absent on a graph no walk from zero has marked. The check
// then reads outcome unknown and compares nothing owned. These cells pin what it
// must still say: a per-source line that names the reason, and a lower-bound
// gap read against the graph's TOTAL kind count (stubs included, so it can miss a
// gap but never invent one).

// unknownWarnAfter is the count of passes in a row a source reads unknown before
// its line is a Warn.
const unknownWarnAfter = 3

// totalBackend answers OwnedCountsKnown from per-source watermark state, as the
// falkor adapter does (known only when the watermark carries owned_marked_walk),
// and counts the graph's total nodes of a kind.
type totalBackend struct {
	*countingBackend
	ownedMarked map[string]bool
	totals      map[contextfabric.SubjectKind]int64
	totalCalls  *atomic.Int64
	// markerErrs scripts, per source, the error of the nth marker lookup.
	markerErrs map[string][]error
	rec        *recorder
}

// recorder keeps the order of the reads the check makes.
type recorder struct {
	mu        sync.Mutex
	events    []string
	calls     map[string]int
	deadlines map[string]time.Time
}

// deadline records the deadline of the context a read ran under.
func (r *recorder) deadline(name string, ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deadlines == nil {
		r.deadlines = map[string]time.Time{}
	}
	if d, ok := ctx.Deadline(); ok {
		r.deadlines[name] = d
	}
}

func (r *recorder) add(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[event]++
	return r.calls[event] - 1
}

func (r *recorder) index(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.events {
		if e == event {
			return i
		}
	}
	return -1
}

func (b totalBackend) OwnedCountsKnown(_ context.Context, _, source string) (bool, error) {
	n := b.rec.add("marker:" + source)
	if errs := b.markerErrs[source]; n < len(errs) && errs[n] != nil {
		return false, errs[n]
	}
	return b.ownedMarked[source], nil
}

func (b totalBackend) CountKind(ctx context.Context, org string, kind contextfabric.SubjectKind) (int64, error) {
	b.rec.add("owned:" + string(kind))
	b.rec.deadline("owned", ctx)
	return b.countingBackend.CountKind(ctx, org, kind)
}

// scriptedSource is a counting source whose nth count read returns errs[n].
type scriptedSource struct {
	*countingSource
	name string
	errs []error
	rec  *recorder
}

func (s *scriptedSource) ProjectionSourceCounts(ctx context.Context, org string) (map[contextfabric.SubjectKind]int64, error) {
	n := s.rec.add("probe:" + s.name)
	s.rec.deadline("probe", ctx)
	if n < len(s.errs) && s.errs[n] != nil {
		s.countingSource.callsFor.Add(1)
		return nil, s.errs[n]
	}
	return s.countingSource.ProjectionSourceCounts(ctx, org)
}

func (b totalBackend) CountKindTotal(_ context.Context, _ string, kind contextfabric.SubjectKind) (int64, error) {
	b.totalCalls.Add(1)
	return b.totals[kind], nil
}

// disabledCounting is a counting source its configuration switched off: it has
// no watermark and never will.
type disabledCounting struct{ *countingSource }

func (disabledCounting) Enabled() bool { return false }

type prodShape struct {
	marked       bool
	sourceCount  int64
	ownedCount   int64
	totalCount   int64
	disableTeams bool
	ticks        int
	// probeInterval defaults to a minute (every check probes); tickAt, when set,
	// gives each tick's offset from testStart (else one minute apart).
	probeInterval time.Duration
	tickAt        []time.Duration
	chErr         error
	// markedBy overrides which sources carry the owned marker (default: both when
	// marked, none otherwise); markerErrs and chErrs script per-call errors.
	markedBy      map[string]bool
	markerErrs    map[string][]error
	chErrs        []error
	rec           *recorder
	buffer        *bytes.Buffer
	observer      *belowObserver
	chSource      *countingSource
	teamsSource   *countingSource
	totalCalls    *atomic.Int64
	checks        []projectionrun.GraphCountCheck
	sourceNamesIn []string
}

// run wires prod's shape: two counting sources (clickhouse, teams_projects) and
// one source without counts (episodes), every watermark holding a cursor.
func (p *prodShape) run(t *testing.T) {
	t.Helper()
	kind := contractsv1.ContextFabricSubjectPullRequest
	p.buffer = &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(p.buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	newCounting := func(name string) *countingSource {
		return &countingSource{fakeSource: &fakeSource{name: name, dormant: true}, counts: map[contextfabric.SubjectKind]int64{kind: p.sourceCount}}
	}
	p.chSource, p.teamsSource = newCounting("source-ch"), newCounting("source-teams")
	p.chSource.err = p.chErr
	if p.probeInterval == 0 {
		p.probeInterval = time.Minute
	}
	var teams contextfabric.ProjectionSource = p.teamsSource
	if p.disableTeams {
		teams = disabledCounting{p.teamsSource}
	}
	p.totalCalls = &atomic.Int64{}
	p.rec = &recorder{}
	owned := map[string]bool{"source-ch": p.marked, "source-teams": p.marked}
	if p.markedBy != nil {
		owned = p.markedBy
	}
	backend := totalBackend{
		countingBackend: &countingBackend{fakeBackend: newFakeBackend(), graph: map[contextfabric.SubjectKind]int64{kind: p.ownedCount}},
		ownedMarked:     owned,
		markerErrs:      p.markerErrs,
		rec:             p.rec,
		totals:          map[contextfabric.SubjectKind]int64{kind: p.totalCount},
		totalCalls:      p.totalCalls,
	}
	p.observer = &belowObserver{}
	var clock atomic.Pointer[time.Time]
	start := testStart
	clock.Store(&start)
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-a"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-ch", Source: &scriptedSource{countingSource: p.chSource, name: "ch", errs: p.chErrs, rec: p.rec}},
			{Name: "source-episodes", Source: fakeSourceNoVersionCapability{inner: &fakeSource{name: "source-episodes", dormant: true}}},
			{Name: "source-teams", Source: teams},
		},
		Backend:                        backend,
		Checkpoints:                    newFakeCheckpointStore(),
		RebuildMarkers:                 newFakeRebuildMarker(),
		Observer:                       p.observer,
		Logger:                         logger,
		GraphCountCheckInterval:        time.Minute,
		GraphCountUnknownProbeInterval: p.probeInterval,
		Now:                            func() time.Time { return *clock.Load() },
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if len(p.tickAt) > 0 {
		p.ticks = len(p.tickAt)
	}
	if p.ticks == 0 {
		p.ticks = 2
	}
	for i := 0; i < p.ticks; i++ {
		offset := time.Duration(i) * time.Minute
		if len(p.tickAt) > 0 {
			offset = p.tickAt[i]
		}
		now := testStart.Add(offset)
		clock.Store(&now)
		coordinator.Tick(ctx)
	}
	if len(p.observer.checks) != p.ticks {
		t.Fatalf("want %d reported checks, got %d:\n%s", p.ticks, len(p.observer.checks), p.buffer.String())
	}
	p.checks = p.observer.checks
}

// lines returns the log lines whose "check" field is name, in order.
func (p *prodShape) lines(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(p.buffer.String()), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line is not JSON: %q", raw)
		}
		if line["check"] == name {
			out = append(out, line)
		}
	}
	return out
}

func forSource(lines []map[string]any, source string) []map[string]any {
	var out []map[string]any
	for _, line := range lines {
		if line["source"] == source {
			out = append(out, line)
		}
	}
	return out
}

func wantUnknownCheck(t *testing.T, check projectionrun.GraphCountCheck) {
	t.Helper()
	if check.Outcome != projectionrun.GraphCountCheckUnknown || check.SourcesChecked != 0 || check.SourcesUnknown != 2 || check.Gaps != 0 || check.DayGaps != 0 || check.EdgeGaps != 0 || check.Errors != 0 {
		t.Fatalf("check = %+v, want outcome unknown, sources_checked 0, sources_unknown 2, no gap, no error", check)
	}
}

// (a) A real 90-node gap (source 100, graph total 10) must not stay invisible.
func TestGraphCountCheckUnknownMarkerAbsentReportsLowerBoundGap(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 10}
	p.run(t)
	wantUnknownCheck(t, p.checks[1])
	if strings.Contains(p.buffer.String(), `"check":"graph_below_source"`) || len(p.observer.events) != 0 {
		t.Fatalf("the owned-count gap stays unreported while the marker is absent; log:\n%s", p.buffer.String())
	}
	absent := p.lines(t, "graph_count_owned_marker_absent")
	for _, source := range []string{"source-ch", "source-teams"} {
		got := forSource(absent, source)
		if len(got) != 2 {
			t.Fatalf("%s: want one owned_marker_absent line per pass (2), got %d:\n%s", source, len(got), p.buffer.String())
		}
		last := got[1]
		for key, want := range map[string]any{"reason": "owned_marker_absent", "basis": "total_kind_count", "coverage": "lower_bound", "lower_bound_gap_kinds": float64(1)} {
			if last[key] != want {
				t.Fatalf("%s line %s = %v, want %v; line: %v", source, key, last[key], want, last)
			}
		}
	}
	gaps := p.lines(t, "graph_below_source_lower_bound")
	if len(gaps) != 2 {
		t.Fatalf("want one confirmed lower-bound gap Warn per source (2), got %d:\n%s", len(gaps), p.buffer.String())
	}
	for _, gap := range gaps {
		for key, want := range map[string]any{"kind": "pull_request", "source_count": float64(100), "graph_total_count": float64(10), "basis": "total_kind_count", "coverage": "lower_bound", "level": "WARN"} {
			if gap[key] != want {
				t.Fatalf("lower-bound gap %s = %v, want %v; line: %v", key, gap[key], want, gap)
			}
		}
	}
}

// (b) Total at or above the source: no gap, and the line does not read as a clean
// "0 gaps": it names the basis and that coverage is a lower bound.
func TestGraphCountCheckUnknownMarkerAbsentNoGapIsNotZeroGaps(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 100}
	p.run(t)
	wantUnknownCheck(t, p.checks[1])
	if len(p.lines(t, "graph_below_source_lower_bound")) != 0 || strings.Contains(p.buffer.String(), `"check":"graph_below_source"`) {
		t.Fatalf("no gap may be reported when the total covers the source:\n%s", p.buffer.String())
	}
	absent := p.lines(t, "graph_count_owned_marker_absent")
	if len(absent) != 4 {
		t.Fatalf("want one owned_marker_absent line per source per pass (4), got %d:\n%s", len(absent), p.buffer.String())
	}
	for _, line := range absent {
		for key, want := range map[string]any{"reason": "owned_marker_absent", "basis": "total_kind_count", "coverage": "lower_bound", "lower_bound_gap_kinds": float64(0)} {
			if line[key] != want {
				t.Fatalf("line %s = %v, want %v; line: %v", key, line[key], want, line)
			}
		}
	}
}

// (c) Control: the same counts with the owned marker set are compared and warn.
func TestGraphCountCheckControlMarkedWalkReportsTheGap(t *testing.T) {
	t.Parallel()
	p := &prodShape{marked: true, sourceCount: 100, ownedCount: 10, totalCount: 10}
	p.run(t)
	check := p.checks[1]
	if check.Outcome != projectionrun.GraphCountCheckCompleted || check.SourcesChecked != 2 || check.SourcesUnknown != 0 || check.Gaps != 2 {
		t.Fatalf("check = %+v, want completed, sources_checked 2, 2 gaps", check)
	}
	if len(p.lines(t, "graph_below_source")) != 2 || len(p.observer.events) != 2 {
		t.Fatalf("want graph_below_source warns for both sources; events=%d log:\n%s", len(p.observer.events), p.buffer.String())
	}
	if len(p.lines(t, "graph_count_owned_marker_absent")) != 0 || p.totalCalls.Load() != 0 {
		t.Fatalf("a marked source must not run the marker-absent path (total reads %d):\n%s", p.totalCalls.Load(), p.buffer.String())
	}
}

// (d) The line is Info for the first N-1 passes in a row and Warn from the Nth.
func TestGraphCountCheckUnknownMarkerAbsentWarnsAfterNPasses(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 100, ticks: unknownWarnAfter + 1}
	p.run(t)
	for _, source := range []string{"source-ch", "source-teams"} {
		got := forSource(p.lines(t, "graph_count_owned_marker_absent"), source)
		if len(got) != unknownWarnAfter+1 {
			t.Fatalf("%s: want %d lines, got %d:\n%s", source, unknownWarnAfter+1, len(got), p.buffer.String())
		}
		for i, line := range got {
			want := "INFO"
			if i+1 >= unknownWarnAfter {
				want = "WARN"
			}
			if line["level"] != want || line["unknown_passes"] != float64(i+1) {
				t.Fatalf("%s pass %d: level %v unknown_passes %v, want %s and %d", source, i+1, line["level"], line["unknown_passes"], want, i+1)
			}
		}
	}
}

// (d2) A source off by configuration has no watermark by design: it makes no
// marker-absent line and no Warn at all, however many passes run.
func TestGraphCountCheckUnknownMarkerAbsentDisabledSourceIsSilent(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 100, disableTeams: true, ticks: unknownWarnAfter + 1}
	p.run(t)
	absent := p.lines(t, "graph_count_owned_marker_absent")
	if got := forSource(absent, "source-teams"); len(got) != 0 {
		t.Fatalf("a disabled source produced %d marker-absent lines:\n%s", len(got), p.buffer.String())
	}
	if got := forSource(absent, "source-ch"); len(got) != unknownWarnAfter+1 {
		t.Fatalf("the enabled source must still report: got %d lines:\n%s", len(got), p.buffer.String())
	}
	if p.teamsSource.callsFor.Load() != 0 {
		t.Fatalf("a disabled source was counted %d times", p.teamsSource.callsFor.Load())
	}
}

// WORK pin (not wall time): per pass the marker-absent path reads each enabled
// unknown source's counts once and the graph total once per DISTINCT kind, shared
// by the sources. Here 2 sources x 1 shared kind x 2 passes.
func TestGraphCountCheckUnknownMarkerAbsentReadWork(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 100}
	p.run(t)
	if got := p.chSource.callsFor.Load(); got != 2 {
		t.Fatalf("clickhouse source count reads = %d, want 1 per pass (2)", got)
	}
	if got := p.teamsSource.callsFor.Load(); got != 2 {
		t.Fatalf("teams source count reads = %d, want 1 per pass (2)", got)
	}
	if got := p.totalCalls.Load(); got != 2 {
		t.Fatalf("graph total reads = %d, want 1 per pass for the one shared kind (2)", got)
	}
}
