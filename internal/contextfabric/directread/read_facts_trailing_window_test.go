package directread

import (
	"strings"
	"testing"
	"time"
)

func TestTrailingWindowEndsAtTodayMidnightUTC(t *testing.T) {
	today := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{time.Second, 12 * time.Hour, 24*time.Hour - time.Second} {
		now := today.Add(offset)
		reader := &FactsReader{now: func() time.Time { return now }}
		for _, days := range []int{1, 7, 30, 365} {
			window := &RequestWindow{Mode: WindowTrailing, Days: days}
			_, effective, err := reader.window(window, 365)
			if err != nil {
				t.Fatalf("offset %v days %d: %v", offset, days, err)
			}
			wantStart := today.AddDate(0, 0, -days)
			if effective.Start == nil || effective.End == nil || !effective.Start.Equal(wantStart) || !effective.End.Equal(today) {
				t.Errorf("offset %v days %d: [%v, %v), want [%v, %v)", offset, days, effective.Start, effective.End, wantStart, today)
			}
			if !strings.Contains(effective.Note, "completed UTC days") || !strings.Contains(effective.Note, "today") {
				t.Errorf("offset %v days %d: note %q does not name the rule", offset, days, effective.Note)
			}
		}
	}
}

func TestTrailingWindowStillRefusesBeyondMaxAndKeepsRangeAndAsOf(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC)
	reader := &FactsReader{now: func() time.Time { return now }}
	if _, _, err := reader.window(&RequestWindow{Mode: WindowTrailing, Days: 366}, 365); err == nil {
		t.Fatal("366 days beyond max 365 was accepted")
	}
	start, end := time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC), time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)
	_, effective, err := reader.window(&RequestWindow{Mode: WindowRange, Start: &start, End: &end}, 365)
	if err != nil || !effective.Start.Equal(start) || !effective.End.Equal(end) {
		t.Fatalf("range changed: %+v, %v", effective, err)
	}
	asOf := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	_, effective, err = reader.window(&RequestWindow{Mode: WindowAsOf, AsOf: &asOf}, 365)
	if err != nil || effective.AsOf == nil || !effective.AsOf.Equal(asOf) {
		t.Fatalf("as_of changed: %+v, %v", effective, err)
	}
}
