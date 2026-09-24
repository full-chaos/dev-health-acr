package devhealthfacts

import (
	"strings"
	"testing"
	"time"
)

// CHAOS-6594: the mix statement names work_unit_investments exactly once, for
// one and for two windows. A second textual reference is a second scan.
func TestRepoMixStatementReadsWorkUnitInvestmentsOnce(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	current := factTimeBound{active: true, hasStart: true, start: start, end: end}
	prior := factTimeBound{active: true, hasStart: true, start: start.Add(-end.Sub(start)), end: start}
	for name, bounds := range map[string][]factTimeBound{
		"current axis":        {{}},
		"range":               {current},
		"range with prior":    {current, prior},
		"point in time":       {{active: true, end: end}},
		"point in time+prior": {{active: true, end: end}, prior},
	} {
		statement := repoMixStatement(bounds)
		if got := strings.Count(statement, "work_unit_investments"); got != 1 {
			t.Errorf("%s: statement references work_unit_investments %d times, want 1", name, got)
		}
		if got := strings.Count(statement, "FROM repos"); got != 1 {
			t.Errorf("%s: statement references repos %d times, want 1", name, got)
		}
		if strings.Contains(statement, "WITH ") {
			t.Errorf("%s: statement uses a CTE; ClickHouse inlines it at every reference", name)
		}
	}
}
