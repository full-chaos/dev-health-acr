package devhealthfacts

import (
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestRollupWindowEndIsExclusiveForTheEvidenceWindowAndTheDefault(t *testing.T) {
	now := time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)
	today := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	start, end := today.AddDate(0, 0, -30), today
	evidence := &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end}
	for name, tc := range map[string]struct {
		window    rollupWindow
		wantStart time.Time
		wantEnd   time.Time
	}{
		"evidence_window":  {resolveRollupWindow(factTimeBound{}, evidence, now), start, end},
		"default_trailing": {resolveRollupWindow(factTimeBound{}, nil, now), today.Add(-metricsSeriesDefaultWindow), today},
	} {
		if !tc.window.start.Equal(tc.wantStart) || !tc.window.end.Equal(tc.wantEnd) {
			t.Errorf("%s: window %v..%v, want %v..%v", name, tc.window.start, tc.window.end, tc.wantStart, tc.wantEnd)
		}
		for label, expr := range map[string]string{
			"day":       tc.window.dayExpr("day"),
			"timestamp": tc.window.timestampExpr("merged_at"),
			"upper":     tc.window.upperExpr("resolved_at"),
		} {
			if !strings.Contains(expr, " < ") || strings.Contains(expr, " <= ") {
				t.Errorf("%s %s = %q, want the end exclusive", name, label, expr)
			}
		}
	}
}

func TestRollupWindowAsOfEndStaysInclusive(t *testing.T) {
	asOf := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	window := resolveRollupWindow(factTimeBound{active: true, end: asOf}, nil, asOf)
	for label, expr := range map[string]string{"day": window.dayExpr("day"), "timestamp": window.timestampExpr("merged_at"), "upper": window.upperExpr("resolved_at")} {
		if !strings.Contains(expr, " <= ") {
			t.Errorf("as_of %s = %q, want the as-of instant inclusive", label, expr)
		}
	}
}
