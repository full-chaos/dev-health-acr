package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A deployments fact and a metrics fact count failed deployments on different
// bases. Each served failed count names its basis, so two counts never appear
// without one.

func readDeployments(t *testing.T, subject contextfabric.SubjectRef, tables ...fakeTable) contextfabric.CanonicalFact {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), contextfabric.FactDeployments)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactDeployments, Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	return result.Facts[0]
}

func requireInteger(t *testing.T, fact contextfabric.CanonicalFact, name string, want int64) {
	t.Helper()
	if v := fact.Fields[name]; v.Integer == nil || *v.Integer != want {
		t.Fatalf("%s = %#v, want %d", name, v, want)
	}
}

func TestRepositoryDeploymentsFactNamesTheStatusBasisOfItsFailedCount(t *testing.T) {
	t.Parallel()
	fact := readDeployments(t, repoSubject("repo-1"), fakeTable{match: "FROM deploy_metrics_daily", rows: [][]any{deployMetricsRow("repo-1")}})
	requireInteger(t, fact, "failed_deployments_count", 2)
	requireString(t, fact, "failed_deployments_basis", "deployment_status")
}

func TestTeamDeploymentsFactNamesTheStatusBasisOfItsFailedCount(t *testing.T) {
	t.Parallel()
	fact := readDeployments(t, teamSubject("CHAOS"),
		fakeTable{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "repo-a", "acme/a"}, {"CHAOS", "repo-b", "acme/b"}}},
		fakeTable{match: "FROM deploy_metrics_daily", rows: [][]any{
			{"repo-a", int64(6), int64(2), int64(3), "2026-10-12", uint8(0), float64(0), uint8(0), float64(0)},
			{"repo-b", int64(4), int64(0), int64(2), "2026-10-11", uint8(0), float64(0), uint8(0), float64(0)},
		}},
	)
	requireInteger(t, fact, "deployments_count_window", 10)
	requireInteger(t, fact, "failed_deployments_count_window", 2)
	requireString(t, fact, "failed_deployments_basis", "deployment_status")
}

func TestTeamDeploymentsFactWithNoDataNamesNoFailedBasis(t *testing.T) {
	t.Parallel()
	fact := readDeployments(t, teamSubject("CHAOS"),
		fakeTable{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "repo-a", "acme/a"}}},
	)
	requireAbsent(t, fact, "failed_deployments_count_window", "failed_deployments_basis")
}

// Every count differs (native 1, heuristic 5, status-only 2: failed 8,
// incident-linked 6), so a field fed from the wrong count fails.
func TestChangeFailureServesTheIncidentLinkedAndStatusOnlyCountsWithTheirBasis(t *testing.T) {
	t.Parallel()
	statusTables := func(failure []any) []fakeTable {
		return []fakeTable{{match: "system.columns", rows: [][]any{{uint64(2)}}}, {match: "FROM repo_change_failure_daily", rows: [][]any{failure}}}
	}
	cases := []struct {
		name    string
		subject contextfabric.SubjectRef
		tables  []fakeTable
	}{
		{"repository", repoSubject("repo-1"), append(statusTables(statusFailureRow("repo-1", 3, 10, 1, 5, 1, 0, 2, 10)),
			fakeTable{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}})},
		{"team", teamSubject("CHAOS"), append(statusTables(statusFailureRow("", 3, 10, 1, 5, 1, 0, 2, 10)),
			fakeTable{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "repo-a", "acme/a"}}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fact, _ := readFailure(t, tc.subject, tc.tables...)
			requireInteger(t, fact, "change_failure_failed_deployments_count", 8)
			requireInteger(t, fact, "change_failure_incident_linked_failed_deployments_count", 6)
			requireInteger(t, fact, "change_failure_status_failed_deployments_count", 2)
			requireString(t, fact, "change_failure_status_failed_deployments_basis", "status_failed_not_incident_linked")
		})
	}
}

func TestChangeFailureOldTableShapeServesTheIncidentLinkedCountAndNoStatusBasis(t *testing.T) {
	t.Parallel()
	fact, _ := readFailure(t, repoSubject("repo-1"), repoFailureTables(failureRow("repo-1", 3, 7, 1, 2, 1, 0))...)
	requireInteger(t, fact, "change_failure_failed_deployments_count", 3)
	requireInteger(t, fact, "change_failure_incident_linked_failed_deployments_count", 3)
	requireAbsent(t, fact, "change_failure_status_failed_deployments_count", "change_failure_status_failed_deployments_basis")
}
