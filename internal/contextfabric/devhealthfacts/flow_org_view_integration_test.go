package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Seeds one (provider, scope, day) key under all three row views, plus a newer
// legacy zero row and a legacy-only older scope, then reads the team flow fact.
func TestFlowProviderTeamReadsOrgTotalsAndScopeBreakdownAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-flow-row-views"
	const teamID = "team-flow-row-views"
	seed := func(rowView, provider, scope string, day time.Time, computedAt time.Time, started, completed uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id, row_view) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			day.Format("2006-01-02"), provider, scope, teamID, started, completed, uint32(1), float64(4), float64(8), float64(0.1), float64(2), computedAt, orgID, rowView); err != nil {
			t.Fatalf("seed(%q,%s): %v", rowView, scope, err)
		}
	}
	day := date(2026, 8, 12)
	seed("", "linear", "scope-x", day, ts(2026, 8, 12, 6, 0, 0), 100, 100)
	seed("org", "linear", "scope-x", day, ts(2026, 8, 12, 7, 0, 0), 5, 3)
	seed("scope", "linear", "scope-x", day, ts(2026, 8, 12, 7, 0, 0), 9, 7)
	seed("", "linear", "scope-x", day, ts(2026, 8, 12, 8, 0, 0), 0, 0)
	seed("", "github", "scope-legacy", day, ts(2026, 8, 12, 6, 0, 0), 2, 1)

	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fields := result.Facts[0].Fields
	if got := fields["items_started_latest_day"].Integer; got == nil || *got != 7 {
		t.Fatalf("items_started_latest_day = %#v, want 7 (org 5 + legacy-only 2)", fields["items_started_latest_day"])
	}
	if got := fields["items_completed_latest_day"].Integer; got == nil || *got != 4 {
		t.Fatalf("items_completed_latest_day = %#v, want 4 (org 3 + legacy-only 1)", fields["items_completed_latest_day"])
	}
	breakdown := fields["scope_breakdown"]
	if breakdown.Table == nil || len(breakdown.Rows) != 2 {
		t.Fatalf("scope_breakdown = %#v, want 2 rows", breakdown)
	}
	started := map[string]int64{}
	for _, row := range breakdown.Rows {
		scope := row.Fields["work_scope_id"].String
		n := row.Fields["items_started"].Integer
		if scope == nil || n == nil {
			t.Fatalf("breakdown row incomplete: %#v", row)
		}
		started[*scope] = *n
	}
	if started["scope-x"] != 9 || started["scope-legacy"] != 2 {
		t.Fatalf("breakdown items_started = %#v, want scope-x 9 (scope view) and scope-legacy 2", started)
	}
	daily := fields["daily_flow"]
	if len(daily.Rows) != 1 {
		t.Fatalf("daily_flow rows = %d, want 1", len(daily.Rows))
	}
	if got := daily.Rows[0].Fields["items_started"].Integer; got == nil || *got != 7 {
		t.Fatalf("daily_flow items_started = %#v, want 7 (org view)", daily.Rows[0].Fields["items_started"])
	}
}
