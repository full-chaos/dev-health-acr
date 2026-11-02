package projectionrun_test

import (
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
