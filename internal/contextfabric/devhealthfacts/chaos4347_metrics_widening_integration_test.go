package devhealthfacts_test

import (
	"context"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// createCHAOS4347Tables creates every source table CHAOS-4347's widened
// providers read, rendered from the shared production declaration
// (devhealthschema), never hand-written here -- the same discipline
// createCHAOS3780Tables documents and the same reason: a fixture that
// modeled its own idea of the schema would drift from what production
// actually has, silently.
func createCHAOS4347Tables(t *testing.T, ctx context.Context, connection clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL(
		"repo_metrics_daily", "team_metrics_daily", "team_project_ownership",
		"cicd_metrics_daily", "deploy_metrics_daily",
	) {
		if err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
}

// TestCHAOS4347WideningAgainstRealClickHouse proves the fakeClient-based
// unit tests' claims actually hold against a real server evaluating the
// real query text: row_number()/PARTITION BY tie-breaking on
// team_metrics_daily (the same intraday-rerun risk metrics.go's package
// doc comment documents for repo_metrics_daily), the
// team_project_ownership -> team_metrics_daily JOIN, valid_to-based
// current-ownership filtering, and the cicd_metrics_daily/
// deploy_metrics_daily aggregate reads. None of this can be exercised by
// fakeClient, which never executes SQL.
func TestCHAOS4347WideningAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4347Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)

	t.Run("team_metrics_daily_picks_latest_row_across_intraday_reruns", func(t *testing.T) {
		const orgID = "org-team-rerun"
		// Two rows sharing (team_id, day) -- an intraday rerun, the exact
		// shape metrics.go's package doc comment documents for
		// repo_metrics_daily/team_metrics_daily. The LATER computed_at row
		// carries different values; only it must be reported.
		if err := direct.Exec(ctx, `INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "team-1", "Team One", uint32(10), uint32(1), uint32(0), 0.1, 0.0, ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed early rerun: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "team-1", "Team One", uint32(25), uint32(6), uint32(2), 0.24, 0.08, ts(2026, 8, 12, 18, 0, 0), orgID); err != nil {
			t.Fatalf("seed later rerun: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		if got := result.Facts[0].Fields["commits_count"].Integer; got == nil || *got != 25 {
			t.Fatalf("commits_count = %#v, want the LATER rerun's 25, not the earlier 10", result.Facts[0].Fields["commits_count"])
		}
	})

	t.Run("project_rollup_sums_counts_across_current_owning_teams_via_real_join", func(t *testing.T) {
		const orgID = "org-project-rollup"
		if err := direct.Exec(ctx, `INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "team-a", "Team A", uint32(20), uint32(4), uint32(2), 0.2, 0.1, ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed team-a metrics: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "team-b", "Team B", uint32(5), uint32(0), uint32(0), 0.0, 0.0, ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed team-b metrics: %v", err)
		}
		// A third team owns the SAME project but has no team_metrics_daily
		// row at all -- the INNER JOIN must simply not contribute it,
		// never fail the whole read.
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-a", "proj-1", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed team-a ownership: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-b", "proj-1", nil, "manual", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed team-b ownership: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-c-no-metrics", "proj-1", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed team-c ownership: %v", err)
		}
		// A LAPSED ownership edge for a DIFFERENT team, past its valid_to --
		// must not contribute even though it would if the query forgot the
		// currently-active filter.
		if err := direct.Exec(ctx, `INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), "team-lapsed", "Team Lapsed", uint32(999), uint32(999), uint32(999), 0.9, 0.9, ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed lapsed team metrics: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-lapsed", "proj-1", nil, "native", ts(2026, 1, 1, 0, 0, 0), ts(2026, 6, 1, 0, 0, 0), ts(2026, 6, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed lapsed ownership: %v", err)
		}

		provider := findProvider(t, providers, contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		fact := result.Facts[0]
		if got := fact.Fields["commits_count"].Integer; got == nil || *got != 25 {
			t.Fatalf("commits_count = %#v, want 20+5=25 (team-a + team-b only, lapsed and metrics-less teams excluded)", fact.Fields["commits_count"])
		}
		if got := fact.Fields["team_count"].Integer; got == nil || *got != 2 {
			t.Fatalf("team_count = %#v, want 2", fact.Fields["team_count"])
		}
		rows := fact.Fields["team_breakdown"].Rows
		if len(rows) != 2 {
			t.Fatalf("team_breakdown = %#v, want 2 rows", rows)
		}
	})

	t.Run("cicd_metrics_daily_repository_aggregate", func(t *testing.T) {
		const orgID = "org-cicd"
		repoID := "11111111-1111-1111-1111-111111111111"
		if err := direct.Exec(ctx, `INSERT INTO cicd_metrics_daily (repo_id, day, pipelines_count, success_rate, avg_duration_minutes, p90_duration_minutes, avg_queue_minutes, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			repoID, date(2026, 8, 12), uint32(40), 0.85, 10.5, 22.0, 2.0, ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed cicd metrics: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactContinuousIntegration)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactContinuousIntegration, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		if got := result.Facts[0].Fields["pipelines_count"].Integer; got == nil || *got != 40 {
			t.Fatalf("pipelines_count = %#v", result.Facts[0].Fields["pipelines_count"])
		}
	})

	t.Run("deploy_metrics_daily_repository_aggregate", func(t *testing.T) {
		const orgID = "org-deploy-metrics"
		repoID := "22222222-2222-2222-2222-222222222222"
		if err := direct.Exec(ctx, `INSERT INTO deploy_metrics_daily (repo_id, day, deployments_count, failed_deployments_count, deploy_time_p50_hours, lead_time_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?)`,
			repoID, date(2026, 8, 12), uint32(6), uint32(1), 1.2, 4.5, ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed deploy metrics: %v", err)
		}
		provider := findProvider(t, providers, contextfabric.FactDeployments)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		if got := result.Facts[0].Fields["deployments_count"].Integer; got == nil || *got != 6 {
			t.Fatalf("deployments_count = %#v", result.Facts[0].Fields["deployments_count"])
		}
	})
}
