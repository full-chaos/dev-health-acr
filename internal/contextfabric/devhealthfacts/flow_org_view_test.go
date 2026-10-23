package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readFlowStatements(t *testing.T, subjects []contextfabric.SubjectRef, tables []fakeTable) []string {
	t.Helper()
	client := &fakeClient{tables: tables}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: subjects,
	}); err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	statements := make([]string, 0, len(client.queries))
	for _, q := range client.queries {
		statements = append(statements, q.statement)
	}
	return statements
}

// CHAOS-8863: every statement of the flow fact that reads work_item_metrics_daily takes its view of the table, and
// the team read issues exactly the statements it issued before (ClickHouse round trips of the turn chain do not
// grow): the team scope read answers the scope view and the org view in ONE statement.
func TestFlowTeamReadTakesItsViewsInTheStatementsItAlreadyIssued(t *testing.T) {
	t.Parallel()
	statements := readFlowStatements(t, []contextfabric.SubjectRef{teamSubject("team-1")}, []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4)}},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("team-1", "2026-02-21", 15, 9, 4)}},
	})
	const statementsBeforeThisChange = 2
	if len(statements) != statementsBeforeThisChange {
		t.Fatalf("the team flow read issues %d statements, want %d (the same as before the views)", len(statements), statementsBeforeThisChange)
	}
	var scopeRows, series int
	for _, statement := range statements {
		if !strings.Contains(statement, "FROM work_item_metrics_daily") {
			continue
		}
		switch {
		case strings.Contains(statement, "rn_scope") && strings.Contains(statement, "rn_org"):
			scopeRows++
			for _, want := range []string{
				"row_view IN ('', 'org', 'scope')",
				"(row_view IN ('', 'scope')), work_scope_id ORDER BY day DESC, (row_view = 'scope') DESC, computed_at DESC",
				"(row_view IN ('', 'org')), work_scope_id ORDER BY day DESC, (row_view = 'org') DESC, computed_at DESC",
				"WHERE (rn_scope = 1 AND row_view IN ('', 'scope')) OR (rn_org = 1 AND row_view IN ('', 'org'))",
			} {
				if !strings.Contains(statement, want) {
					t.Errorf("the scope read lacks %q:\n%s", want, statement)
				}
			}
		default:
			series++
			for _, want := range []string{"row_view IN ('', 'org')", "ORDER BY (row_view = 'org') DESC, computed_at DESC"} {
				if !strings.Contains(statement, want) {
					t.Errorf("the daily series lacks %q:\n%s", want, statement)
				}
			}
		}
	}
	if scopeRows != 1 || series != 1 {
		t.Fatalf("scope reads %d, series %d, want one of each", scopeRows, series)
	}
}

// The scope breakdown rows and the latest-day totals of one team come from different rows of the same statement: the
// breakdown holds the scope-view row of each work scope, the totals sum the org-view rows, each item once.
func TestFlowTeamTotalsSumTheOrgRowsAndTheBreakdownKeepsTheScopeRows(t *testing.T) {
	t.Parallel()
	// Two work scopes hold one item: the scope view counts it in each (3 + 3 started), the org view once (3 + 0).
	scopeA := append(workItemMetricsDailyRow("team-1", "scope-a", 3, 1, 2), uint8(1), uint8(1)) // scope row and org row
	scopeB := append(workItemMetricsDailyRow("team-1", "scope-b", 3, 1, 2), uint8(1), uint8(0)) // scope row only
	scopeBOrg := append(workItemMetricsDailyRow("team-1", "scope-b", 0, 0, 0), uint8(0), uint8(1))
	client := &fakeClient{tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{scopeA, scopeB, scopeBOrg}},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("team-1", "2026-02-21", 3, 1, 4)}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("ReadFacts() = %v, %v", result.Facts, err)
	}
	fields := result.Facts[0].Fields
	if got := *fields["items_started_latest_day"].Integer; got != 3 {
		t.Errorf("items_started_latest_day = %d, want 3 (each item once: the org rows)", got)
	}
	if got := *fields["scope_count"].Integer; got != 2 {
		t.Errorf("scope_count = %d, want 2 (one row per work scope: the scope rows)", got)
	}
	if rows := fields["scope_breakdown"].Rows; len(rows) != 2 {
		t.Errorf("scope_breakdown rows = %d, want 2", len(rows))
	}
}

// The project statements read the org view: a project sums its teams.
func TestFlowProjectReadsTakeTheOrgView(t *testing.T) {
	t.Parallel()
	statements := readFlowStatements(t, []contextfabric.SubjectRef{projectSubject("linear", "proj-1")}, []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{projectWorkItemMetricsRow("linear", "proj-1", "team-1", "scope-a", 10, 6)}},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("linear:proj-1", "2026-02-21", 15, 9, 4)}},
	})
	seen := 0
	for _, statement := range statements {
		if !strings.Contains(statement, "FROM work_item_metrics_daily") {
			continue
		}
		seen++
		if !strings.Contains(statement, "row_view IN ('', 'org')") || !strings.Contains(statement, "(row_view = 'org') DESC") {
			t.Errorf("a project statement lacks the org view:\n%s", statement)
		}
	}
	if seen == 0 {
		t.Fatal("no project statement read work_item_metrics_daily: the test measured nothing")
	}
}
