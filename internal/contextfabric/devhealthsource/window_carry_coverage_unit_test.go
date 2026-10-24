package devhealthsource_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// The coverage counters are the proof that the parity oracle still crosses
// ties and arms. They run without ClickHouse, on synthetic trial results.
func TestCarryCoverageFailsWhenATieOrAnArmIsNeverExercised(t *testing.T) {
	arms := []string{"transition", "work_item_column", "column_superseded"}
	full := devhealthsource.CarryTrialForTest{
		Equal: true, NonEmpty: true, TieKept: 2, TieDropped: 1,
		Arms: map[string]int{"transition": 3, "work_item_column": 2, "column_superseded": 1},
	}
	build := func(mutate func(*devhealthsource.CarryTrialForTest)) carryCoverage {
		var cov carryCoverage
		for i := 0; i < 8; i++ {
			result := full
			result.Arms = map[string]int{}
			for arm, n := range full.Arms {
				result.Arms[arm] = n
			}
			if mutate != nil {
				mutate(&result)
			}
			cov.add(result)
		}
		return cov
	}
	for _, tc := range []struct {
		name   string
		mutate func(*devhealthsource.CarryTrialForTest)
		want   string
	}{
		{"complete coverage has no gap", nil, ""},
		{"no tied row kept", func(r *devhealthsource.CarryTrialForTest) { r.TieKept = 0 }, "kept a row tied"},
		{"no tied row dropped", func(r *devhealthsource.CarryTrialForTest) { r.TieDropped = 0 }, "dropped a row tied"},
		{"transition arm never read", func(r *devhealthsource.CarryTrialForTest) { delete(r.Arms, "transition") }, "arm transition"},
		{"work_item_column arm never read", func(r *devhealthsource.CarryTrialForTest) { r.Arms["work_item_column"] = 0 }, "arm work_item_column"},
		{"column_superseded arm never read", func(r *devhealthsource.CarryTrialForTest) { delete(r.Arms, "column_superseded") }, "arm column_superseded"},
		{"empty pages only", func(r *devhealthsource.CarryTrialForTest) { r.NonEmpty = false }, "non-empty page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gaps := build(tc.mutate).gaps(arms)
			if tc.want == "" {
				if len(gaps) != 0 {
					t.Fatalf("gaps = %v, want none", gaps)
				}
				return
			}
			if len(gaps) == 0 || !strings.Contains(strings.Join(gaps, "; "), tc.want) {
				t.Fatalf("gaps = %v, want one naming %q", gaps, tc.want)
			}
		})
	}
	t.Run("skipped trials count for nothing", func(t *testing.T) {
		var cov carryCoverage
		for i := 0; i < 20; i++ {
			cov.add(devhealthsource.CarryTrialForTest{Skipped: true, Equal: true, NonEmpty: true, TieKept: 5, TieDropped: 5, Arms: map[string]int{"transition": 1}})
		}
		gaps := cov.gaps([]string{"transition"})
		if !strings.Contains(strings.Join(gaps, "; "), "complete table") || !strings.Contains(strings.Join(gaps, "; "), "kept a row tied") {
			t.Fatalf("gaps = %v: twenty skipped trials must count for nothing", gaps)
		}
	})
	t.Run("a producer without arms needs none", func(t *testing.T) {
		if gaps := build(nil).gaps(nil); len(gaps) != 0 {
			t.Fatalf("gaps = %v, want none", gaps)
		}
	})
}
