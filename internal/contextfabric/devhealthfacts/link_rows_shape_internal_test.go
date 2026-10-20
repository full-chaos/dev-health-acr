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

func TestAdmittedLinkedPullRequestRowsAmongCandidatesReadOnlyTheCandidatesLinks(t *testing.T) {
	fragment := admittedLinkedPullRequestRowsAmongSQL("(SELECT 'x')")
	for _, want := range []string{"work_graph_issue_pr AS l FINAL", "git_pull_requests AS pr FINAL ON " + linkedPullRequestOnSQL, "l.work_item_id IN (SELECT 'x')", "(pr.repo_id, pr.number) IN (SELECT lc.repo_id, lc.pr_number FROM work_graph_issue_pr AS lc WHERE lc.org_id = {org_id:String} AND lc.work_item_id IN (SELECT 'x'))", linkTierRankSQL + " > 0"} {
		if !strings.Contains(fragment, want) {
			t.Fatalf("the candidate link-row fragment lacks %q:\n%s", want, fragment)
		}
	}
}

func TestTeamProjectLinkRowsHoldOnlyTheOwnedProjectsLinks(t *testing.T) {
	statement := teamProjectLinkRowsSQL("ids", factTimeBound{})
	if got := strings.Count(statement, "FROM work_items AS cw FINAL"); got != 1 || strings.Count(statement, "FROM work_items") != strings.Count(statement, "FROM work_items AS cp WHERE")+1 {
		t.Fatalf("team project link rows read work_items with FINAL %d times, want once, and otherwise only as the unmerged candidates:\n%s", got, statement)
	}
	if strings.Contains(statement, admittedLinkedPullRequestRowsSQL) {
		t.Fatalf("team project link rows join every link row and pull request of the organization:\n%s", statement)
	}
	for _, want := range []string{"cp.work_item_id IN " + linkedWorkItemIDsSQL, "cw.work_item_id IN (SELECT cp.work_item_id", "l.work_item_id IN (SELECT cp.work_item_id", "lc.work_item_id IN (SELECT cp.work_item_id", "cw.project_id IN (SELECT v FROM", "cp.project_id IN (SELECT v FROM", workItemIsIssueSQL("cw")} {
		if !strings.Contains(statement, want) {
			t.Fatalf("team project link rows lack %q:\n%s", want, statement)
		}
	}
}
