package devhealthfacts_test

// Ownership is synced state: its valid_from is the sync stamp, not the start of
// ownership. A past window must serve a project's owned teams although the
// ownership row was synced after the window ended. EXECUTED against a real
// ClickHouse, through the project rollup readers of the shared module.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestProjectRollupsReadOwnershipAsSyncedForAPastWindowAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4363Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("team_metrics_daily") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	providers := devhealthfacts.NewProviders(query)
	const orgID = "org-past-project-window"
	end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -60)
	syncedAfterWindow := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-past", orgID, "linear", "PAST1", "Project", uint8(1), "active", "", syncedAfterWindow)
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-past", "Team Past", syncedAfterWindow, orgID, "linear", uint8(1))
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-past", "irrelevant", "PAST1", "native", syncedAfterWindow, nil, syncedAfterWindow)
	inWindow := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	computed := inWindow.Add(6 * time.Hour)
	exec(`INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		inWindow, "team-past", "Team Past", uint32(7), uint32(1), uint32(1), 0.1, 0.1, computed, orgID)
	exec(`INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		inWindow, "team-past", "product", "growth", uint32(30), uint32(12), uint32(4), uint64(850), computed, orgID)
	exec(`INSERT INTO capacity_forecasts (forecast_id, computed_at, team_id, work_scope_id, backlog_size, throughput_mean, throughput_stddev, insufficient_history, high_variance, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"forecast-past", computed, "team-past", "proj-past", uint32(12), 3.2, 0.8, uint8(0), uint8(1), orgID)
	exec(`INSERT INTO estimate_coverage_metrics_daily (day, provider, work_scope_id, team_id, estimated_count, unestimated_count, backlog_size, ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		inWindow, "linear", "proj-past", "team-past", uint32(18), uint32(2), uint32(20), 0.9, computed, orgID)

	for _, kind := range []contextfabric.FactKind{
		contextfabric.FactMetrics, contextfabric.FactInvestment, contextfabric.FactReadiness, contextfabric.FactWorkload,
	} {
		t.Run(string(kind), func(t *testing.T) {
			provider := findProvider(t, providers, kind)
			rangeStart, rangeEnd := start, end
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &rangeStart, End: &rangeEnd},
				Kind: kind, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-past")},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
			if len(result.Facts) != 1 {
				t.Fatalf("%s facts = %d (state %v reason %q), want 1: ownership synced after the window end must still serve the window", kind, len(result.Facts), result.State, result.Reason)
			}
			if rows := result.Facts[0].Fields["team_breakdown"].Rows; len(rows) != 1 {
				t.Fatalf("%s team_breakdown = %d rows, want 1", kind, len(rows))
			}
		})
	}
}
