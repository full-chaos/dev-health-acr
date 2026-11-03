package projectionrun_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12649: the graph counts the nodes the owned writer marked, never a stub a
// relationship created. A graph no walk from zero has marked yet would read as one
// large gap per kind, so its source reads unknown: never a gap, never "0 gaps".

func lastCheck(t *testing.T, h *countHarness) projectionrun.GraphCountCheck {
	t.Helper()
	if len(h.observer.checks) == 0 {
		t.Fatal("no count check was reported")
	}
	return h.observer.checks[len(h.observer.checks)-1]
}

func TestASourceNoWalkHasMarkedReadsUnknownNotZeroGaps(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
		nil, nil, time.Minute)
	h.backend.unmarked = true
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	if len(h.observer.events) != 0 || strings.Contains(h.log.String(), `"check":"graph_below_source"`) {
		t.Fatalf("an unmarked graph was reported as a gap:\n%s", h.log.String())
	}
	if h.source.callsFor.Load() != 0 {
		t.Fatalf("the source was counted %d times for a source that cannot be compared", h.source.callsFor.Load())
	}
	check := lastCheck(t, h)
	if check.Outcome != projectionrun.GraphCountCheckUnknown || check.SourcesUnknown != 1 || check.KindsCompared != 0 || check.Gaps != 0 {
		t.Fatalf("check = %+v, want outcome unknown, one unknown source, nothing compared", check)
	}
}

func TestAMarkedSourceIsComparedAsBefore(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		nil, nil, time.Minute)
	h.run(testStart)
	check := lastCheck(t, h)
	if check.Outcome != projectionrun.GraphCountCheckCompleted || check.SourcesUnknown != 0 || check.KindsCompared != 1 {
		t.Fatalf("check = %+v, want completed with the kind compared", check)
	}
}

func TestAnUnreadableOwnedMarkerFailsTheCheck(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
		nil, nil, time.Minute)
	h.backend.markerErr = errors.New("falkor down")
	h.run(testStart)
	check := lastCheck(t, h)
	if check.Outcome != projectionrun.GraphCountCheckFailed || check.Errors != 1 || check.Gaps != 0 {
		t.Fatalf("check = %+v, want failed with one error and no gap", check)
	}
	if !strings.Contains(h.log.String(), `"stage":"owned_marker"`) {
		t.Fatalf("the failed marker read was not named:\n%s", h.log.String())
	}
}

// The Info line carries the unknown outcome and the count of sources it could not
// compare, so "the check could not compare" is visible without the Warn level.
func TestTheFinishedLineSaysUnknown(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
		nil, nil, time.Minute)
	h.backend.unmarked = true
	h.run(testStart)
	check := lastCheck(t, h)
	var buffer strings.Builder
	projectionrun.SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buffer, nil))}.ObserveGraphCountCheck(check)
	got := buffer.String()
	if !strings.Contains(got, `"outcome":"unknown"`) || !strings.Contains(got, `"sources_unknown":1`) {
		t.Fatalf("finished line = %s, want outcome unknown and sources_unknown 1", got)
	}
}

// daySource is a counting source that also counts its rows per UTC day of their
// sync stamp (CHAOS-12649).
type daySource struct {
	*countingSource
	days map[contextfabric.SubjectKind]map[int64]int64
	err  error
}

func (s daySource) ProjectionSourceDayCounts(context.Context, string) (map[contextfabric.SubjectKind]map[int64]int64, error) {
	return s.days, s.err
}

const (
	day20387 int64 = 20387 // 2025-10-26
	day20388 int64 = 20388 // 2025-10-27
)

func newDayHarness(t *testing.T, sourceDays, graphDays map[contextfabric.SubjectKind]map[int64]int64, sourceErr, graphErr error) *countHarness {
	t.Helper()
	kind := contractsv1.ContextFabricSubjectCIRun
	h := buildCountHarness(t,
		map[contextfabric.SubjectKind]int64{kind: 1000},
		map[contextfabric.SubjectKind]int64{kind: 997},
		nil, nil, time.Minute,
		func(s *countingSource) contextfabric.ProjectionSource {
			return daySource{countingSource: s, days: sourceDays, err: sourceErr}
		}, nil)
	h.backend.days, h.backend.daysErr = graphDays, graphErr
	return h
}

// The askdev-1 shape: the kind's total is inside its tolerance (997 of 1000),
// but one day of sync stamps is missing from the graph. Today's check passes; the
// day check warns, with the day, the kind and both counts, at the second check.
func TestAShortDayInsideTheKindsToleranceWarnsWithTheDay(t *testing.T) {
	t.Parallel()
	kind := contractsv1.ContextFabricSubjectCIRun
	h := newDayHarness(t,
		map[contextfabric.SubjectKind]map[int64]int64{kind: {day20387: 997, day20388: 3}},
		map[contextfabric.SubjectKind]map[int64]int64{kind: {day20387: 997}},
		nil, nil)
	h.run(testStart)
	if strings.Contains(h.log.String(), "graph_below_source_on_day") {
		t.Fatal("a short day seen once may be rows in flight and must not warn yet")
	}
	h.run(testStart.Add(time.Minute))
	logged := h.log.String()
	if strings.Contains(logged, `"check":"graph_below_source"`) {
		t.Fatalf("the kind's total is inside its tolerance, so the total check must stay silent:\n%s", logged)
	}
	for _, want := range []string{`"check":"graph_below_source_on_day"`, `"kind":"` + string(kind) + `"`, `"day":"2025-10-27"`, `"source_count":3`, `"graph_count":0`, `"tolerance":0`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("missing %s in:\n%s", want, logged)
		}
	}
	check := lastCheck(t, h)
	if check.DayGaps != 1 || check.DaysCompared != 2 || check.Outcome != projectionrun.GraphCountCheckCompleted {
		t.Fatalf("check = %+v, want one day gap of two days compared, completed", check)
	}
}

// Days in step, and a day short by exactly its tolerance (103 against 100: the
// floor of 3), do not warn.
func TestDaysInStepDoNotWarn(t *testing.T) {
	t.Parallel()
	kind := contractsv1.ContextFabricSubjectCIRun
	h := newDayHarness(t,
		map[contextfabric.SubjectKind]map[int64]int64{kind: {day20387: 997, day20388: 103}},
		map[contextfabric.SubjectKind]map[int64]int64{kind: {day20387: 997, day20388: 100}},
		nil, nil)
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	if strings.Contains(h.log.String(), "graph_below_source_on_day") {
		t.Fatalf("days in step warned:\n%s", h.log.String())
	}
	if check := lastCheck(t, h); check.DaysCompared != 2 || check.DayGaps != 0 {
		t.Fatalf("check = %+v, want two days compared and no gap", check)
	}
}

func TestAnUnreadableDayCountFailsTheCheck(t *testing.T) {
	t.Parallel()
	kind := contractsv1.ContextFabricSubjectCIRun
	days := map[contextfabric.SubjectKind]map[int64]int64{kind: {day20387: 1000}}
	for name, tc := range map[string]struct {
		sourceErr, graphErr error
		stage               string
	}{
		"source": {sourceErr: errors.New("clickhouse down"), stage: `"stage":"source_day_count"`},
		"graph":  {graphErr: errors.New("falkor down"), stage: `"stage":"graph_day_count"`},
	} {
		h := newDayHarness(t, days, days, tc.sourceErr, tc.graphErr)
		h.run(testStart)
		check := lastCheck(t, h)
		if check.Outcome != projectionrun.GraphCountCheckFailed || check.Errors != 1 || !strings.Contains(h.log.String(), tc.stage) {
			t.Fatalf("%s: check = %+v, want failed with %s:\n%s", name, check, tc.stage, h.log.String())
		}
	}
}

// A backend that counts nodes and not days, beside a source that counts days,
// cannot make a measurement the check owes: unmeasured, and the check fails.
func TestABackendWithoutDayCountsIsUnmeasured(t *testing.T) {
	t.Parallel()
	kind := contractsv1.ContextFabricSubjectCIRun
	days := map[contextfabric.SubjectKind]map[int64]int64{kind: {day20387: 1000}}
	h := buildCountHarness(t,
		map[contextfabric.SubjectKind]int64{kind: 1000},
		map[contextfabric.SubjectKind]int64{kind: 1000},
		nil, nil, time.Minute,
		func(s *countingSource) contextfabric.ProjectionSource {
			return daySource{countingSource: s, days: days}
		},
		func(b *countingBackend) contextfabric.ProjectionBackend {
			return nodeAndEdgeBackend{fakeBackend: b.fakeBackend, inner: b}
		})
	h.run(testStart)
	check := lastCheck(t, h)
	if check.Outcome != projectionrun.GraphCountCheckFailed || !strings.Contains(h.log.String(), `"reason":"backend_no_day_counts"`) {
		t.Fatalf("check = %+v, want failed with backend_no_day_counts:\n%s", check, h.log.String())
	}
}

// nodeAndEdgeBackend counts nodes and edges, and not days.
type nodeAndEdgeBackend struct {
	*fakeBackend
	inner *countingBackend
}

func (b nodeAndEdgeBackend) CountKind(ctx context.Context, org string, kind contextfabric.SubjectKind) (int64, error) {
	return b.inner.CountKind(ctx, org, kind)
}

func (b nodeAndEdgeBackend) OwnedCountsKnown(ctx context.Context, org, source string) (bool, error) {
	return b.inner.OwnedCountsKnown(ctx, org, source)
}

func (b nodeAndEdgeBackend) CountEdges(ctx context.Context, org string, key contextfabric.EdgeCountKey) (int64, error) {
	return b.inner.CountEdges(ctx, org, key)
}
