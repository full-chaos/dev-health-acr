package directread

import (
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// rangeEndDayRead serves one team's 60 daily_health rows, newest 2026-09-27,
// through the read_facts reader under window and returns the served table.
func rangeEndDayRead(t *testing.T, window *RequestWindow) ServedTable {
	t.Helper()
	table, err := rangeEndDayReadRows(t, window, 60)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return table
}

func rangeEndDayReadRows(t *testing.T, window *RequestWindow, rows int) (ServedTable, error) {
	t.Helper()
	team := subject("team", "team:IDENTITY")
	client := &chaos9883Client{dailyRows: map[string]int{"IDENTITY": rows}}
	var health contextfabric.FactProvider
	for _, candidate := range devhealthfacts.NewProviders(client) {
		if candidate.Capability().Kind == contextfabric.FactHealth {
			health = candidate
		}
	}
	reader := newTestFactsReader(t, teamGraph(team), health)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"health"}, Tables: "include", Window: window,
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: team.CanonicalID}},
	})
	if err != nil {
		return ServedTable{}, err
	}
	for _, fact := range response.Facts {
		if table, ok := fact.Tables["daily_health"]; ok {
			return table, nil
		}
	}
	t.Fatalf("no daily_health table served: %+v", response.Coverage)
	return ServedTable{}, nil
}

// An explicit range keeps its end day, as the providers' day predicate does:
// a range that ends at a UTC midnight serves that day's row and expects it. A
// trailing window ends before today, and a range that ends later in the day
// already kept the day.
func TestRangeEndingAtMidnightServesItsEndDay(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	midnight, oneAM := day(27), day(27).Add(time.Hour)
	for _, tc := range []struct {
		name               string
		window             *RequestWindow
		rows, expected     int
		newest             string
		expectPointsServed bool
	}{
		{"range ending at midnight", &RequestWindow{Mode: WindowRange, Start: &start, End: &midnight}, 60, 60, "2026-09-27", true},
		{"range ending at 01:00", &RequestWindow{Mode: WindowRange, Start: &start, End: &oneAM}, 60, 60, "2026-09-27", true},
		{"trailing 60 days", &RequestWindow{Mode: WindowTrailing, Days: 60}, 60, 60, "2026-09-27", true},
		{"as_of", &RequestWindow{Mode: WindowAsOf, AsOf: &midnight}, 60, 0, "2026-09-27", false},
		{"current", nil, 60, 0, "2026-09-27", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := rangeEndDayRead(t, tc.window)
			if table.RowsReturned != tc.rows || table.RowsOmitted != 0 || table.NewestReturned != tc.newest {
				t.Fatalf("rows=%d omitted=%d newest=%q, want %d, 0, %q", table.RowsReturned, table.RowsOmitted, table.NewestReturned, tc.rows, tc.newest)
			}
			if !tc.expectPointsServed {
				if table.ExpectedPoints != nil {
					t.Fatalf("expected_points = %d, want none outside a range", *table.ExpectedPoints)
				}
				return
			}
			if table.ExpectedPoints == nil || *table.ExpectedPoints != tc.expected || *table.ReturnedPoints != tc.expected || len(table.MissingInstants) != 0 {
				t.Fatalf("expected=%v returned=%v missing=%v, want %d, %d, none", table.ExpectedPoints, table.ReturnedPoints, table.MissingInstants, tc.expected, tc.expected)
			}
		})
	}
}

// The maximum check counts duration, so a maximum-length explicit range can
// serve one daily point more than the kind's maximum: every UTC day the range
// touches is served. These cells pin the served day count through the real
// read path; health's maximum is 60 days.
func TestMaximumLengthRangeServesEveryUTCDayItTouches(t *testing.T) {
	const max = 60
	for _, tc := range []struct {
		name       string
		start, end time.Time
	}{
		{"max days, both ends at midnight", time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)},
		{"max days, both ends at noon", time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC), time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.end.Sub(tc.start); got != max*24*time.Hour {
				t.Fatalf("fixture span %v, want exactly %d days", got, max)
			}
			start, end := tc.start, tc.end
			table, err := rangeEndDayReadRows(t, &RequestWindow{Mode: WindowRange, Start: &start, End: &end}, max+5)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if table.RowsReturned != max+1 || table.OldestReturned != "2026-07-29" || table.NewestReturned != "2026-09-27" {
				t.Fatalf("rows=%d oldest=%q newest=%q, want %d, 2026-07-29, 2026-09-27", table.RowsReturned, table.OldestReturned, table.NewestReturned, max+1)
			}
			if table.ExpectedPoints == nil || *table.ExpectedPoints != max+1 || *table.ReturnedPoints != max+1 || len(table.MissingInstants) != 0 {
				t.Fatalf("expected=%v returned=%v missing=%v, want %d, %d, none", table.ExpectedPoints, table.ReturnedPoints, table.MissingInstants, max+1, max+1)
			}
		})
	}
}

func TestRangeOneSecondOverTheMaximumIsRefused(t *testing.T) {
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	end := start.Add(60*24*time.Hour + time.Second)
	_, err := rangeEndDayReadRows(t, &RequestWindow{Mode: WindowRange, Start: &start, End: &end}, 65)
	var refusal *RequestError
	if !errors.As(err, &refusal) || refusal.Reason != "window_beyond_kind_max" || refusal.MaxDays != 60 {
		t.Fatalf("err %v, want window_beyond_kind_max with max_days 60", err)
	}
}
