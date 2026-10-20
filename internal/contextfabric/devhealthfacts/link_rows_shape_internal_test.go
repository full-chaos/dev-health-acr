package devhealthfacts

import (
	"strings"
	"testing"
)

func TestAdmittedLinkedPullRequestRowsReadNoWorkItems(t *testing.T) {
	fragment := admittedLinkedPullRequestRowsSQL
	if strings.Contains(fragment, "work_items") {
		t.Fatalf("the shared link-row fragment reads work_items, want a join key only:\n%s", fragment)
	}
	for _, want := range []string{"work_graph_issue_pr AS l FINAL", "git_pull_requests AS pr FINAL", linkedPullRequestOnSQL, linkTierRankSQL + " > 0", "l.org_id = {org_id:String}"} {
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
	for _, want := range []string{"cp.work_item_id IN " + linkedWorkItemIDsSQL, "cw.work_item_id IN (", "cw.project_id IN (SELECT v FROM", "cp.project_id IN (SELECT v FROM", workItemIsIssueSQL("cw")} {
		if !strings.Contains(statement, want) {
			t.Fatalf("team project link rows lack %q:\n%s", want, statement)
		}
	}
}

// The join order holds only the owned side in memory: the organization's pull
// requests stream on the left of the outer join, its link rows on the left of
// the inner one, and the planner may not swap the sides.
func TestTeamProjectLinkRowsStreamTheOrganizationPastTheOwnedSide(t *testing.T) {
	statement := teamProjectLinkRowsSQL("ids", factTimeBound{})
	order := []string{
		"FROM git_pull_requests AS pr FINAL\n  INNER JOIN (",
		"FROM work_graph_issue_pr AS l FINAL\n    INNER JOIN (SELECT o.team_id AS team_id, w.work_item_id AS work_item_id FROM ",
		") AS ow ON l.work_item_id = ow.work_item_id\n    WHERE l.org_id = {org_id:String} AND " + linkTierRankSQL + " > 0",
		") AS lw ON pr.org_id = lw.org_id AND pr.repo_id = lw.pr_repo_id AND pr.number = lw.pr_number",
		"SETTINGS query_plan_join_swap_table = 0",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(statement[at:], want)
		if i < 0 {
			t.Fatalf("team project link rows lack, in order, %q:\n%s", want, statement)
		}
		at += i + len(want)
	}
	if strings.Contains(statement, admittedLinkedPullRequestRowsSQL) {
		t.Fatalf("team project link rows join every admitted link row of the organization as a right side:\n%s", statement)
	}
}
