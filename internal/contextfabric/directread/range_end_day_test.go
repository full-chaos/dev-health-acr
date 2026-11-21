package directread

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// rangeEndDayRead serves one team's 60 daily_health rows, newest 2026-09-27,
// through the read_facts reader under window and returns the served table.
func rangeEndDayRead(t *testing.T, window *RequestWindow) ServedTable {
	t.Helper()
	team := subject("team", "team:IDENTITY")
	client := &chaos9883Client{dailyRows: map[string]int{"IDENTITY": 60}}
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
		t.Fatalf("read: %v", err)
	}
	for _, fact := range response.Facts {
		if table, ok := fact.Tables["daily_health"]; ok {
			return table
		}
	}
	t.Fatalf("no daily_health table served: %+v", response.Coverage)
	return ServedTable{}
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
