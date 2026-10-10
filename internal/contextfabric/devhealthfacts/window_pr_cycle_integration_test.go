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

// TestWindowPRCycleMedianIsOverPullRequestsNotDailyMedians runs the real
// statements: pull requests of 1 h, 1 h and 100 h merge on three days, so
// repo-day median is stored as 100 (the stored daily value is a fixture
// input, not derived here) while the window median over the three pull
// requests is 1 h; a team owning two repositories gets one median over the
// union, not a combination of the repository medians; a window with no
// merged pull request has no value.
func TestWindowPRCycleMedianIsOverPullRequestsNotDailyMedians(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("repos", "team_repo_ownership", "git_pull_requests", "repo_metrics_daily", "repo_change_failure_daily", "team_metrics_daily") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-window-cycle"
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	synced := ts(2026, 9, 29, 0, 0, 0)
	pr := func(label string, number uint32, created time.Time, hours float64, merged bool) {
		var mergedAt any
		if merged {
			mergedAt = created.Add(time.Duration(hours * float64(time.Hour)))
		}
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoUUID(label), orgID, number, "pr", "closed", synced, created, mergedAt, nil, "feature", "")
	}
	for _, label := range []string{"repo-a", "repo-b", "repo-empty"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, "github", synced)
		exec(`INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoUUID(label), orgID, date(2026, 9, 18), uint32(1), uint32(1), float64(100), float64(0), nil, uint32(1), float64(0), synced)
	}
	pr("repo-a", 1, ts(2026, 9, 14, 9, 0, 0), 1, true)
	pr("repo-a", 2, ts(2026, 9, 15, 9, 0, 0), 1, true)
	pr("repo-a", 3, ts(2026, 9, 14, 10, 0, 0), 100, true)
	pr("repo-a", 4, ts(2026, 9, 17, 9, 0, 0), 5, false) // never merged
	pr("repo-a", 5, ts(2026, 8, 1, 9, 0, 0), 200, true) // merged outside the window
	pr("repo-b", 1, ts(2026, 9, 16, 9, 0, 0), 50, true)
	pr("repo-b", 2, ts(2026, 9, 16, 11, 0, 0), 60, true)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-1", repoUUID("repo-a"), "acme/repo-a", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, synced)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-1", repoUUID("repo-b"), "acme/repo-b", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, synced)
	exec(`INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id, repo_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		date(2026, 9, 18), "team-1", "Team 1", uint32(1), uint32(0), uint32(0), float64(0), float64(0), synced, orgID, repoUUID("repo-a"))

	start, end := ts(2026, 9, 13, 0, 0, 0), ts(2026, 9, 19, 23, 59, 59)
	read := func(subject contextfabric.SubjectRef) (contextfabric.CanonicalFact, bool) {
		t.Helper()
		provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contractsv1TimeContext(start, end), Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) == 0 {
			return contextfabric.CanonicalFact{}, false
		}
		return result.Facts[0], true
	}

	t.Run("repository_median_is_over_pull_requests", func(t *testing.T) {
		fact, _ := read(repoSubject(repoUUID("repo-a")))
		if got := fact.Fields["window_pr_cycle_hours_median"].Number; !approx(got, 1) {
			t.Fatalf("window_pr_cycle_hours_median = %#v, want 1", fact.Fields["window_pr_cycle_hours_median"])
		}
		if got := fact.Fields["window_pr_count"].Integer; got == nil || *got != 3 {
			t.Fatalf("window_pr_count = %#v, want 3", fact.Fields["window_pr_count"])
		}
		if got := fact.Fields["median_pr_cycle_hours"].Number; !approx(got, 100) {
			t.Fatalf("median_pr_cycle_hours = %#v, want the stored repo-day value 100 unchanged", fact.Fields["median_pr_cycle_hours"])
		}
	})
	t.Run("team_median_is_one_median_over_the_union", func(t *testing.T) {
		fact, _ := read(teamSubject("team-1"))
		// 1, 1, 50, 60, 100: the union median is 50. The repository medians
		// are 1 and 55, so a median of them (28) or a mean of them (28)
		// would differ.
		if got := fact.Fields["window_pr_cycle_hours_median"].Number; !approx(got, 50) {
			t.Fatalf("window_pr_cycle_hours_median = %#v, want 50", fact.Fields["window_pr_cycle_hours_median"])
		}
		if got := fact.Fields["window_pr_count"].Integer; got == nil || *got != 5 {
			t.Fatalf("window_pr_count = %#v, want 5", fact.Fields["window_pr_count"])
		}
	})
	t.Run("no_merged_pull_request_has_no_value", func(t *testing.T) {
		fact, ok := read(repoSubject(repoUUID("repo-empty")))
		if !ok {
			t.Fatal("repo-empty has its daily row, so a fact is expected")
		}
		if _, present := fact.Fields["window_pr_cycle_hours_median"]; present {
			t.Fatalf("window_pr_cycle_hours_median = %#v, want absent", fact.Fields["window_pr_cycle_hours_median"])
		}
		if _, present := fact.Fields["window_pr_count"]; present {
			t.Fatalf("window_pr_count = %#v, want absent", fact.Fields["window_pr_count"])
		}
	})
}
