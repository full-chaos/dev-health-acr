package devhealthfacts

import "testing"

func TestParseCompoundingRiskRationaleProducerSentences(t *testing.T) {
	tail := "The inputs that were present are compounding above their tuned thresholds."
	full := "Churn, complexity trend, ownership concentration, and review latency are compounding above their tuned thresholds."
	cases := []struct {
		name, in, sev string
		score         float64
		ok            bool
	}{
		{"full coverage", "Compounding Risk score is 0.636 (severity: elevated). " + full, "elevated", 0.636, true},
		{"partial coverage", "Compounding Risk score is 0.636 (severity: elevated), computed from 80% of its inputs (churn, complexity trend, review latency). " + tail, "elevated", 0.636, true},
		{"partial coverage high", "Compounding Risk score is 0.994 (severity: high), computed from 50% of its inputs (churn, review latency). " + tail, "high", 0.994, true},
		{"severity only", "Compounding Risk severity is high.", "", 0, false},
		{"hotspot proxy", "Hotspot complexity delta is 0.2 (threshold: 0.1) and churn-complexity overlap is 0.3 (threshold: 0.2), indicating growing technical debt in the most actively-changed files.", "", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCompoundingRiskRationale(c.in)
		if ok != c.ok || got.severity != c.sev || got.score != c.score {
			t.Errorf("%s: got (%v,%v) want (%v,%v,%v)", c.name, got, ok, c.score, c.sev, c.ok)
		}
	}
}
