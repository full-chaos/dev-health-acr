package directread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// chaos9883Scanner replays canned rows into the destination types the health
// queries scan.
type chaos9883Scanner struct {
	rows [][]any
	next int
}

func (s *chaos9883Scanner) Next() bool { return s.next < len(s.rows) }

func (s *chaos9883Scanner) Scan(dest ...any) error {
	row := s.rows[s.next]
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[index].(string)
		case *uint8:
			*value = row[index].(uint8)
		case *float64:
			*value = row[index].(float64)
		default:
			return errors.New("chaos9883: unsupported scan destination")
		}
	}
	s.next++
	return nil
}

func (s *chaos9883Scanner) Err() error   { return nil }
func (s *chaos9883Scanner) Close() error { return nil }

// chaos9883Client answers the team health scalar read (one latest row per
// team) and the team daily series read (n rows per team); every other query
// the provider runs gets no rows.
type chaos9883Client struct{ dailyRows map[string]int }

func (c *chaos9883Client) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	switch {
	case strings.Contains(statement, "PARTITION BY scope_id, day ORDER BY computed_at DESC"):
		var rows [][]any
		for team, n := range c.dailyRows {
			for i := 0; i < n; i++ {
				day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
				rows = append(rows, []any{team, day, "high", uint8(1), 0.61})
			}
		}
		return &chaos9883Scanner{rows: rows}, nil
	case strings.Contains(statement, "ifNull(churn_norm"):
		var rows [][]any
		for team := range c.dailyRows {
			rows = append(rows, []any{
				team, "elevated", uint8(1), 0.62, "2026-09-27 00:00:00", "2026-09-27", uint8(1), uint8(1),
				uint8(1), 0.3, 0.4, uint8(1), 0.2, 0.3, uint8(1), 0.5, 0.2, uint8(1), 0.1, 0.1,
			})
		}
		return &chaos9883Scanner{rows: rows}, nil
	}
	return &chaos9883Scanner{}, nil
}

func chaos9883Read(t *testing.T, tables string) (FactsResponse, []contextfabric.SubjectRef) {
	t.Helper()
	teams := []contextfabric.SubjectRef{
		subject("team", "team:IDENTITY"), subject("team", "team:CHECKOUT"), subject("team", "team:DATA"),
	}
	client := &chaos9883Client{dailyRows: map[string]int{
		"IDENTITY": 3,
		"CHECKOUT": contextfabric.MaxFactValueRows + 10,
		"DATA":     5,
	}}
	var health contextfabric.FactProvider
	for _, candidate := range devhealthfacts.NewProviders(client) {
		if candidate.Capability().Kind == contextfabric.FactHealth {
			health = candidate
		}
	}
	reader := newTestFactsReader(t, teamGraph(teams...), health)
	request := FactsRequest{Kinds: []string{"health"}, Tables: tables}
	for _, team := range teams {
		request.Subjects = append(request.Subjects, RequestSubject{Kind: "team", CanonicalID: team.CanonicalID})
	}
	response, err := reader.Read(requestContext(), unrestrictedA(), request)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return response, teams
}

// CHAOS-9883: one team losing daily_health rows to the row cap must mark
// only that team truncated, and only when the daily table is served.
func TestChaos9883HealthDailyRowCapTruncatesOnlyTheSubjectThatLostRows(t *testing.T) {
	t.Run("tables include", func(t *testing.T) {
		response, teams := chaos9883Read(t, "include")
		want := map[string]string{"team:IDENTITY": OutcomeFactServed, "team:CHECKOUT": OutcomeTruncated, "team:DATA": OutcomeFactServed}
		for _, team := range teams {
			if got := coverageOf(t, response, "health", team).Outcome; got != want[team.CanonicalID] {
				t.Errorf("%s outcome = %q, want %q", team.CanonicalID, got, want[team.CanonicalID])
			}
		}
		for _, fact := range response.Facts {
			if fact.Provenance.SourceState != "available" && fact.Subject.CanonicalID != "team:CHECKOUT" {
				t.Errorf("%s provenance source_state = %q, want available", fact.Subject.CanonicalID, fact.Provenance.SourceState)
			}
		}
	})
	t.Run("tables omit", func(t *testing.T) {
		response, teams := chaos9883Read(t, "omit")
		for _, team := range teams {
			if got := coverageOf(t, response, "health", team).Outcome; got != OutcomeFactServed {
				t.Errorf("%s outcome = %q, want %q (daily table not served, nothing lost)", team.CanonicalID, got, OutcomeFactServed)
			}
		}
		if response.Status != StatusComplete {
			t.Errorf("status = %q, want complete: %s", response.Status, fmt.Sprint(response.Coverage))
		}
	})
}
