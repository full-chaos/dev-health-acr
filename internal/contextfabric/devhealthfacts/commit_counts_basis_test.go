package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The team and project commit counts are one day (the latest) at the owner's
// share, so each fact that serves them names that basis. The repository fact
// is not share-weighted and does not carry the field.

func readMetricsFact(t *testing.T, subject contextfabric.SubjectRef, table fakeTable) contextfabric.CanonicalFact {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: []fakeTable{table}}), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	return result.Facts[0]
}

func TestTeamMetricsFactNamesTheOwnerShareBasisOfItsCommitCounts(t *testing.T) {
	t.Parallel()
	fact := readMetricsFact(t, teamSubject("team-1"), fakeTable{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("team-1")}})
	requireString(t, fact, "commit_counts_basis", "owner_share_weighted_latest_day")
	if fact.Fields["commits_count"].Number == nil {
		t.Fatalf("commits_count missing beside the basis: %#v", fact.Fields)
	}
}

func TestProjectRollupFactNamesTheOwnerShareBasisOfItsCommitCounts(t *testing.T) {
	t.Parallel()
	fact := readMetricsFact(t, projectSubject("linear", "proj-1"), fakeTable{match: "FROM team_project_ownership", rows: [][]any{
		projectRollupRow("linear", "proj-1", "team-1", "Team One", 0.3, 0.1, 0.2, 0.2, 0.1),
	}})
	requireString(t, fact, "commit_counts_basis", "owner_share_weighted_latest_day")
	requireString(t, fact, "rollup_basis", "team_project_ownership_sum")
	if fact.Fields["commits_count"].Number == nil {
		t.Fatalf("commits_count missing beside the basis: %#v", fact.Fields)
	}
}

func TestRepositoryMetricsFactCarriesNoCommitCountsBasis(t *testing.T) {
	t.Parallel()
	fact := readMetricsFact(t, repoSubject("repo-1"), fakeTable{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}})
	requireAbsent(t, fact, "commit_counts_basis")
}

func TestNoTeamOrProjectMetricsFactMeansNoCommitCountsBasis(t *testing.T) {
	t.Parallel()
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: []fakeTable{{match: "FROM team_metrics_daily", rows: nil}}}), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	for _, fact := range result.Facts {
		requireAbsent(t, fact, "commit_counts_basis")
	}
}
