package contextfabric_test

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// recheckReadStep is what one graph read costs on the scenario's clock.
const recheckReadStep = 7 * time.Millisecond

// TestTheReuseLineStatesTheRecheckItRan (CHAOS-12511): every line of an attempt
// whose recheck reached the graph states how long it took and how many subjects
// it covered, hit or miss, whatever the outcome. The measurement is exact: the
// clock moves only inside a graph read, so recheck_ms is reads * step, which a
// constant or a clock started after the first read cannot equal.
func TestTheReuseLineStatesTheRecheckItRan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario contextfabric.ReuseLineScenario
		want     map[string]any
		reads    int
		subjects int
	}{
		{contextfabric.ReuseLineHit, map[string]any{"outcome": "hit"}, 4, 54},
		{contextfabric.ReuseLineNoSubject, map[string]any{"outcome": "hit"}, 1, 0},
		{contextfabric.ReuseLineDegraded, map[string]any{"outcome": "hit_degraded"}, 4, 54},
		{contextfabric.ReuseLineNotCommitted, map[string]any{"outcome": "miss_authorization", "reason": "subject_not_committed"}, 2, 54},
		{contextfabric.ReuseLineRecheckFailed, map[string]any{"outcome": "miss_authorization", "reason": "subject_recheck_failed"}, 2, 54},
		{contextfabric.ReuseLineContainment, map[string]any{"outcome": "miss_evidence_containment"}, 4, 54},
		{contextfabric.ReuseLineUnavailable, map[string]any{"outcome": "miss_recheck_unavailable"}, 4, 54},
		{contextfabric.ReuseLineNarrowing, map[string]any{"outcome": "miss_project_narrowing_unverified"}, 4, 54},
		{contextfabric.ReuseLineDegradeInvalid, map[string]any{"outcome": "miss_degrade_invalid"}, 4, 54},
		{contextfabric.ReuseLineRecheckDeadline, map[string]any{"outcome": "miss_recheck_deadline"}, 4, 54},
	} {
		t.Run(string(tc.scenario), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			run := contextfabric.RunReuseLineForTest(ctx, t, sink, tc.scenario, recheckReadStep)
			if run.Reads != tc.reads {
				t.Fatalf("graph reads = %d, want %d", run.Reads, tc.reads)
			}
			line := certifyRequestLine(t, buf.Bytes(), eventspec.AnswerReuseOutcome, tc.want)
			wantMS := float64(int64(tc.reads) * recheckReadStep.Milliseconds())
			if got, ok := line["recheck_ms"]; !ok || got != wantMS {
				t.Fatalf("recheck_ms = %v (written %t), want %v: %d reads of %s", got, ok, wantMS, tc.reads, recheckReadStep)
			}
			if got, ok := line["recheck_subjects"]; !ok || got != float64(tc.subjects) {
				t.Fatalf("recheck_subjects = %v (written %t), want %d", got, ok, tc.subjects)
			}
		})
	}
}

// TestTheReuseLineOmitsARecheckThatDidNotRun (CHAOS-12511): an attempt that
// reached no graph read has nothing to measure. The gate withholds the answer
// first, or the recheck refuses it before its first read.
func TestTheReuseLineOmitsARecheckThatDidNotRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario contextfabric.ReuseLineScenario
		want     map[string]any
	}{
		{contextfabric.ReuseLineGateWithheld, map[string]any{"outcome": "miss_authorization", "reason": "gate_denied"}},
		{contextfabric.ReuseLineCapExceeded, map[string]any{"outcome": "miss_authorization", "reason": "subject_cap_exceeded", "subject_count": 1801}},
		{contextfabric.ReuseLineGraphNotProjected, map[string]any{"outcome": "graph_not_projected"}},
	} {
		t.Run(string(tc.scenario), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			run := contextfabric.RunReuseLineForTest(ctx, t, sink, tc.scenario, recheckReadStep)
			if run.Reads != 0 {
				t.Fatalf("graph reads = %d, want 0", run.Reads)
			}
			line := certifyRequestLine(t, buf.Bytes(), eventspec.AnswerReuseOutcome, tc.want)
			for _, key := range []string{"recheck_ms", "recheck_subjects"} {
				if got, ok := line[key]; ok {
					t.Fatalf("%s = %v is written on a line where no recheck ran", key, got)
				}
			}
		})
	}
}

// TestTheReuseSinkStatesAZeroRecheck (CHAOS-12511): a recheck that ran is stated
// even when both numbers are zero. A zero is a measurement, and an absent field
// is not.
func TestTheReuseSinkStatesAZeroRecheck(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordAnswerReuse(ctx, principalOrg1, contextfabric.AnswerReuseHit, contextfabric.OwnershipRoutingVersion, contextfabric.AnswerReuseRefusal{Recheck: contextfabric.AnswerReuseRecheck{Ran: true}})
	line := certifyRequestLine(t, buf.Bytes(), eventspec.AnswerReuseOutcome, map[string]any{"org_id": "org_1", "outcome": "hit", "recheck_ms": 0, "recheck_subjects": 0})
	for _, key := range []string{"recheck_ms", "recheck_subjects"} {
		if got, ok := line[key]; !ok || got != float64(0) {
			t.Fatalf("%s = %v (written %t), want a written 0", key, got, ok)
		}
	}
}
