package devhealthsource

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestIssuePullRequestLinkStatementShape pins the SQL the producer sends: the
// link table is read FINAL, the issue end joins work_items on (org, work item)
// and is keyed by the ISSUE's repo_id (w.repo_id, not l.repo_id), the pull
// request end joins on the row's repo_id (the pull request's) and number, and
// the cursor pages on last_synced.
func TestIssuePullRequestLinkStatementShape(t *testing.T) {
	t.Parallel()
	rec := &statementRecorder{}
	cursor := cursorState{Since: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), After: "k", Space: cursorSpaceIngest}
	if _, _, err := queryIssuePullRequestLinks(context.Background(), rec, "org-1", cursor, 10); !errors.Is(err, errStatementRecorded) {
		t.Fatalf("err = %v", err)
	}
	statement := rec.statements[0]
	for _, fragment := range []string{
		"FROM work_graph_issue_pr AS l FINAL",
		"LEFT JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id",
		"LEFT JOIN git_pull_requests AS p FINAL ON p.org_id = l.org_id AND p.repo_id = l.repo_id AND p.number = l.pr_number",
		"LEFT JOIN repos AS r FINAL ON r.id = l.repo_id AND r.org_id = l.org_id",
		"toString(w.repo_id)",
		"WHERE l.org_id = {org_id:String}",
	} {
		if !strings.Contains(statement, fragment) {
			t.Errorf("statement lacks %q:\n%s", fragment, statement)
		}
	}
	if got := cursorExpression(t, "work_graph_issue_pr", statement); got != "l.last_synced" {
		t.Errorf("pages on %q, want l.last_synced", got)
	}
}
