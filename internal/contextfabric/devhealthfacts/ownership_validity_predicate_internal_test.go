package devhealthfacts

import (
	"testing"
	"time"
)

func TestOwnershipValidityPredicateIsNeverTimeSlicedByValidFrom(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(60 * 24 * time.Hour)
	cases := []struct {
		name  string
		bound factTimeBound
		want  string
	}{
		{"unbounded read keeps currently-owned", factTimeBound{}, " AND valid_from <= now64(3) AND valid_to IS NULL"},
		{"range reads ownership not ended by the window start", factTimeBound{active: true, hasStart: true, start: start, end: end},
			" AND (valid_to IS NULL OR valid_to > {" + boundStartParam + ":DateTime64(6,'UTC')})"},
		{"point in time reads ownership not ended by its instant", factTimeBound{active: true, end: end},
			" AND (valid_to IS NULL OR valid_to > {" + boundEndParam + ":DateTime64(6,'UTC')})"},
	}
	for _, tc := range cases {
		if got := ownershipValidityPredicate(tc.bound); got != tc.want {
			t.Errorf("%s: predicate = %q, want %q", tc.name, got, tc.want)
		}
	}
}
