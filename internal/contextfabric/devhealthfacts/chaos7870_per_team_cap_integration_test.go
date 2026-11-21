package devhealthfacts_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The per-team daily cap runs on a real server: a team with more days than the
// cap keeps its newest days in order and says it lost rows, and a team with a
// short series next to it is served whole and reads available.
func TestFlowTeamDailySeriesCapsEachTeamOnItsOwnAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	const orgID = "org-flow-per-team-cap"
	seed := func(team string, days int) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id)
SELECT toDate('2026-08-31') - number, 'linear', 'scope-x', '`+team+`', toUInt32(number), toUInt32(1), toUInt32(1), 4.0, 8.0, 0.1, 2.0, toDateTime('2026-08-31 06:00:00'), '`+orgID+`' FROM numbers(`+strconv.Itoa(days)+`)`); err != nil {
			t.Fatalf("seed %s: %v", team, err)
		}
	}
	seed("team-a-long", 205)
	seed("team-b-short", 3)

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-a-long"), teamSubject("team-b-short")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(result.Facts))
	}
	for _, fact := range result.Facts {
		rows := fact.Fields["daily_flow"].Rows
		switch fact.Subject.CanonicalID {
		case "team:team-a-long":
			if len(rows) != contextfabric.MaxFactValueRows {
				t.Fatalf("team A daily rows = %d, want %d", len(rows), contextfabric.MaxFactValueRows)
			}
			// number = days before 2026-08-31, and the series is served newest first.
			for i, row := range rows {
				if got := row.Fields["items_started"].Integer; got == nil || *got != int64(i) {
					t.Fatalf("team A row %d items_started = %v, want %d: the kept rows must be the newest days in order", i, got, i)
				}
			}
			// 205 window days, 64 shown: the count is the team's days, not the rows a cap let through.
			if got := fact.Fields["daily_flow_omitted_count"].Integer; got == nil || *got != 141 {
				t.Fatalf("team A daily_flow_omitted_count = %v, want 141", got)
			}
			if fact.SourceState != contextfabric.SourceTruncated {
				t.Fatalf("team A state = %q, want truncated", fact.SourceState)
			}
		case "team:team-b-short":
			if len(rows) != 3 {
				t.Fatalf("team B daily rows = %d, want 3: a long neighbour must not take its rows", len(rows))
			}
			if _, cut := fact.Fields["daily_flow_omitted_count"]; cut || fact.SourceState != contextfabric.SourceAvailable {
				t.Fatalf("team B cut=%v state=%q, want whole and available", cut, fact.SourceState)
			}
		}
	}
	if !result.Truncated || !result.TruncationSubjectLocal {
		t.Fatalf("Truncated=%v TruncationSubjectLocal=%v, want both true", result.Truncated, result.TruncationSubjectLocal)
	}
}
