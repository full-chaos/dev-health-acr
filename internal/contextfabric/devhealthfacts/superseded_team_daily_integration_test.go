package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A carried team leaves the same recomputed day under the superseded id
// (is_active = 0) and the current id. The project series counts the day once.
func TestFlowProjectDailySeriesCountsACarriedTeamOnce(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-flow-superseded-team"
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-superseded", orgID, "linear", "SUPERSEDED", "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	for _, team := range []struct {
		id     string
		active uint8
	}{{"platform", 0}, {"team:linear:platform", 1}} {
		if err := direct.Exec(ctx, `INSERT INTO teams (id, org_id, name, provider, is_active, updated_at) VALUES (?,?,?,?,?,?)`,
			team.id, orgID, "Platform", "linear", team.active, ts(2026, 8, 12, 0, 0, 0)); err != nil {
			t.Fatalf("seed teams(%s): %v", team.id, err)
		}
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 10), "linear", "proj-superseded", team.id, uint32(5), uint32(3), uint32(1), float64(4), float64(8), float64(0.1), float64(2), ts(2026, 8, 10, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed work_item_metrics_daily(%s): %v", team.id, err)
		}
	}

	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-superseded")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1", result.Facts)
	}
	table := result.Facts[0].Fields["daily_flow"]
	if table.Table == nil || len(table.Rows) != 1 {
		t.Fatalf("daily_flow = %#v, want one row", table)
	}
	if got := table.Rows[0].Fields["items_started"].Integer; got == nil || *got != 5 {
		t.Fatalf("items_started = %#v, want 5 (the carried team's day counted once, not 10)", table.Rows[0].Fields["items_started"])
	}
}

// A retraction row (counts zero) over the retired team key neither dilutes the
// day's bug ratio average nor adds a team to the project's breakdown.
func TestFlowProjectReadsIgnoreARetractionRowOverARetiredKey(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-flow-retraction-row"
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-retraction", orgID, "linear", "RETRACT", "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	for _, team := range []struct {
		id     string
		active uint8
	}{{"platform", 0}, {"team:linear:platform", 1}} {
		if err := direct.Exec(ctx, `INSERT INTO teams (id, org_id, name, provider, is_active, updated_at) VALUES (?,?,?,?,?,?)`,
			team.id, orgID, "Platform", "linear", team.active, ts(2026, 8, 12, 0, 0, 0)); err != nil {
			t.Fatalf("seed teams(%s): %v", team.id, err)
		}
	}
	insert := func(teamID string, started, completed uint32, bugRatio float64) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 10), "linear", "proj-retraction", teamID, started, completed, uint32(0), nil, nil, bugRatio, float64(0), ts(2026, 8, 11, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed work_item_metrics_daily(%s): %v", teamID, err)
		}
	}
	insert("team:linear:platform", 5, 3, 0.4)
	insert("platform", 0, 0, 0)

	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-retraction")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1", result.Facts)
	}
	series := result.Facts[0].Fields["daily_flow"]
	if series.Table == nil || len(series.Rows) != 1 {
		t.Fatalf("daily_flow = %#v, want one row", series)
	}
	if got := series.Rows[0].Fields["bug_completed_ratio"].Number; got == nil || *got != 0.4 {
		t.Fatalf("bug_completed_ratio = %#v, want 0.4 (the retraction row's 0 must not enter the average)", series.Rows[0].Fields["bug_completed_ratio"])
	}
}
