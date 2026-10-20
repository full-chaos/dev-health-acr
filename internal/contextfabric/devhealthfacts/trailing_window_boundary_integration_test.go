package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestTrailingWindowEndExcludesTheEndDayAgainstRealClickHouse runs the real
// rollup statements over a window that ends at 00:00 UTC of a day: rows dated
// that day, and a pull request merged at exactly that instant, are outside it.
// Every rollup of one answer must agree on that: change failure, deployments
// and the window pull request count.
func TestTrailingWindowEndExcludesTheEndDayAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("repos", "team_repo_ownership", "git_pull_requests", "repo_metrics_daily", "repo_change_failure_daily", "deploy_metrics_daily", "team_metrics_daily", "projects", "team_project_ownership", "work_items", "work_graph_issue_pr") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-trailing-boundary"
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	synced := ts(2026, 9, 29, 0, 0, 0)
	repo := repoUUID("repo-boundary")
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo, orgID, "acme/repo-boundary", "github", synced)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-boundary", repo, "acme/repo-boundary", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, synced)
	for _, day := range []time.Time{date(2026, 9, 18), date(2026, 9, 19), date(2026, 9, 20)} {
		exec(`INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repo, orgID, day, uint32(1), uint32(1), float64(1), float64(0), nil, uint32(1), float64(0), synced)
	}
	exec(`INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id, repo_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		date(2026, 9, 19), "team-boundary", "Team Boundary", uint32(1), uint32(0), uint32(0), float64(0), float64(0), synced, orgID, repo)

	// 2 deployments and 1 failure on the last day inside the window; 100 and
	// 50 on the end day, which are outside it.
	for _, row := range []struct {
		day               time.Time
		deployments, fail uint32
	}{{date(2026, 9, 19), 2, 1}, {date(2026, 9, 20), 100, 50}} {
		exec(`INSERT INTO repo_change_failure_daily (org_id, repo_id, day, deployments_count, failed_deployments_native, failed_deployments_heuristic, incidents_direct, incidents_via_deployment, computed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, repo, row.day, row.deployments, row.fail, uint32(0), uint32(0), uint32(0), row.day.Add(6*time.Hour))
		exec(`INSERT INTO deploy_metrics_daily (repo_id, day, deployments_count, failed_deployments_count, deploy_time_p50_hours, lead_time_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?)`,
			repo, row.day, row.deployments, row.fail, 1.0, 1.0, row.day.Add(6*time.Hour), orgID)
	}
	pr := func(number uint32, merged time.Time) {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repo, orgID, number, "pr", "closed", synced, merged.Add(-2*time.Hour), merged, merged, "feature", "")
	}
	pr(1, ts(2026, 9, 19, 23, 0, 0))
	pr(2, ts(2026, 9, 20, 0, 0, 0))

	window := contractsv1TimeContext(ts(2026, 9, 13, 0, 0, 0), ts(2026, 9, 20, 0, 0, 0))
	read := func(kind contextfabric.FactKind, subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
		t.Helper()
		provider := findProvider(t, devhealthfacts.NewProviders(query), kind)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: window, Kind: kind, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts(%s) error = %v", kind, err)
		}
		return factFor(t, result, subject)
	}
	for name, subject := range map[string]contextfabric.SubjectRef{"repository": repoSubject(repo), "team": teamSubject("team-boundary")} {
		subject := subject
		t.Run(name+"_change_failure_and_pull_requests", func(t *testing.T) {
			fields := read(contextfabric.FactMetrics, subject).Fields
			wantInt(t, fields, "change_failure_deployments_count", 2)
			wantInt(t, fields, "change_failure_failed_deployments_count", 1)
			wantInt(t, fields, "window_pr_count", 1)
		})
	}
	t.Run("team_deployments", func(t *testing.T) {
		fields := read(contextfabric.FactDeployments, teamSubject("team-boundary")).Fields
		wantInt(t, fields, "deployments_count_window", 2)
		wantInt(t, fields, "failed_deployments_count_window", 1)
	})
}
