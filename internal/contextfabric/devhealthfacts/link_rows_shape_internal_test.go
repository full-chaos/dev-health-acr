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
	if got := strings.Count(statement, "FROM work_items"); got != 1 || !strings.Contains(statement, "FROM work_items AS cw FINAL") {
		t.Fatalf("team project link rows read work_items %d times, want once, with FINAL, for the sent candidates:\n%s", got, statement)
	}
	for _, banned := range []string{admittedLinkedPullRequestRowsSQL, linkedWorkItemIDsSQL} {
		if strings.Contains(statement, banned) {
			t.Fatalf("team project link rows hold every link of the organization (%q):\n%s", banned, statement)
		}
	}
	if got := strings.Count(statement, teamLinkCandidatesSent); got != 3 {
		t.Fatalf("team project link rows read the sent candidates %d times, want 3 (the issues, the link rows, their pull requests):\n%s", got, statement)
	}
	for _, want := range []string{"cw.work_item_id IN " + teamLinkCandidatesSent, "l.work_item_id IN " + teamLinkCandidatesSent, "lc.work_item_id IN " + teamLinkCandidatesSent, "cw.project_id IN (SELECT v FROM", workItemIsIssueSQL("cw")} {
		if !strings.Contains(statement, want) {
			t.Fatalf("team project link rows lack %q:\n%s", want, statement)
		}
	}
}

func TestTeamLinkCandidatesReadTheLinkedIdsOfTheOwnedProjectsOnce(t *testing.T) {
	statement := teamLinkCandidatesStatement(factTimeBound{})
	for _, want := range []string{"SELECT groupUniqArray(cp.work_item_id) FROM work_items AS cp WHERE cp.org_id = {org_id:String}", "cp.work_item_id IN " + linkedWorkItemIDsSQL, "cp.project_id IN (SELECT v FROM", "team_id IN {ids:Array(String)}"} {
		if !strings.Contains(statement, want) {
			t.Fatalf("team link candidates lack %q:\n%s", want, statement)
		}
	}
	if strings.Contains(statement, "work_items AS cp FINAL") {
		t.Fatalf("team link candidates read work_items with FINAL, want the unmerged rows:\n%s", statement)
	}
}
