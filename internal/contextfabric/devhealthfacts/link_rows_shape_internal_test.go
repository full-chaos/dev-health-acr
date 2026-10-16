package devhealthfacts

import (
	"strings"
	"testing"
)

func TestAdmittedLinkedPullRequestRowsReadNoWorkItems(t *testing.T) {
	fragment := nativeFirstLinkedPullRequestRowsAmongSQL("(SELECT 1)")
	if strings.Contains(fragment, "work_items") {
		t.Fatalf("the shared link-row fragment reads work_items, want a join key only:\n%s", fragment)
	}
	for _, want := range []string{"work_graph_issue_pr FINAL", "git_pull_requests AS pr FINAL", linkedPullRequestOnSQL, linkTierRankSQL + " > 0", "l.org_id = {org_id:String}", "provenance = 'native'"} {
		if !strings.Contains(fragment, want) {
			t.Fatalf("the shared link-row fragment lacks %q:\n%s", want, fragment)
		}
	}
}

func TestTeamProjectLinkRowsReadWorkItemsForTheOwnedProjectsInTwoPasses(t *testing.T) {
	statement := teamProjectLinkRowsSQL("ids", factTimeBound{})
	if got := strings.Count(statement, "FROM work_items"); got != 2 {
		t.Fatalf("team project link rows read work_items %d times, want 2 (unmerged candidates, then FINAL):\n%s", got, statement)
	}
	for _, want := range []string{"work_graph_issue_pr FINAL", "provenance = 'native'", "cp.work_item_id IN " + linkedWorkItemIDsSQL, "cw.work_item_id IN (", "cw.project_id IN (SELECT v FROM", "cp.project_id IN (SELECT v FROM", workItemIsIssueSQL("cw")} {
		if !strings.Contains(statement, want) {
			t.Fatalf("team project link rows lack %q:\n%s", want, statement)
		}
	}
}
