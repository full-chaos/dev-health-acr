package devhealthfacts_test

import (
	"context"
	"math"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func insertTeamMetricsRepoRow(t *testing.T, ctx context.Context, conn clickhousedriver.Conn, orgID, teamID, repoID string, day time.Time, commits, afterHours, weekend uint32, storedRatio float64) {
	t.Helper()
	// The stored ratios are deliberately wrong (storedRatio): the served
	// ratio must be recomputed from the summed counts, never read back.
	if err := conn.Exec(ctx, `INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id, repo_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		day, teamID, "Team "+teamID, commits, afterHours, weekend, storedRatio, storedRatio, ts(2026, 8, 12, 6, 0, 0), orgID, repoID); err != nil {
		t.Fatalf("seed team metrics %s/%s: %v", teamID, repoID, err)
	}
}

func approx(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) < 1e-9
}

// TestTeamMetricsSumRepositoriesAgainstRealClickHouse proves, through the
// provider's ReadFacts, that a team's metrics count every repository of
// the team: counts are summed, ratios are recomputed from the summed
// counts, and the legacy ” bucket beside real rows is not counted.
func TestTeamMetricsSumRepositoriesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4347Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	day := date(2026, 8, 12)

	read := func(t *testing.T, orgID string, subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
		t.Helper()
		provider := findProvider(t, providers, contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		return result.Facts[0]
	}
	wantInt := func(t *testing.T, fact contextfabric.CanonicalFact, name string, want int64) {
		t.Helper()
		if got := fact.Fields[name].Integer; got == nil || *got != want {
			t.Fatalf("%s = %#v, want %d", name, fact.Fields[name], want)
		}
	}

	t.Run("team_with_two_repositories_sums_counts_and_recomputes_ratios", func(t *testing.T) {
		const orgID = "org-team-two-repos"
		// repo-1: 10 commits, 2 after hours, 1 weekend; repo-2: 20, 7, 3.
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-1", "repo-1", day, 10, 2, 1, 0.77)
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-1", "repo-2", day, 20, 7, 3, 0.66)
		fact := read(t, orgID, teamSubject("team-1"))
		wantInt(t, fact, "commits_count", 30)
		wantInt(t, fact, "after_hours_commits_count", 9)
		wantInt(t, fact, "weekend_commits_count", 4)
		// 9/30 and 4/30, never the stored 0.77/0.66 and never an average.
		if got := fact.Fields["after_hours_commit_ratio"].Number; !approx(got, 0.3) {
			t.Fatalf("after_hours_commit_ratio = %#v, want 0.3", fact.Fields["after_hours_commit_ratio"])
		}
		if got := fact.Fields["weekend_commit_ratio"].Number; !approx(got, 4.0/30.0) {
			t.Fatalf("weekend_commit_ratio = %#v, want 4/30", fact.Fields["weekend_commit_ratio"])
		}
	})

	t.Run("legacy_empty_repository_bucket_beside_real_rows_is_not_counted", func(t *testing.T) {
		const orgID = "org-team-legacy"
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-1", "", day, 99, 50, 50, 0.5)
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-1", "repo-1", day, 10, 2, 1, 0.5)
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-1", "repo-2", day, 20, 7, 3, 0.5)
		fact := read(t, orgID, teamSubject("team-1"))
		wantInt(t, fact, "commits_count", 30)
		wantInt(t, fact, "after_hours_commits_count", 9)
		wantInt(t, fact, "weekend_commits_count", 4)
	})

	t.Run("single_repository_team_keeps_its_counts_and_recomputes_ratio", func(t *testing.T) {
		const orgID = "org-team-one-repo"
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-1", "repo-1", day, 8, 2, 0, 0.9)
		fact := read(t, orgID, teamSubject("team-1"))
		wantInt(t, fact, "commits_count", 8)
		if got := fact.Fields["after_hours_commit_ratio"].Number; !approx(got, 0.25) {
			t.Fatalf("after_hours_commit_ratio = %#v, want 2/8", fact.Fields["after_hours_commit_ratio"])
		}
		if got := fact.Fields["weekend_commit_ratio"].Number; !approx(got, 0) {
			t.Fatalf("weekend_commit_ratio = %#v, want 0", fact.Fields["weekend_commit_ratio"])
		}
	})

	t.Run("project_rollup_sums_each_teams_repositories_and_never_averages_ratios", func(t *testing.T) {
		const orgID = "org-project-two-repos"
		if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			"proj-1-internal-id", orgID, "linear", "PROJ1", "Project One", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
			t.Fatalf("seed projects row: %v", err)
		}
		// team-a: two repositories (10+20 commits); team-b: one (8).
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-a", "repo-1", day, 10, 2, 1, 0.77)
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-a", "repo-2", day, 20, 7, 3, 0.66)
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-a", "", day, 99, 50, 50, 0.5)
		insertTeamMetricsRepoRow(t, ctx, direct, orgID, "team-b", "repo-3", day, 8, 2, 0, 0.9)
		for _, team := range []string{"team-a", "team-b"} {
			if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				orgID, "linear", team, "legacy-mismatched-project-id", "PROJ1", "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
				t.Fatalf("seed ownership %s: %v", team, err)
			}
		}
		fact := read(t, orgID, projectSubject("linear", "proj-1-internal-id"))
		wantInt(t, fact, "team_count", 2)
		wantInt(t, fact, "commits_count", 38)
		wantInt(t, fact, "after_hours_commits_count", 11)
		wantInt(t, fact, "weekend_commits_count", 4)
		if _, ok := fact.Fields["after_hours_commit_ratio"]; ok {
			t.Fatalf("project fact carries a top-level ratio %#v; ratios are never averaged across teams", fact.Fields["after_hours_commit_ratio"])
		}
		rows := fact.Fields["team_breakdown"].Rows
		if len(rows) != 2 {
			t.Fatalf("team_breakdown = %#v, want 2 rows", rows)
		}
		want := map[string]struct {
			commits int64
			ratio   float64
		}{"team-a": {30, 0.3}, "team-b": {8, 0.25}}
		for _, row := range rows {
			team := *row.Fields["team_id"].String
			w, ok := want[team]
			if !ok {
				t.Fatalf("unexpected team %q in breakdown", team)
			}
			if got := row.Fields["commits_count"].Integer; got == nil || *got != w.commits {
				t.Fatalf("%s commits_count = %#v, want %d", team, row.Fields["commits_count"], w.commits)
			}
			if got := row.Fields["after_hours_commit_ratio"].Number; !approx(got, w.ratio) {
				t.Fatalf("%s after_hours_commit_ratio = %#v, want %v", team, row.Fields["after_hours_commit_ratio"], w.ratio)
			}
		}
	})
}
