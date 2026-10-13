package devhealthfacts

import (
	"testing"
	"time"
)

func TestBlockerOldestAgeIsWholeDaysWithABasisOrAnAbsenceReason(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	start12 := now.Add(-12*24*time.Hour - 3*time.Hour).Unix()
	cases := []struct {
		name       string
		age        blockerTeamAge
		wantOK     bool
		wantDays   int64
		wantBasis  string
		wantReason string
	}{
		{"every open edge has the provider time", blockerTeamAge{openEdges: 2, edgesWithStart: 2, edgesWithExactStart: 2, oldestStartUnix: start12}, true, 12, "exact", ""},
		{"a start from the first sync is a lower bound", blockerTeamAge{openEdges: 1, edgesWithStart: 1, edgesWithExactStart: 0, oldestStartUnix: start12}, true, 12, "lower_bound", ""},
		{"an open edge with no start keeps the known age as a lower bound", blockerTeamAge{openEdges: 2, edgesWithStart: 1, edgesWithExactStart: 1, oldestStartUnix: start12}, true, 12, "lower_bound", ""},
		{"no open edge has a start", blockerTeamAge{openEdges: 2}, false, 0, "", "no_relation_start"},
		{"no open edge", blockerTeamAge{}, false, 0, "", "none_open"},
		{"a start in the future is zero days, not negative", blockerTeamAge{openEdges: 1, edgesWithStart: 1, edgesWithExactStart: 1, oldestStartUnix: now.Add(time.Hour).Unix()}, true, 0, "exact", ""},
		{"an edge younger than a day is zero days", blockerTeamAge{openEdges: 1, edgesWithStart: 1, edgesWithExactStart: 1, oldestStartUnix: now.Add(-23 * time.Hour).Unix()}, true, 0, "exact", ""},
	}
	for _, c := range cases {
		days, basis, reason, ok := blockerOldestAge(c.age, now)
		if ok != c.wantOK || days != c.wantDays || basis != c.wantBasis || reason != c.wantReason {
			t.Errorf("%s: got (%d, %q, %q, %v), want (%d, %q, %q, %v)", c.name, days, basis, reason, ok, c.wantDays, c.wantBasis, c.wantReason, c.wantOK)
		}
	}
}
