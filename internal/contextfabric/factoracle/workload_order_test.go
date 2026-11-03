package factoracle

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"
)

// CHAOS-12808: a pinned outcome holds each finding's detail text exactly, so
// a value the oracle derives must not depend on Go's random map order. The
// workload pair adds the ops forecast rows of one day in float64; over the
// rows of the 2026-11-03 capture, two runs gave 26.839993339993338 and
// 26.83999333999334 for the same day, and the recorded run failed on the
// detail text. Here the sum is order-sensitive by construction (1e16 + 1 is
// 1e16 in float64), and the oracle is run many times on the same input.
func TestTheWorkloadDayTotalDoesNotDependOnMapOrder(t *testing.T) {
	values := []string{"1e16", "1", "1", "1", "-1e16", "0.1", "0.2", "0.3"}
	latest := map[string]forecastNode{}
	for i, v := range values {
		id := fmt.Sprintf("forecast-%02d", i)
		latest[id] = forecastNode{
			scope: fmt.Sprintf("scope-%02d", i), id: id, day: "2026-10-12",
			computed: Leaf{T: LeafString, V: "2026-10-12 01:00:00"},
			row: map[string]any{
				"backlogSize": json.Number("1"), "throughputMean": json.Number(v), "throughputStddev": json.Number(v),
			},
		}
	}
	facts := []ServedFact{{Kind: "workload", Tables: map[string]ServedTable{"daily_workload": {
		Columns: []string{"day", "backlog_size", "throughput_mean", "throughput_stddev"},
		Rows:    [][]any{{"2026-10-12", json.Number("8"), json.Number("123.456"), json.Number("789.012")}},
	}}}}
	// Per compared path, the distinct detail texts over the runs. The work
	// scope findings of this hand-made fact are not what is measured here.
	details := map[string]map[string]bool{}
	for range 200 {
		rr := &RootReport{}
		compareWorkloadTeam(rr, latest, facts)
		for _, f := range rr.Findings {
			if f.Path != nodePath+"throughputMean" && f.Path != nodePath+"throughputStddev" {
				continue
			}
			if details[f.Path] == nil {
				details[f.Path] = map[string]bool{}
			}
			details[f.Path][f.Detail] = true
		}
	}
	if len(details) != 2 {
		t.Fatalf("findings on %d of the 2 float paths, want both (the acr values differ from every ops total): %v", len(details), details)
	}
	for path, seen := range details {
		if len(seen) != 1 {
			got := make([]string, 0, len(seen))
			for d := range seen {
				got = append(got, d)
			}
			sort.Strings(got)
			t.Errorf("%s: 200 runs on one input gave %d detail texts, want 1: %v", path, len(got), got)
		}
	}
}
