package projectionrun_test

import (
	"strings"
	"testing"
	"time"
)

// refusedRead is a count read ClickHouse refused with a memory-limit exception.
type refusedRead struct{}

func (refusedRead) Error() string         { return "synthetic refusal text" }
func (refusedRead) ClickHouseCode() int32 { return 241 }
func (refusedRead) LimitClass() string    { return "memory" }

func probeStates(lines []map[string]any) []any {
	states := make([]any, 0, len(lines))
	for _, line := range lines {
		states = append(states, line["probe"])
	}
	return states
}

// The marker-absent reads run at most once per probe interval per source: a check
// inside the interval reads nothing; after it, the reads run again. A lower-bound
// gap confirms across two probes, not across two checks.
func TestGraphCountCheckUnknownMarkerAbsentProbeRunsOncePerInterval(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 10, probeInterval: time.Hour,
		tickAt: []time.Duration{0, time.Minute, 30 * time.Minute, 61 * time.Minute}}
	p.run(t)
	if got := p.chSource.callsFor.Load(); got != 2 {
		t.Fatalf("clickhouse source reads = %d, want 2 (checks at 0 and 61 minutes)", got)
	}
	if got := p.teamsSource.callsFor.Load(); got != 2 {
		t.Fatalf("teams source reads = %d, want 2", got)
	}
	if got := p.totalCalls.Load(); got != 2 {
		t.Fatalf("graph total reads = %d, want 2", got)
	}
	got := probeStates(forSource(p.lines(t, "graph_count_owned_marker_absent"), "source-ch"))
	want := []any{"ran", "skipped_interval", "skipped_interval", "ran"}
	for i := range want {
		if len(got) != len(want) || got[i] != want[i] {
			t.Fatalf("probe states = %v, want %v", got, want)
		}
	}
	if gaps := p.lines(t, "graph_below_source_lower_bound"); len(gaps) != 2 {
		t.Fatalf("want one confirmed lower-bound Warn per source at the second probe (2), got %d:\n%s", len(gaps), p.buffer.String())
	}
	for _, check := range p.checks {
		wantUnknownCheck(t, check)
	}
}

// A negative interval switches the reads off: the line says so and nothing is read.
func TestGraphCountCheckUnknownMarkerAbsentProbeDisabledReadsNothing(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 10, probeInterval: -1}
	p.run(t)
	if p.chSource.callsFor.Load() != 0 || p.teamsSource.callsFor.Load() != 0 || p.totalCalls.Load() != 0 {
		t.Fatalf("a disabled probe read: ch %d teams %d totals %d", p.chSource.callsFor.Load(), p.teamsSource.callsFor.Load(), p.totalCalls.Load())
	}
	for _, line := range p.lines(t, "graph_count_owned_marker_absent") {
		if line["probe"] != "disabled" || line["coverage"] != "none" {
			t.Fatalf("line = %v, want probe disabled and coverage none", line)
		}
	}
}

// A source count read refused with a memory-limit exception (code 241): the line
// is a Warn naming the code and limit class (never the text), the outcome stays
// unknown, the other source is still probed, and the refused read is not retried
// inside the interval.
func TestGraphCountCheckUnknownMarkerAbsentRefusedReadIsLoudAndNotRetried(t *testing.T) {
	t.Parallel()
	p := &prodShape{sourceCount: 100, ownedCount: 4, totalCount: 10, probeInterval: time.Hour, chErr: refusedRead{}}
	p.run(t)
	for _, check := range p.checks {
		wantUnknownCheck(t, check)
	}
	if strings.Contains(p.buffer.String(), "synthetic refusal text") {
		t.Fatalf("the exception text reached the log:\n%s", p.buffer.String())
	}
	ch := forSource(p.lines(t, "graph_count_owned_marker_absent"), "source-ch")
	if len(ch) != 2 {
		t.Fatalf("want a line per check (2), got %d", len(ch))
	}
	first := ch[0]
	for key, want := range map[string]any{"level": "WARN", "probe": "failed", "clickhouse_code": float64(241), "limit_class": "memory", "probe_failed_reads": float64(1), "failure_class": "unclassified"} {
		if first[key] != want {
			t.Fatalf("first line %s = %v, want %v; line %v", key, first[key], want, first)
		}
	}
	if ch[1]["probe"] != "skipped_interval" {
		t.Fatalf("second check probe = %v, want skipped_interval (no retry inside the interval)", ch[1]["probe"])
	}
	if got := p.chSource.callsFor.Load(); got != 1 {
		t.Fatalf("refused source read %d times, want exactly 1", got)
	}
	teams := forSource(p.lines(t, "graph_count_owned_marker_absent"), "source-teams")
	if len(teams) != 2 || teams[0]["probe"] != "ran" || teams[0]["basis"] != "total_kind_count" {
		t.Fatalf("the other source must still be probed: %v", teams)
	}
}
