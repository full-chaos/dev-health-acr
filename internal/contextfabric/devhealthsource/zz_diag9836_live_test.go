package devhealthsource_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// TEMPORARY proof (not for merge): the merged slope rule on medians, repeated
// on fresh databases; it logs every repetition and fails on purpose so the log
// is printed.
func TestDiag9836SlopeRuleRepeated(t *testing.T) {
	var b strings.Builder
	trips := 0
	for rep := 0; rep < 10; rep++ {
		med := map[int]float64{}
		low := map[int]float64{}
		for i, versions := range []int{1000, 2000} {
			m := freshDatabaseFixture(t, fmt.Sprintf("95460000-0000-4000-8000-000000009%d%02d", i, rep), "merged", versions)
			capped := contextpacket.NewMemoryCappedQueryClient(m.query, prodMaxMemoryUsage)
			p := peakOf(t, measureProjectTeamsFragments(t, m.f, m.query, capped, "diag", map[string]bool{"paged_statement": true}, slopeRuns), "paged_statement")
			med[versions], low[versions] = p.medianMiB, p.mib
		}
		small, large := med[1000], med[2000]
		trip := large > 1.15*small && large > small+2
		oldTrip := low[2000] > 1.15*low[1000] && low[2000] > low[1000]+2
		if trip {
			trips++
		}
		fmt.Fprintf(&b, "  rep=%d median 1000=%.2f 2000=%.2f trip=%v | lowest 1000=%.2f 2000=%.2f old_rule_trip=%v\n", rep, small, large, trip, low[1000], low[2000], oldTrip)
	}
	for rep := 0; rep < 2; rep++ {
		med := map[int]float64{}
		for i, versions := range []int{1000, 2000} {
			m := freshDatabaseFixture(t, fmt.Sprintf("95460000-0000-4000-8000-000000008%d%02d", i, rep), "merged", versions)
			capped := contextpacket.NewMemoryCappedQueryClient(m.query, prodMaxMemoryUsage)
			p := peakOf(t, measureProjectTeamsFragments(t, m.f, m.query, capped, "diag-legacy", map[string]bool{"legacy_paged_statement": true}, slopeRuns), "legacy_paged_statement")
			med[versions] = p.medianMiB
		}
		small, large := med[1000], med[2000]
		fmt.Fprintf(&b, "  LEGACY rep=%d median 1000=%.2f 2000=%.2f trip=%v\n", rep, small, large, large > 1.15*small && large > small+2)
	}
	t.Logf("diag9836 slope repeated (median of %d warm reads):\n%s", slopeRuns, b.String())
	t.Errorf("diag9836: intentional failure so the log is printed; trips=%d", trips)
}
