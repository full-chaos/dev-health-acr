package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTeamWindowPRCycleServedWithoutWellbeingRow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM team_metrics_daily", rows: nil},
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"team-1", "repo-a", "acme/a"}, {"team-1", "repo-b", "acme/b"}}},
		{match: "FROM git_pull_requests", rows: [][]any{{"", int64(5), float64(7.5)}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1 team fact carrying the window PR cycle", len(result.Facts))
	}
	f := result.Facts[0].Fields
	if got := f["window_pr_cycle_hours_median"].Number; got == nil || *got != 7.5 {
		t.Fatalf("window_pr_cycle_hours_median = %#v, want 7.5", f["window_pr_cycle_hours_median"])
	}
	if got := f["window_pr_count"].Integer; got == nil || *got != 5 {
		t.Fatalf("window_pr_count = %#v, want 5", f["window_pr_count"])
	}
	for _, name := range []string{"day", "commits_count", "after_hours_commits_count", "weekend_commits_count", "after_hours_commit_ratio", "weekend_commit_ratio"} {
		if _, ok := f[name]; ok {
			t.Fatalf("%s served without a wellbeing row: %#v", name, f[name])
		}
	}
}

func TestRepositoryWindowPRCycleServedWithoutDailyRow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repo_metrics_daily", rows: nil},
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", int64(3), float64(1)}}},
	}}
	fact := readMetricsFor(t, client, repoSubject("repo-1"))
	if got := fact.Fields["window_pr_cycle_hours_median"].Number; got == nil || *got != 1 {
		t.Fatalf("window_pr_cycle_hours_median = %#v, want 1", fact.Fields["window_pr_cycle_hours_median"])
	}
	if got := fact.Fields["window_pr_count"].Integer; got == nil || *got != 3 {
		t.Fatalf("window_pr_count = %#v, want 3", fact.Fields["window_pr_count"])
	}
	for _, name := range []string{"day", "commits_count", "prs_merged", "median_pr_cycle_hours", "daily_metrics"} {
		if _, ok := fact.Fields[name]; ok {
			t.Fatalf("%s served without a daily row: %#v", name, fact.Fields[name])
		}
	}
}

func TestTeamWithoutOwnedRepositoryAndWithoutWellbeingRowServesNoWindowFact(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM team_metrics_daily", rows: nil},
		{match: "GROUP BY team_id, repo_key", rows: nil},
		{match: "FROM git_pull_requests", rows: [][]any{{"", int64(5), float64(7.5)}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want none for a team owning no repository", result.Facts)
	}
}

func TestTeamWithOwnedRepositoriesAndNoWindowEvidenceServesNoFact(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM team_metrics_daily", rows: nil},
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"team-1", "repo-a", "acme/a"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want none: no merged pull request, no change failure counts, no wellbeing row", result.Facts)
	}
}

func TestTeamWithoutOwnedRepositoryAndWithoutWellbeingRowNamesTheReason(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM team_metrics_daily", rows: nil},
		{match: "GROUP BY team_id, repo_key", rows: nil},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != contextfabric.SourceNotApplicable {
		t.Fatalf("state = %q, want %q for a team that owns no repository", result.State, contextfabric.SourceNotApplicable)
	}
	if !strings.Contains(result.Reason, "owns no repositor") {
		t.Fatalf("reason = %q, want the no-owned-repositories reason", result.Reason)
	}
}
