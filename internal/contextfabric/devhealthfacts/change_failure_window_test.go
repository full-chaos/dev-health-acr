package devhealthfacts_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// failureRow is one statement row: key, stored rows, deployments, failed
// native, failed heuristic, incidents direct, incidents via deployment.
func failureRow(key string, stored, deployments, native, heuristic, direct, via int64) []any {
	return []any{key, stored, deployments, native, heuristic, direct, via}
}

func readFailure(t *testing.T, subject contextfabric.SubjectRef, tables ...fakeTable) (contextfabric.CanonicalFact, *fakeClient) {
	t.Helper()
	client := &fakeClient{tables: tables}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
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
	return result.Facts[0], client
}

func repoFailureTables(failure ...[]any) []fakeTable {
	return []fakeTable{
		{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}},
		{match: "FROM repo_change_failure_daily", rows: failure},
	}
}

func stringField(t *testing.T, fact contextfabric.CanonicalFact, name string) string {
	t.Helper()
	v, ok := fact.Fields[name]
	if !ok || v.String == nil {
		t.Fatalf("field %s missing or not a string: %#v", name, v)
	}
	return *v.String
}

func TestRepositoryChangeFailureIsTheSumOfStoredCountsNotAnAverageOfDays(t *testing.T) {
	// 3 stored days: 4 deployments, 1 native + 1 heuristic failed = 0.5. The
	// deprecated repo_metrics_daily value (0.1 in the row) is never served.
	fact, _ := readFailure(t, repoSubject("repo-1"), repoFailureTables(failureRow("repo-1", 3, 4, 1, 1, 1, 0))...)
	if got := stringField(t, fact, "change_failure_rate_state"); got != "measured" {
		t.Fatalf("state = %q", got)
	}
	if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.5 {
		t.Fatalf("change_failure_rate = %#v, want 0.5", v)
	}
	if v := fact.Fields["change_failure_deployments_count"]; v.Integer == nil || *v.Integer != 4 {
		t.Fatalf("deployments = %#v", v)
	}
	if v := fact.Fields["change_failure_failed_deployments_count"]; v.Integer == nil || *v.Integer != 2 {
		t.Fatalf("failed = %#v", v)
	}
	if got := stringField(t, fact, "change_failure_link_tier"); got != "heuristic" {
		t.Fatalf("link tier = %q", got)
	}
}

func TestRepositoryChangeFailureStatesNeverServeAZeroRate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rows  [][]any
		state string
	}{
		{"no deployments in the window", [][]any{failureRow("repo-1", 2, 0, 0, 0, 1, 0)}, "not_applicable_no_deployments"},
		{"deployments without incident evidence", [][]any{failureRow("repo-1", 2, 5, 0, 0, 0, 0)}, "unknown_no_incident_evidence"},
		{"no stored row", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact, _ := readFailure(t, repoSubject("repo-1"), repoFailureTables(tc.rows...)...)
			if tc.state == "" {
				for _, name := range []string{"change_failure_rate_state", "change_failure_rate", "change_failure_deployments_count", "change_failure_failed_deployments_count"} {
					if _, ok := fact.Fields[name]; ok {
						t.Fatalf("%s served for a view with no stored row", name)
					}
				}
				return
			}
			if got := stringField(t, fact, "change_failure_rate_state"); got != tc.state {
				t.Fatalf("state = %q, want %q", got, tc.state)
			}
			if _, ok := fact.Fields["change_failure_rate"]; ok {
				t.Fatalf("a rate was served beside state %s: %#v", tc.state, fact.Fields["change_failure_rate"])
			}
		})
	}
}

func TestRepositoryMeasuredZeroIsServedAsZero(t *testing.T) {
	fact, _ := readFailure(t, repoSubject("repo-1"), repoFailureTables(failureRow("repo-1", 2, 5, 0, 0, 1, 0))...)
	if v, ok := fact.Fields["change_failure_rate"]; !ok || v.Number == nil || *v.Number != 0 {
		t.Fatalf("a measured zero must be served: %#v", v)
	}
	if _, ok := fact.Fields["change_failure_link_tier"]; ok {
		t.Fatal("no deployment failed: no link tier")
	}
}

func TestChangeFailureStatementReadsTheStoredCountsOfTheWindowScopedToTheOrganization(t *testing.T) {
	_, client := readFailure(t, repoSubject("repo-1"), repoFailureTables(failureRow("repo-1", 1, 1, 0, 0, 1, 0))...)
	var statement string
	for _, q := range client.queries {
		if strings.Contains(q.statement, "repo_change_failure_daily") {
			statement = q.statement
		}
	}
	for _, want := range []string{"FROM repo_change_failure_daily FINAL", "org_id = {org_id:String}", "toString(repo_id) IN {ids:Array(String)}", "toDate({", "GROUP BY repo_id", "sum(deployments_count)", "sum(failed_deployments_native)", "sum(failed_deployments_heuristic)", "sum(incidents_direct)", "sum(incidents_via_deployment)"} {
		if !strings.Contains(statement, want) {
			t.Fatalf("statement lacks %q:\n%s", want, statement)
		}
	}
	for _, q := range client.queries {
		if strings.Contains(q.statement, "repo_metrics_daily") && strings.Contains(q.statement, "change_failure_rate") {
			t.Fatalf("the deprecated column is still read:\n%s", q.statement)
		}
	}
}

func TestTeamChangeFailureIsOverTheUnionOfOwnedRepositories(t *testing.T) {
	team := teamSubject("CHAOS")
	tables := []fakeTable{
		{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("CHAOS")}},
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "repo-a", "acme/a"}, {"CHAOS", "repo-b", "acme/b"}}},
		{match: "FROM repo_change_failure_daily", rows: [][]any{failureRow("", 4, 10, 2, 1, 1, 1)}},
	}
	fact, client := readFailure(t, team, tables...)
	if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.3 {
		t.Fatalf("team change_failure_rate = %#v, want 0.3 (3 failed of 10 deployments across both repositories)", v)
	}
	if got := stringField(t, fact, "change_failure_rate_state"); got != "measured" {
		t.Fatalf("state = %q", got)
	}
	var ids []string
	for _, q := range client.queries {
		if strings.Contains(q.statement, "repo_change_failure_daily") {
			for _, b := range q.bindings {
				if b.Name == "ids" {
					ids, _ = b.Value.([]string)
				}
			}
			if strings.Contains(q.statement, "GROUP BY") {
				t.Fatalf("a team rate must be over the union, not grouped per repository:\n%s", q.statement)
			}
		}
	}
	if len(ids) != 2 {
		t.Fatalf("owned repository ids bound = %v, want both owned repositories", ids)
	}
}

func TestTeamWithNoOwnedRepositoryServesNoChangeFailureState(t *testing.T) {
	tables := []fakeTable{{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("CHAOS")}}}
	fact, client := readFailure(t, teamSubject("CHAOS"), tables...)
	if _, ok := fact.Fields["change_failure_rate_state"]; ok {
		t.Fatal("a team that owns nothing has nothing to count")
	}
	for _, q := range client.queries {
		if strings.Contains(q.statement, "repo_change_failure_daily") {
			t.Fatal("a read with no owned repository was issued")
		}
	}
}

// An ungrouped aggregate returns one all-zero row when nothing is stored: the
// team has no stored count, so it has no state at all (the ops rule: no state).
func TestTeamWithNoStoredCountsHasNoStateOfItsOwn(t *testing.T) {
	tables := []fakeTable{
		{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("CHAOS")}},
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "repo-a", "acme/a"}}},
		{match: "FROM repo_change_failure_daily", rows: [][]any{failureRow("", 0, 0, 0, 0, 0, 0)}},
	}
	fact, _ := readFailure(t, teamSubject("CHAOS"), tables...)
	for _, name := range []string{"change_failure_rate_state", "change_failure_rate", "change_failure_deployments_count", "change_failure_failed_deployments_count"} {
		if _, ok := fact.Fields[name]; ok {
			t.Fatalf("%s served for a view with no stored count", name)
		}
	}
}

// repo_change_failure_daily holds days that repo_metrics_daily and
// team_metrics_daily may have no row for; the rate is still served.
func TestRepositoryChangeFailureIsServedWithoutARepoMetricsRow(t *testing.T) {
	tables := []fakeTable{{match: "FROM repo_change_failure_daily", rows: [][]any{failureRow("repo-1", 1, 4, 1, 0, 1, 0)}}}
	fact, _ := readFailure(t, repoSubject("repo-1"), tables...)
	if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.25 {
		t.Fatalf("rate = %#v, want 0.25", v)
	}
	if got := stringField(t, fact, "change_failure_rate_state"); got != "measured" {
		t.Fatalf("state = %q", got)
	}
	if _, ok := fact.Fields["daily_metrics"]; ok {
		t.Fatal("no series exists for this repository")
	}
}

func TestTeamChangeFailureIsServedWithoutATeamMetricsRow(t *testing.T) {
	tables := []fakeTable{
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "repo-a", "acme/a"}}},
		{match: "FROM repo_change_failure_daily", rows: [][]any{failureRow("", 2, 4, 2, 0, 1, 0)}},
	}
	fact, _ := readFailure(t, teamSubject("CHAOS"), tables...)
	if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.5 {
		t.Fatalf("rate = %#v, want 0.5", v)
	}
}

// One window serves the rate and the pull request cycle read: a default
// trailing window is resolved once, so both statements bind the same days.
func TestChangeFailureAndPRCycleReadsBindTheSameWindow(t *testing.T) {
	_, client := readFailure(t, repoSubject("repo-1"), repoFailureTables(failureRow("repo-1", 1, 4, 1, 0, 1, 0))...)
	bound := func(match string) map[string]any {
		for _, q := range client.queries {
			if strings.Contains(q.statement, match) {
				out := map[string]any{}
				for _, b := range q.bindings {
					if strings.HasPrefix(b.Name, "rollup_") || strings.HasPrefix(b.Name, "window_") {
						out[b.Name] = b.Value
					}
				}
				return out
			}
		}
		t.Fatalf("no statement over %s", match)
		return nil
	}
	cycle, failure := bound("FROM git_pull_requests"), bound("FROM repo_change_failure_daily")
	if len(cycle) == 0 || !reflect.DeepEqual(cycle, failure) {
		t.Fatalf("window bindings differ: cycle %v, failure %v", cycle, failure)
	}
}
