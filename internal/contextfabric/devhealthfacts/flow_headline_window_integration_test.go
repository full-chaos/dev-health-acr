package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func flowInt(t *testing.T, fields map[string]contextfabric.FactValue, name string) int64 {
	t.Helper()
	v, ok := fields[name]
	if !ok || v.Integer == nil {
		t.Fatalf("field %q missing or not an integer; fields = %v", name, fieldNames(fields))
	}
	return *v.Integer
}

func fieldNames(fields map[string]contextfabric.FactValue) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	return names
}

func flowStr(t *testing.T, fields map[string]contextfabric.FactValue, name string) string {
	t.Helper()
	v, ok := fields[name]
	if !ok || v.String == nil {
		t.Fatalf("field %q missing or not a string; fields = %v", name, fieldNames(fields))
	}
	return *v.String
}

func seriesSums(t *testing.T, fields map[string]contextfabric.FactValue) (started, completed int64) {
	t.Helper()
	table := fields["daily_flow"].Table
	if table == nil {
		t.Fatalf("daily_flow missing; fields = %v", fieldNames(fields))
	}
	for _, row := range table.Rows {
		started += flowInt(t, row.Fields, "items_started")
		completed += flowInt(t, row.Fields, "items_completed")
	}
	return started, completed
}

// Scopes whose latest observed days differ (scope-a Aug 20, scope-b Aug 5)
// make the latest-day sum and the window sum disagree; a same-day rerun and an
// out-of-window row must change neither.
func seedFlowHeadlineTeam(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID, teamID string) {
	t.Helper()
	seed := func(day time.Time, computedAt time.Time, provider, scope string, started, completed uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			day, provider, scope, teamID, started, completed, uint32(7), float64(4), float64(8), float64(0.1), float64(2), computedAt, orgID); err != nil {
			t.Fatalf("seed work_item_metrics_daily: %v", err)
		}
	}
	seed(date(2026, 7, 20), ts(2026, 7, 20, 6, 0, 0), "linear", "scope-a", 100, 100)
	seed(date(2026, 8, 1), ts(2026, 8, 1, 6, 0, 0), "linear", "scope-a", 10, 5)
	seed(date(2026, 8, 10), ts(2026, 8, 10, 5, 0, 0), "linear", "scope-a", 99, 99)
	seed(date(2026, 8, 10), ts(2026, 8, 10, 6, 0, 0), "linear", "scope-a", 3, 2)
	seed(date(2026, 8, 20), ts(2026, 8, 20, 6, 0, 0), "linear", "scope-a", 4, 1)
	seed(date(2026, 8, 5), ts(2026, 8, 5, 6, 0, 0), "github", "scope-b", 6, 6)
}

func TestFlowHeadlineNamesTheWindowTeamAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)
	const orgID, teamID = "org-flow-headline-window", "team-headline"
	seedFlowHeadlineTeam(t, ctx, direct, orgID, teamID)
	provider := findProvider(t, providers, contextfabric.FactFlow)

	start, end := ts(2026, 8, 1, 0, 0, 0), ts(2026, 8, 30, 0, 0, 0)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("ReadFacts() = %v, %v; want one fact", result.Facts, err)
	}
	fields := result.Facts[0].Fields

	for _, old := range []string{"items_started", "items_completed"} {
		if _, ok := fields[old]; ok {
			t.Fatalf("headline still carries the unnamed-window field %q", old)
		}
	}
	if got := flowInt(t, fields, "items_started_window"); got != 23 {
		t.Fatalf("items_started_window = %d, want 23 (10+3+4+6; rerun and out-of-window rows excluded)", got)
	}
	if got := flowInt(t, fields, "items_completed_window"); got != 14 {
		t.Fatalf("items_completed_window = %d, want 14", got)
	}
	seriesStarted, seriesCompleted := seriesSums(t, fields)
	if seriesStarted != flowInt(t, fields, "items_started_window") || seriesCompleted != flowInt(t, fields, "items_completed_window") {
		t.Fatalf("window totals %d/%d do not equal the daily_flow series sums %d/%d",
			flowInt(t, fields, "items_started_window"), flowInt(t, fields, "items_completed_window"), seriesStarted, seriesCompleted)
	}
	if got := flowInt(t, fields, "items_started_latest_day"); got != 10 {
		t.Fatalf("items_started_latest_day = %d, want 10 (scope-a Aug 20 = 4, scope-b Aug 5 = 6)", got)
	}
	if got := flowInt(t, fields, "items_completed_latest_day"); got != 7 {
		t.Fatalf("items_completed_latest_day = %d, want 7 (1+6)", got)
	}
	var breakdownStarted int64
	observed := map[string]string{}
	for _, row := range fields["scope_breakdown"].Table.Rows {
		breakdownStarted += flowInt(t, row.Fields, "items_started")
		observed[flowStr(t, row.Fields, "work_scope_id")] = flowStr(t, row.Fields, "day")
	}
	if breakdownStarted != flowInt(t, fields, "items_started_latest_day") {
		t.Fatalf("latest-day headline %d != scope_breakdown sum %d", flowInt(t, fields, "items_started_latest_day"), breakdownStarted)
	}
	if observed["scope-a"] != "2026-08-20" || observed["scope-b"] != "2026-08-05" {
		t.Fatalf("observed latest day per scope = %v", observed)
	}
	if got := flowStr(t, fields, "window_mode"); got != "range" {
		t.Fatalf("window_mode = %q", got)
	}
	if flowStr(t, fields, "window_start") != "2026-08-01" || flowStr(t, fields, "window_end") != "2026-08-30" {
		t.Fatalf("window echo = %q..%q", flowStr(t, fields, "window_start"), flowStr(t, fields, "window_end"))
	}
	if got := flowInt(t, fields, "window_days"); got != 30 {
		t.Fatalf("window_days = %d, want 30", got)
	}
	if got := flowInt(t, fields, "window_days_with_data"); got != 4 {
		t.Fatalf("window_days_with_data = %d, want 4 (Aug 1, 5, 10, 20)", got)
	}
}

func TestFlowHeadlineCurrentAxisHasNoWindowTotalAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)
	const orgID, teamID = "org-flow-headline-current", "team-headline-current"
	seedFlowHeadlineTeam(t, ctx, direct, orgID, teamID)
	provider := findProvider(t, providers, contextfabric.FactFlow)

	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("ReadFacts() = %v, %v; want one fact", result.Facts, err)
	}
	fields := result.Facts[0].Fields
	for _, name := range []string{"items_started_window", "items_completed_window", "window_days", "window_days_with_data", "items_started", "items_completed"} {
		if _, ok := fields[name]; ok {
			t.Fatalf("current-axis fact carries %q; no window exists, so no total may be reported", name)
		}
	}
	if got := flowStr(t, fields, "window_mode"); got != "current" {
		t.Fatalf("window_mode = %q, want current", got)
	}
	if got := flowInt(t, fields, "items_started_latest_day"); got != 10 {
		t.Fatalf("items_started_latest_day = %d, want 10 (scope-a Aug 20 = 4, scope-b Aug 5 = 6)", got)
	}
}

func TestFlowHeadlineNamesTheWindowProjectAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)
	const orgID = "org-flow-headline-project"
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-headline", orgID, "linear", "HEADLINE", "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	seed := func(day time.Time, teamID string, started, completed uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			day, "linear", "proj-headline", teamID, started, completed, uint32(1), float64(4), float64(8), float64(0.1), float64(2), ts(2026, 8, 30, 0, 0, 0), orgID); err != nil {
			t.Fatalf("seed work_item_metrics_daily: %v", err)
		}
	}
	seed(date(2026, 8, 2), "team-a", 8, 4)
	seed(date(2026, 8, 12), "team-a", 3, 2)
	seed(date(2026, 8, 6), "team-b", 5, 5)
	provider := findProvider(t, providers, contextfabric.FactFlow)

	start, end := ts(2026, 8, 1, 0, 0, 0), ts(2026, 8, 30, 0, 0, 0)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-headline")},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("ReadFacts() = %v, %v; want one fact", result.Facts, err)
	}
	fields := result.Facts[0].Fields
	if _, ok := fields["items_started"]; ok {
		t.Fatalf("project headline still carries items_started")
	}
	if got := flowInt(t, fields, "items_started_window"); got != 16 {
		t.Fatalf("items_started_window = %d, want 16 (8+3+5)", got)
	}
	if got := flowInt(t, fields, "items_completed_window"); got != 11 {
		t.Fatalf("items_completed_window = %d, want 11 (4+2+5)", got)
	}
	seriesStarted, _ := seriesSums(t, fields)
	if seriesStarted != 16 {
		t.Fatalf("daily_flow sums to %d, want it equal to the window total 16", seriesStarted)
	}
	if got := flowInt(t, fields, "items_started_latest_day"); got != 8 {
		t.Fatalf("items_started_latest_day = %d, want 8 (team-a latest Aug 12 = 3, team-b Aug 6 = 5)", got)
	}
	for _, row := range fields["team_breakdown"].Table.Rows {
		if _, ok := row.Fields["items_started"]; ok {
			t.Fatalf("team_breakdown row still carries items_started: %v", row.Fields)
		}
		flowInt(t, row.Fields, "items_started_latest_day")
	}
}
