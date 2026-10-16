package devhealthfacts

import "testing"

func TestParseCompoundingRiskRationaleProducerSentences(t *testing.T) {
	tail := "The inputs that were present are compounding above their tuned thresholds."
	full := "Churn, complexity trend, ownership concentration, and review latency are compounding above their tuned thresholds."
	cases := []struct {
		name, in, sev, day string
		score              float64
		ok                 bool
	}{
		{"full coverage", "Compounding Risk score is 0.636 (severity: elevated). " + full, "elevated", "", 0.636, true},
		{"partial coverage", "Compounding Risk score is 0.636 (severity: elevated), computed from 80% of its inputs (churn, complexity trend, review latency). " + tail, "elevated", "", 0.636, true},
		{"partial coverage high", "Compounding Risk score is 0.994 (severity: high), computed from 50% of its inputs (churn, review latency). " + tail, "high", "", 0.994, true},
		// The exact sentences Ops writes since the as-of day was named: the day
		// follows the score sentence (and its coverage clause), before the drivers.
		{"day, full coverage", "Compounding Risk score is 0.739 (severity: high). Score as of 2026-10-14. " + full, "high", "2026-10-14", 0.739, true},
		{"day, partial coverage", "Compounding Risk score is 0.739 (severity: high), computed from 50% of its inputs (churn, review latency). Score as of 2026-10-14. Churn is compounding above its tuned threshold.", "high", "2026-10-14", 0.739, true},
		{"day, no drivers", "Compounding Risk score is 0.739 (severity: high). Score as of 2026-10-14.", "high", "2026-10-14", 0.739, true},
		{"malformed day is not a day", "Compounding Risk score is 0.739 (severity: high). Score as of yesterday. " + full, "high", "", 0.739, true},
		{"severity only", "Compounding Risk severity is high.", "", "", 0, false},
		{"hotspot proxy", "Hotspot complexity delta is 0.2 (threshold: 0.1) and churn-complexity overlap is 0.3 (threshold: 0.2), indicating growing technical debt in the most actively-changed files.", "", "", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCompoundingRiskRationale(c.in)
		if ok != c.ok || got.severity != c.sev || got.score != c.score || got.day != c.day {
			t.Errorf("%s: got (%v,%v) want (%v,%v,%q,%v)", c.name, got, ok, c.score, c.sev, c.day, c.ok)
		}
	}
}
