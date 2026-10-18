package devhealthfacts_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const commitmentDueDatesMatch = "AS commitment_due_dates"

func commitmentDueDateRow(provider, projectID, workItemID, status string, due time.Time) []any {
	return []any{provider + ":" + projectID, workItemID, status, due.UnixMilli()}
}

func commitmentClientWithDueDates(projects, items, dueDates [][]any) *fakeClient {
	client := commitmentClient(projects, items)
	client.tables[0].rows = dueDates
	return client
}

type servedDueDate struct{ id, dueAt, status, pastDue string }

func servedDueDates(t *testing.T, fact directread.ServedFact) []servedDueDate {
	t.Helper()
	table, ok := fact.Tables["issue_due_dates"]
	if !ok {
		raw, _ := json.Marshal(fact)
		t.Fatalf("no issue_due_dates table served: %s", raw)
	}
	index := map[string]int{}
	for i, column := range table.Columns {
		index[column] = i
	}
	for _, column := range []string{"work_item_id", "due_at", "status", "past_due"} {
		if _, present := index[column]; !present {
			t.Fatalf("issue_due_dates columns = %v, want %q", table.Columns, column)
		}
	}
	out := make([]servedDueDate, 0, len(table.Rows))
	for _, row := range table.Rows {
		out = append(out, servedDueDate{
			id: fmt.Sprint(row[index["work_item_id"]]), dueAt: fmt.Sprint(row[index["due_at"]]),
			status: fmt.Sprint(row[index["status"]]), pastDue: fmt.Sprint(row[index["past_due"]]),
		})
	}
	return out
}

func TestCommitmentDueDatesListPastDueOpenIssuesFirstThenTheRestByDueDate(t *testing.T) {
	project := projectSubject("linear", "proj-d")
	day := func(d int) time.Time { return time.Date(2026, 10, d, 9, 30, 0, 0, time.UTC) }
	client := commitmentClientWithDueDates(
		[][]any{commitmentProjectRow("linear", "proj-d", "started", "linear.project.state", "", true)},
		[][]any{commitmentItemRow("linear", "proj-d", commitmentCounts{total: 5, open: 4, closed: 1, withDueAt: 5, pastDueOpen: 2})},
		[][]any{
			commitmentDueDateRow("linear", "proj-d", "i-future-late", "in_progress", day(30)),
			commitmentDueDateRow("linear", "proj-d", "i-closed-old", "done", day(1)),
			commitmentDueDateRow("linear", "proj-d", "i-past-recent", "todo", day(12)),
			commitmentDueDateRow("linear", "proj-d", "i-future-soon", "blocked", day(14)),
			commitmentDueDateRow("linear", "proj-d", "i-past-old", "in_review", day(2)),
		},
	)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	got := servedDueDates(t, fact)
	want := []servedDueDate{
		{"i-past-old", "2026-10-02T09:30:00Z", "in_review", "true"},
		{"i-past-recent", "2026-10-12T09:30:00Z", "todo", "true"},
		{"i-closed-old", "2026-10-01T09:30:00Z", "done", "false"},
		{"i-future-soon", "2026-10-14T09:30:00Z", "blocked", "false"},
		{"i-future-late", "2026-10-30T09:30:00Z", "in_progress", "false"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	wantServed(t, fact, map[string]any{"issue_due_dates_truncated": false})
}

func TestCommitmentDueDatesCapAtTwentyFiveAndNameTheTruncation(t *testing.T) {
	project := projectSubject("jira", "proj-cap")
	due := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var dated [][]any
	for i := 0; i < 26; i++ {
		dated = append(dated, commitmentDueDateRow("jira", "proj-cap", fmt.Sprintf("i-%02d", i), "todo", due.AddDate(0, 0, i)))
	}
	dated = append(dated, commitmentDueDateRow("jira", "proj-cap", "i-past", "todo", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
	client := commitmentClientWithDueDates(
		[][]any{commitmentProjectRow("jira", "proj-cap", "", "", "", true)},
		[][]any{commitmentItemRow("jira", "proj-cap", commitmentCounts{total: 40, open: 40, withDueAt: 27, pastDueOpen: 1})},
		dated,
	)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	got := servedDueDates(t, fact)
	if len(got) != 25 {
		t.Fatalf("rows = %d, want the cap of 25", len(got))
	}
	if got[0].id != "i-past" {
		t.Errorf("first row = %+v, want the past due open issue kept ahead of the cut", got[0])
	}
	if got[24].id != "i-23" {
		t.Errorf("last kept row = %+v, want the 24th later-dated issue", got[24])
	}
	wantServed(t, fact, map[string]any{"issue_due_dates_truncated": true})
}

func TestCommitmentDueDatesAtTheCapAreNotTruncated(t *testing.T) {
	project := projectSubject("gitlab", "proj-exact")
	due := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var dated [][]any
	for i := 0; i < 25; i++ {
		dated = append(dated, commitmentDueDateRow("gitlab", "proj-exact", fmt.Sprintf("i-%02d", i), "todo", due.AddDate(0, 0, i)))
	}
	client := commitmentClientWithDueDates(
		[][]any{commitmentProjectRow("gitlab", "proj-exact", "", "", "", true)},
		[][]any{commitmentItemRow("gitlab", "proj-exact", commitmentCounts{total: 25, open: 25, withDueAt: 25})},
		dated,
	)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	if got := servedDueDates(t, fact); len(got) != 25 {
		t.Fatalf("rows = %d, want 25", len(got))
	}
	wantServed(t, fact, map[string]any{"issue_due_dates_truncated": false})
}

func TestCommitmentDueDatePastDueNeedsAKnownOpenStatusAndADayBeforeTheRequestDay(t *testing.T) {
	project := projectSubject("jira", "proj-pd")
	client := commitmentClientWithDueDates(
		[][]any{commitmentProjectRow("jira", "proj-pd", "", "", "", true)},
		[][]any{commitmentItemRow("jira", "proj-pd", commitmentCounts{total: 5, open: 2, closed: 1, unknown: 2, withDueAt: 5, pastDueOpen: 1})},
		[][]any{
			commitmentDueDateRow("jira", "proj-pd", "i-open-before", "in_progress", time.Date(2026, 10, 12, 23, 59, 0, 0, time.UTC)),
			commitmentDueDateRow("jira", "proj-pd", "i-open-today", "todo", time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)),
			commitmentDueDateRow("jira", "proj-pd", "i-done-before", "done", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
			commitmentDueDateRow("jira", "proj-pd", "i-unknown-before", "unknown", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
			commitmentDueDateRow("jira", "proj-pd", "i-canceled-before", "canceled", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
		},
	)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	pastDue := map[string]string{}
	for _, row := range servedDueDates(t, fact) {
		pastDue[row.id] = row.pastDue
	}
	for id, want := range map[string]string{
		"i-open-before": "true", "i-open-today": "false", "i-done-before": "false", "i-unknown-before": "false", "i-canceled-before": "false",
	} {
		if pastDue[id] != want {
			t.Errorf("%s past_due = %q, want %q", id, pastDue[id], want)
		}
	}
}

func TestCommitmentWithoutDatedIssuesServesNoDueDateTable(t *testing.T) {
	project := projectSubject("linear", "proj-nd")
	client := commitmentClientWithDueDates(
		[][]any{commitmentProjectRow("linear", "proj-nd", "", "", "2026-11-01", true)},
		[][]any{commitmentItemRow("linear", "proj-nd", commitmentCounts{total: 2, open: 2})},
		nil,
	)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	if _, present := fact.Tables["issue_due_dates"]; present {
		t.Errorf("a project with no dated issue serves an issue_due_dates table")
	}
	wantAbsent(t, fact, "issue_due_dates", "issue_due_dates_truncated")
}

func TestCommitmentDueDateStatementIsBoundedAndReadsTheSameAuthorizedItems(t *testing.T) {
	client := commitmentClientWithDueDates(
		[][]any{commitmentProjectRow("linear", "proj-l", "", "", "", true)},
		[][]any{commitmentItemRow("linear", "proj-l", commitmentCounts{total: 1, open: 1, withDueAt: 1})},
		[][]any{commitmentDueDateRow("linear", "proj-l", "i-1", "todo", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))},
	)
	readCommitmentFacts(t, client, projectSubject("linear", "proj-l"))
	var statement string
	for _, query := range client.queries {
		if strings.Contains(query.statement, commitmentDueDatesMatch) {
			statement = query.statement
		}
	}
	if statement == "" {
		t.Fatal("no due date statement was read")
	}
	assertQueryScopedToOrgAndSubjects(t, statement)
	if fields := strings.Fields(statement); fields[0] != "SELECT" {
		t.Errorf("statement does not start with SELECT:\n%s", statement)
	}
	authorized := devhealthfacts.CommitmentAuthorizedItemIDsSQLForTest(storage.Principal{OrgID: "org-commitment"}, nil)
	for _, fragment := range []string{
		"FROM project_rows\n  WHERE work_item_id IN " + authorized,
		"due_at IS NOT NULL",
		"LIMIT 26 BY project_key",
		"ifNull(w.type, '') NOT IN ('pr', 'merge_request')",
	} {
		if !strings.Contains(statement, fragment) {
			t.Errorf("due date statement lacks %q:\n%s", fragment, statement)
		}
	}
}

func TestCommitmentDueDatesCarryNoJudgmentWord(t *testing.T) {
	project := projectSubject("linear", "proj-w")
	client := commitmentClientWithDueDates(
		[][]any{commitmentProjectRow("linear", "proj-w", "", "", "", true)},
		[][]any{commitmentItemRow("linear", "proj-w", commitmentCounts{total: 1, open: 1, withDueAt: 1, pastDueOpen: 1})},
		[][]any{commitmentDueDateRow("linear", "proj-w", "i-1", "todo", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))},
	)
	fact := servedCommitment(t, readCommitmentFacts(t, client, project), project)
	names := []string{}
	for name := range fact.Fields {
		names = append(names, name)
	}
	for tableName, table := range fact.Tables {
		names = append(names, tableName)
		names = append(names, table.Columns...)
	}
	judgment := map[string]bool{"overdue": true, "late": true, "behind": true, "slip": true, "slipped": true, "risk": true, "delayed": true, "missed": true, "breach": true, "verdict": true, "score": true}
	for _, name := range names {
		for _, token := range strings.Split(name, "_") {
			if judgment[token] {
				t.Errorf("served commitment fact carries the judgment word %q in %q", token, name)
			}
		}
	}
	if _, present := fact.Tables["issue_due_dates"]; !present {
		t.Error("no issue_due_dates table served, so the wording check read nothing")
	}
}
