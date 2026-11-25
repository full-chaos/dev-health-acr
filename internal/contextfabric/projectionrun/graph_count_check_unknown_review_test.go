package projectionrun_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// (O1a) A gap, a refused probe, a gap: the second gap is not "two probes in a
// row", so it must not warn. The other source, probed cleanly each time, does.
func TestGraphCountCheckUnknownMarkerAbsentRefusedProbeBreaksTheGapRun(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 10, ticks: 3,
		chErrs: []error{nil, errors.New("synthetic refusal"), nil}}
	p.run(t)
	gaps := p.lines(t, "graph_below_source_lower_bound")
	if got := forSource(gaps, "source-ch"); len(got) != 0 {
		t.Fatalf("a gap measured at probes 1 and 3 with a refused probe between them warned %d time(s):\n%s", len(got), p.buffer.String())
	}
	if got := forSource(gaps, "source-teams"); len(got) != 2 {
		t.Fatalf("the cleanly probed source must warn at probes 2 and 3 (2 lines), got %d:\n%s", len(got), p.buffer.String())
	}
}

// (O1b) A failed marker lookup breaks the run of marker-absent checks: the next
// absent check starts again at unknown_passes 1 and stays INFO.
func TestGraphCountCheckUnknownMarkerAbsentMarkerErrorResetsTheRun(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 100, ticks: 4,
		markerErrs: map[string][]error{"source-ch": {nil, nil, errors.New("synthetic marker failure"), nil}}}
	p.run(t)
	lines := forSource(p.lines(t, "graph_count_owned_marker_absent"), "source-ch")
	if len(lines) != 3 {
		t.Fatalf("want marker-absent lines at checks 1, 2 and 4 (3), got %d:\n%s", len(lines), p.buffer.String())
	}
	last := lines[2]
	if last["unknown_passes"] != float64(1) || last["level"] != "INFO" {
		t.Fatalf("after a marker error the run restarts: unknown_passes %v level %v, want 1 and INFO", last["unknown_passes"], last["level"])
	}
}

// (O2) The marker-absent reads run after every source's marker lookup and the
// marked sources' own comparison, so a slow probe cannot take their time.
func TestGraphCountCheckUnknownMarkerAbsentProbesRunAfterTheMarkedSources(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 100, totalCount: 100, ticks: 1,
		markedBy: map[string]bool{"source-ch": false, "source-teams": true}}
	p.run(t)
	probe := p.rec.index("probe:ch")
	for _, before := range []string{"marker:source-ch", "marker:source-teams", "owned:pull_request"} {
		if at := p.rec.index(before); at < 0 || probe < 0 || at > probe {
			t.Fatalf("%s (event %d) must come before the unknown source's probe read (event %d); order: %v", before, at, probe, p.rec.events)
		}
	}
	if p.checks[0].SourcesChecked != 1 || p.checks[0].SourcesUnknown != 1 {
		t.Fatalf("check = %+v, want the marked source compared and one unknown", p.checks[0])
	}
}

// (O5) A refused marker lookup on a source its configuration switched off says
// nothing: no owned_marker warning line for it.
func TestGraphCountCheckDisabledSourceMarkerErrorIsSilent(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 100, disableTeams: true, ticks: 2,
		markerErrs: map[string][]error{"source-teams": {errors.New("synthetic marker failure"), errors.New("synthetic marker failure")}}}
	p.run(t)
	for _, line := range p.lines(t, "graph_count") {
		if line["source"] == "source-teams" {
			t.Fatalf("a disabled source logged a marker failure: %v", line)
		}
	}
	for _, raw := range strings.Split(strings.TrimSpace(p.buffer.String()), "\n") {
		if strings.Contains(raw, `"source":"source-teams"`) && (strings.Contains(raw, `"level":"WARN"`) || strings.Contains(raw, `"level":"ERROR"`) || strings.Contains(raw, `"level":"INFO"`)) {
			t.Fatalf("a disabled source logged above debug level: %s", raw)
		}
	}
}

// (O2) The probes run under their own bounded context, not the node phase's: its
// deadline (10 s) is earlier than the one the marked sources' reads ran under (30 s).
func TestGraphCountCheckUnknownMarkerAbsentProbeHasItsOwnDeadline(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 100, totalCount: 100, ticks: 1,
		markedBy: map[string]bool{"source-ch": false, "source-teams": true}}
	p.run(t)
	probe, okProbe := p.rec.deadlines["probe"]
	owned, okOwned := p.rec.deadlines["owned"]
	if !okProbe || !okOwned {
		t.Fatalf("both reads must run under a bounded context: probe %v owned %v", okProbe, okOwned)
	}
	if !probe.Before(owned) {
		t.Fatalf("the probe ran under the node deadline (%v), want its own earlier one (%v)", owned, probe)
	}
}

// (O2) A probe that runs out of its own time is a failed probe with a limit
// class on its line; it never cancels the check or the marked source's comparison.
func TestGraphCountCheckUnknownMarkerAbsentProbeTimeoutIsAFailedProbeNotACancelledCheck(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 100, totalCount: 100, ticks: 1,
		markedBy: map[string]bool{"source-ch": false, "source-teams": true},
		chErrs:   []error{fmt.Errorf("count read: %w", context.DeadlineExceeded)}}
	p.run(t)
	check := p.checks[0]
	if check.Outcome != projectionrun.GraphCountCheckUnknown || check.SourcesChecked != 1 || check.Errors != 0 {
		t.Fatalf("check = %+v, want unknown with the marked source compared and no error", check)
	}
	lines := forSource(p.lines(t, "graph_count_owned_marker_absent"), "source-ch")
	if len(lines) != 1 {
		t.Fatalf("want one marker-absent line for source-ch, got %d:\n%s", len(lines), p.buffer.String())
	}
	for key, want := range map[string]any{"level": "WARN", "probe": "failed", "limit_class": "execution_time"} {
		if lines[0][key] != want {
			t.Fatalf("line %s = %v, want %v; line %v", key, lines[0][key], want, lines[0])
		}
	}
}

// A source its configuration switched off, whose graph still carries the owned
// marker, takes the known path as it did before the marker-absent change: its
// count read returns nothing to compare and the check completes.
func TestGraphCountCheckDisabledSourceWithMarkerIsCheckedAsBefore(t *testing.T) {
	t.Parallel()
	p := &prodShape{marked: true, sourceCount: 100, ownedCount: 100, totalCount: 100, disableTeams: true, ticks: 1}
	p.run(t)
	check := p.checks[0]
	if check.Outcome != projectionrun.GraphCountCheckCompleted || check.SourcesChecked != 2 || check.SourcesUnknown != 0 {
		t.Fatalf("check = %+v, want completed with both sources checked and none unknown", check)
	}
}

// The marker-absent probes of one check share a 10 s deadline (the check holds
// the org lock, so they must stay short); the node phase keeps its 30 s.
func TestGraphCountCheckUnknownMarkerAbsentProbeDeadlineIsShort(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 100, totalCount: 100, ticks: 1,
		markedBy: map[string]bool{"source-ch": false, "source-teams": true}}
	started := time.Now()
	p.run(t)
	probe, ok := p.rec.deadlines["probe"]
	if !ok {
		t.Fatal("the probe ran without a deadline")
	}
	if span := probe.Sub(started); span > 15*time.Second || span <= 0 {
		t.Fatalf("probe deadline is %v after the start, want at most 10 s plus slack", span)
	}
}
