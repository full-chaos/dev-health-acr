package contextfabric

import (
	"testing"
	"time"
)

func TestDailyWindowEndKeepsTheEndDayOfAnExplicitRangeOnly(t *testing.T) {
	midnight := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	nextDay := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		end          time.Time
		endExclusive bool
		want         time.Time
	}{
		{"range ending at midnight", midnight, false, nextDay},
		{"range ending at 01:00", midnight.Add(time.Hour), false, nextDay},
		{"range ending one microsecond before midnight", nextDay.Add(-time.Microsecond), false, nextDay},
		{"range ending at midnight in another zone", midnight.In(time.FixedZone("UTC-5", -5*3600)), false, nextDay},
		{"trailing window", midnight, true, midnight},
	} {
		if got := DailyWindowEnd(tc.end, tc.endExclusive); !got.Equal(tc.want) {
			t.Errorf("%s: DailyWindowEnd = %s, want %s", tc.name, got, tc.want)
		}
	}
}
