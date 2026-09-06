package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestFlowProviderTeamDailySeriesAgainstRealClickHouse proves CHAOS-4645's
// new queryTeamFlowDailySeries against a REAL ClickHouse server (CI-pinned
// clickhouse-server:26.7 via testcontainers, seeded/fixture data -- NOT
// kiac/dh_0830 live org data). The fakeClient cannot exercise this: a
// fake hands back pre-baked Go values regardless of the SQL text, so a
// column-type mismatch (the CHAOS-4645 bug: selecting a raw Date/
// LowCardinality(String) column into a Go string scan target without an
// explicit toString() cast) is invisible to it and only surfaces against a
// real driver. Seeds THREE days for one team, two of them under two
// different (provider, work_scope_id) pairs on the SAME day, to prove both
// the per-day grouping and the same-day cross-scope summation.
func TestFlowProviderTeamDailySeriesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("work_item_metrics_daily") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-flow-daily-series"
	seed := func(day string, computedAt interface{ Unix() int64 }, provider, workScopeID string, started, completed uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			day, provider, workScopeID, "team-flow-series", started, completed, uint32(1), float64(4), float64(8), float64(0.1), float64(2), computedAt, orgID); err != nil {
			t.Fatalf("seed work_item_metrics_daily(%s): %v", day, err)
		}
	}
	// Day 1: two providers, same work_scope_id -- must SUM into one row.
	seed(date(2026, 8, 10).Format("2006-01-02"), ts(2026, 8, 10, 6, 0, 0), "linear", "scope-x", 5, 3)
	seed(date(2026, 8, 10).Format("2006-01-02"), ts(2026, 8, 10, 7, 0, 0), "github", "scope-x", 2, 1)
	// Day 2: single row.
	seed(date(2026, 8, 11).Format("2006-01-02"), ts(2026, 8, 11, 6, 0, 0), "linear", "scope-x", 4, 4)

	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-flow-series")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1", result.Facts)
	}
	fact := result.Facts[0]
	tableValue := fact.Fields["daily_flow"]
	if tableValue.Table == nil {
		t.Fatalf("daily_flow missing entirely -- fields = %#v", fact.Fields)
	}
	if err := tableValue.Validate(); err != nil {
		t.Fatalf("daily_flow fails FactValue.Validate() against real ClickHouse data: %v", err)
	}
	if tableValue.Table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("daily_flow.Shape = %q, want time_series", tableValue.Table.Shape)
	}
	rows := tableValue.Rows
	if len(rows) != 2 {
		t.Fatalf("daily_flow rows = %d, want 2 (one per distinct day) -- rows: %#v", len(rows), rows)
	}
	byDay := map[string]contextfabric.FactValueRow{}
	for _, row := range rows {
		day := row.Fields["day"].String
		if day == nil {
			t.Fatalf("row missing day: %#v", row)
		}
		byDay[*day] = row
	}
	day1, ok := byDay["2026-08-10"]
	if !ok {
		t.Fatalf("no row for 2026-08-10; byDay = %#v", byDay)
	}
	if got := day1.Fields["items_started"].Integer; got == nil || *got != 7 {
		t.Fatalf("2026-08-10 items_started = %#v, want 7 (5+2, summed across both providers)", day1.Fields["items_started"])
	}
	if got := day1.Fields["items_completed"].Integer; got == nil || *got != 4 {
		t.Fatalf("2026-08-10 items_completed = %#v, want 4 (3+1)", day1.Fields["items_completed"])
	}
	day2, ok := byDay["2026-08-11"]
	if !ok {
		t.Fatalf("no row for 2026-08-11; byDay = %#v", byDay)
	}
	if got := day2.Fields["items_started"].Integer; got == nil || *got != 4 {
		t.Fatalf("2026-08-11 items_started = %#v, want 4", day2.Fields["items_started"])
	}
	// Additive: the pre-existing scope_breakdown/items_started scalar (both
	// of which reflect only the LATEST day's rows) must be untouched by the
	// new daily_flow field's presence.
	if got := fact.Fields["items_started"].Integer; got == nil {
		t.Fatalf("pre-existing items_started scalar is missing -- daily_flow must be additive, not replace it")
	}
}

// TestFlowProviderProjectDailySeriesAgainstRealClickHouse is the project
// counterpart, proving queryProjectFlowDailySeries sums across two owning
// teams' contributions for the SAME day, against a real ClickHouse server.
func TestFlowProviderProjectDailySeriesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "work_item_metrics_daily") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-flow-project-daily-series"
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-flow-series", orgID, "linear", "FLOWSERIES", "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	seed := func(teamID string, computedAt interface{ Unix() int64 }, started, completed uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 10), "linear", "proj-flow-series", teamID, started, completed, uint32(1), float64(4), float64(8), float64(0.1), float64(2), computedAt, orgID); err != nil {
			t.Fatalf("seed work_item_metrics_daily(%s): %v", teamID, err)
		}
	}
	seed("team-a", ts(2026, 8, 10, 6, 0, 0), 5, 3)
	seed("team-b", ts(2026, 8, 10, 6, 0, 0), 2, 1)

	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-flow-series")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1", result.Facts)
	}
	fact := result.Facts[0]
	tableValue := fact.Fields["daily_flow"]
	if tableValue.Table == nil {
		t.Fatalf("daily_flow missing entirely -- fields = %#v", fact.Fields)
	}
	if err := tableValue.Validate(); err != nil {
		t.Fatalf("daily_flow fails FactValue.Validate() against real ClickHouse data: %v", err)
	}
	rows := tableValue.Rows
	if len(rows) != 1 {
		t.Fatalf("daily_flow rows = %d, want 1 (one day, both teams summed into it)", len(rows))
	}
	if got := rows[0].Fields["items_started"].Integer; got == nil || *got != 7 {
		t.Fatalf("items_started = %#v, want 7 (5+2, summed across both owning teams for the day)", rows[0].Fields["items_started"])
	}
	if got := rows[0].Fields["items_completed"].Integer; got == nil || *got != 4 {
		t.Fatalf("items_completed = %#v, want 4 (3+1)", rows[0].Fields["items_completed"])
	}
}
