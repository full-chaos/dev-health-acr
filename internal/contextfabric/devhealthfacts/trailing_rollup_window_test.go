package devhealthfacts

import (
	"strings"
	"testing"
	"time"
)

func TestRollupWindowOfATrailingRangeEndsExclusiveAndAnExplicitRangeStaysInclusive(t *testing.T) {
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	for _, exclusive := range []bool{true, false} {
		window := resolveRollupWindow(factTimeBound{active: true, hasStart: true, start: start, end: end, endExclusive: exclusive}, nil, end)
		for label, expr := range map[string]string{"day": window.dayExpr("day"), "timestamp": window.timestampExpr("merged_at"), "upper": window.upperExpr("resolved_at")} {
			hasLessEqual := strings.Contains(expr, " <= ")
			if exclusive && (hasLessEqual || !strings.Contains(expr, " < ")) {
				t.Errorf("trailing %s = %q, want the end exclusive", label, expr)
			}
			if !exclusive && !hasLessEqual {
				t.Errorf("explicit range %s = %q, want the end inclusive", label, expr)
			}
		}
	}
}

func TestFreshnessAsOfDateOfAnExclusiveEndIsTheLastDayInside(t *testing.T) {
	end := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	exclusive := freshnessAsOfDateSQL(factTimeBound{active: true, hasStart: true, end: end, endExclusive: true})
	if !strings.Contains(exclusive, "INTERVAL 1 MICROSECOND") {
		t.Errorf("exclusive end as-of date = %q, want the end minus one microsecond", exclusive)
	}
	inclusive := freshnessAsOfDateSQL(factTimeBound{active: true, hasStart: true, end: end})
	if strings.Contains(inclusive, "MICROSECOND") {
		t.Errorf("explicit range as-of date = %q, want the unchanged end", inclusive)
	}
}
