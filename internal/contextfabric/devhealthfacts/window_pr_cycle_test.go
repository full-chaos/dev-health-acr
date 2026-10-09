package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readMetricsFor(t *testing.T, client *fakeClient, subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	return result.Facts[0]
}

func windowStatement(t *testing.T, client *fakeClient) string {
	t.Helper()
	for _, q := range client.queries {
		if strings.Contains(q.statement, "FROM git_pull_requests") {
			return q.statement
		}
	}
	t.Fatalf("no git_pull_requests query among %d queries", len(client.queries))
	return ""
}

func TestRepositoryMetricsServeWindowPRCycleMedianBesideTheDailyMedian(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}},
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", int64(3), float64(1)}}},
	}}
	fact := readMetricsFor(t, client, repoSubject("repo-1"))
	if got := fact.Fields["window_pr_cycle_hours_median"].Number; got == nil || *got != 1 {
		t.Fatalf("window_pr_cycle_hours_median = %#v, want 1", fact.Fields["window_pr_cycle_hours_median"])
	}
	if got := fact.Fields["window_pr_count"].Integer; got == nil || *got != 3 {
		t.Fatalf("window_pr_count = %#v, want 3", fact.Fields["window_pr_count"])
	}
	if got := fact.Fields["median_pr_cycle_hours"].Number; got == nil || *got != 12.5 {
		t.Fatalf("median_pr_cycle_hours = %#v, want the repo-day value 12.5 unchanged", fact.Fields["median_pr_cycle_hours"])
	}
	statement := windowStatement(t, client)
	for _, want := range []string{"quantileExactInclusive(0.5)", "merged_at IS NOT NULL", "FINAL", "GROUP BY repo_id"} {
		if !strings.Contains(statement, want) {
			t.Fatalf("window statement lacks %q:\n%s", want, statement)
		}
	}
	if strings.Contains(statement, "median_pr_cycle_hours") {
		t.Fatalf("window statement reads the daily median:\n%s", statement)
	}
}

func TestRepositoryMetricsWithoutMergedPullRequestsHaveNoWindowCycle(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}}}}
	fact := readMetricsFor(t, client, repoSubject("repo-1"))
	for _, name := range []string{"window_pr_cycle_hours_median", "window_pr_count"} {
		if _, ok := fact.Fields[name]; ok {
			t.Fatalf("%s = %#v, want absent (never 0) when no pull request merged in the window", name, fact.Fields[name])
		}
	}
}

func TestTeamMetricsServeOneWindowPRCycleMedianOverTheOwnedRepositories(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("team-1")}},
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"team-1", "repo-a", "acme/a"}, {"team-1", "repo-b", "acme/b"}}},
		{match: "FROM git_pull_requests", rows: [][]any{{"", int64(5), float64(7.5)}}},
	}}
	fact := readMetricsFor(t, client, teamSubject("team-1"))
	if got := fact.Fields["window_pr_cycle_hours_median"].Number; got == nil || *got != 7.5 {
		t.Fatalf("window_pr_cycle_hours_median = %#v, want 7.5", fact.Fields["window_pr_cycle_hours_median"])
	}
	if got := fact.Fields["window_pr_count"].Integer; got == nil || *got != 5 {
		t.Fatalf("window_pr_count = %#v, want 5", fact.Fields["window_pr_count"])
	}
	statement := windowStatement(t, client)
	if strings.Contains(statement, "GROUP BY") {
		t.Fatalf("team window statement groups by repository, so it cannot be one median over the union:\n%s", statement)
	}
}

func TestTeamMetricsWithoutMergedPullRequestsHaveNoWindowCycle(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("team-1")}},
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"team-1", "repo-a", "acme/a"}}},
	}}
	fact := readMetricsFor(t, client, teamSubject("team-1"))
	if _, ok := fact.Fields["window_pr_cycle_hours_median"]; ok {
		t.Fatalf("window_pr_cycle_hours_median present with no merged pull request: %#v", fact.Fields["window_pr_cycle_hours_median"])
	}
}
