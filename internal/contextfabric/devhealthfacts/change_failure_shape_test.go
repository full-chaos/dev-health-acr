package devhealthfacts_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func statusFailureRow(key string, stored, deployments, native, heuristic, direct, via, failedStatus, known int64) []any {
	return []any{key, stored, deployments, native, heuristic, direct, via, failedStatus, known}
}

func failureStatements(c *fakeClient) []string {
	var out []string
	for _, q := range c.queries {
		if strings.Contains(q.statement, "FROM repo_change_failure_daily") {
			out = append(out, q.statement)
		}
	}
	return out
}

func requireString(t *testing.T, fact contextfabric.CanonicalFact, name, want string) {
	t.Helper()
	if got := stringField(t, fact, name); got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func requireAbsent(t *testing.T, fact contextfabric.CanonicalFact, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, has := fact.Fields[name]; has {
			t.Fatalf("%s must be absent, got %#v", name, fact.Fields[name])
		}
	}
}

func TestChangeFailureOldTableShapeNamesNoStatusColumnAndServesTheReason(t *testing.T) {
	// No probe answer (the fake serves no system.columns row): the older
	// statement form, which the pre-migration table answers.
	fact, client := readFailure(t, repoSubject("repo-1"), repoFailureTables(failureRow("repo-1", 3, 4, 0, 0, 0, 0))...)
	for _, statement := range failureStatements(client) {
		if strings.Contains(statement, "failed_deployments_status") || strings.Contains(statement, "deployments_status_known") {
			t.Fatalf("old shape statement names a column the table does not have: %s", statement)
		}
	}
	requireString(t, fact, "change_failure_rate_state", "unknown_no_incident_evidence")
	requireString(t, fact, "change_failure_unknown_reason", "deployments_without_outcome")
	requireAbsent(t, fact, "change_failure_rate", "change_failure_evidence_basis",
		"change_failure_status_failed_deployments_count", "change_failure_status_known_deployments_count")
}

func TestChangeFailurePartialStatusColumnsStayOnTheOldForm(t *testing.T) {
	tables := append([]fakeTable{{match: "system.columns", rows: [][]any{{uint64(1)}}}},
		repoFailureTables(failureRow("repo-1", 3, 4, 0, 0, 0, 0))...)
	_, client := readFailure(t, repoSubject("repo-1"), tables...)
	for _, statement := range failureStatements(client) {
		if strings.Contains(statement, "failed_deployments_status") {
			t.Fatalf("one of two columns is not the new shape: %s", statement)
		}
	}
}

func TestChangeFailureNewTableShapeReadsAndServesTheStatusCounts(t *testing.T) {
	tables := append([]fakeTable{{match: "system.columns", rows: [][]any{{uint64(2)}}}},
		repoFailureTables(statusFailureRow("repo-1", 3, 4, 1, 0, 1, 0, 2, 3))...)
	fact, client := readFailure(t, repoSubject("repo-1"), tables...)
	statements := failureStatements(client)
	if len(statements) != 1 || !strings.Contains(statements[0], "sum(failed_deployments_status)") || !strings.Contains(statements[0], "sum(deployments_status_known)") {
		t.Fatalf("new shape statement does not read both columns: %v", statements)
	}
	for name, want := range map[string]int64{
		"change_failure_deployments_count":               4,
		"change_failure_failed_deployments_count":        3,
		"change_failure_status_failed_deployments_count": 2,
		"change_failure_status_known_deployments_count":  3,
	} {
		if v := fact.Fields[name]; v.Integer == nil || *v.Integer != want {
			t.Fatalf("%s = %#v, want %d", name, v, want)
		}
	}
	requireString(t, fact, "change_failure_rate_state", "measured")
	requireString(t, fact, "change_failure_evidence_basis", "both")
	if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.75 {
		t.Fatalf("rate = %#v, want (1 native + 2 status) / 4", v)
	}
	if v := fact.Fields["change_failure_status_coverage"]; v.Number == nil || *v.Number != 0.75 {
		t.Fatalf("coverage = %#v, want 3 known of 4", v)
	}
	requireString(t, fact, "change_failure_link_tier", "native")
	requireAbsent(t, fact, "change_failure_unknown_reason")
}

func TestChangeFailureUnknownReasonsAreThreeDistinctAnswers(t *testing.T) {
	// No deployments in the window: a stored row exists.
	fact, _ := readFailure(t, repoSubject("repo-1"), repoFailureTables(failureRow("repo-1", 2, 0, 0, 0, 1, 0))...)
	requireString(t, fact, "change_failure_rate_state", "not_applicable_no_deployments")
	requireString(t, fact, "change_failure_unknown_reason", "no_deployments_in_window")
	// Not collected: the repository has a metrics row and no stored count row.
	fact, _ = readFailure(t, repoSubject("repo-1"), repoFailureTables()...)
	requireAbsent(t, fact, "change_failure_rate_state", "change_failure_rate")
	requireString(t, fact, "change_failure_unknown_reason", "not_collected")
	// A measured rate serves no reason.
	fact, _ = readFailure(t, repoSubject("repo-1"), repoFailureTables(failureRow("repo-1", 1, 4, 1, 0, 1, 0))...)
	requireAbsent(t, fact, "change_failure_unknown_reason")
	requireString(t, fact, "change_failure_evidence_basis", "incident")
}

func TestChangeFailureStatusAloneMeasuresWithoutAnIncident(t *testing.T) {
	tables := append([]fakeTable{{match: "system.columns", rows: [][]any{{uint64(2)}}}},
		repoFailureTables(statusFailureRow("repo-1", 2, 4, 0, 0, 0, 0, 0, 4))...)
	fact, _ := readFailure(t, repoSubject("repo-1"), tables...)
	requireString(t, fact, "change_failure_rate_state", "measured")
	requireString(t, fact, "change_failure_evidence_basis", "status")
	if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0 {
		t.Fatalf("all-success by status = %#v, want a measured 0", v)
	}
	requireAbsent(t, fact, "change_failure_unknown_reason", "change_failure_link_tier")
}

func TestChangeFailureOldWriterRowWithNoKnownStatusIsUnknownNotAZero(t *testing.T) {
	// Both status columns at their default on a row with deployments.
	tables := append([]fakeTable{{match: "system.columns", rows: [][]any{{uint64(2)}}}},
		repoFailureTables(statusFailureRow("repo-1", 1, 4, 0, 0, 0, 0, 0, 0))...)
	fact, _ := readFailure(t, repoSubject("repo-1"), tables...)
	requireString(t, fact, "change_failure_rate_state", "unknown_no_incident_evidence")
	requireString(t, fact, "change_failure_unknown_reason", "deployments_without_outcome")
	requireAbsent(t, fact, "change_failure_rate", "change_failure_evidence_basis")
}

func TestChangeFailureStatusOnlyWindowDividesByTheKnownDaysAndShowsCoverage(t *testing.T) {
	// Day 1 by an old pod: D 4, K 0. Day 2: D 2, K 2, one failed. Summed: D 6, K 2.
	tables := append([]fakeTable{{match: "system.columns", rows: [][]any{{uint64(2)}}}},
		repoFailureTables(statusFailureRow("repo-1", 2, 6, 0, 0, 0, 0, 1, 2))...)
	fact, _ := readFailure(t, repoSubject("repo-1"), tables...)
	requireString(t, fact, "change_failure_evidence_basis", "status")
	if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.5 {
		t.Fatalf("rate = %#v, want 1 over K = 2", v)
	}
	if v := fact.Fields["change_failure_status_coverage"]; v.Number == nil || *v.Number < 0.333 || *v.Number > 0.334 {
		t.Fatalf("coverage = %#v, want 2 of 6", v)
	}
}
