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
	fragment := admittedLinkedPullRequestRowsAmongSQL("(SELECT 7)")
	for _, want := range []string{"work_graph_issue_pr AS l FINAL", "git_pull_requests AS pr FINAL ON " + linkedPullRequestOnSQL, "cityHash64(l.work_item_id) IN (SELECT 7)", "(pr.repo_id, pr.number) IN (SELECT lc.repo_id, lc.pr_number FROM work_graph_issue_pr AS lc WHERE lc.org_id = {org_id:String} AND cityHash64(lc.work_item_id) IN (SELECT 7))", linkTierRankSQL + " > 0"} {
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
	if got := strings.Count(statement, teamLinkCandidatesSent); got != 1 {
		t.Fatalf("team project link rows read the sent candidate ids %d times, want 1 (the issue read, by String id):\n%s", got, statement)
	}
	if got := strings.Count(statement, teamLinkCandidateHashesSent); got != 2 {
		t.Fatalf("team project link rows read the sent candidate hashes %d times, want 2 (the link rows, their pull requests):\n%s", got, statement)
	}
	for _, want := range []string{"cw.work_item_id IN " + teamLinkCandidatesSent, "cityHash64(l.work_item_id) IN " + teamLinkCandidateHashesSent, "cityHash64(lc.work_item_id) IN " + teamLinkCandidateHashesSent, "l.work_item_id = w.work_item_id", "cw.project_id IN (SELECT v FROM", workItemIsIssueSQL("cw")} {
		if !strings.Contains(statement, want) {
			t.Fatalf("team project link rows lack %q:\n%s", want, statement)
		}
	}
}

func TestTeamLinkCandidatesReadTheLinkedIdsOfTheOwnedProjectsOnce(t *testing.T) {
	statement := teamLinkCandidatesStatement(factTimeBound{})
	for _, want := range []string{"SELECT groupArray(cp.work_item_id) FROM work_items AS cp WHERE cp.org_id = {org_id:String}", "cityHash64(cp.work_item_id) IN " + linkedWorkItemIDHashesSQL, "cp.project_id IN (SELECT v FROM", "team_id IN {ids:Array(String)}"} {
		if !strings.Contains(statement, want) {
			t.Fatalf("team link candidates lack %q:\n%s", want, statement)
		}
	}
	for _, banned := range []string{"work_items AS cp FINAL", "groupUniqArray", linkedWorkItemIDsSQL} {
		if strings.Contains(statement, banned) {
			t.Fatalf("team link candidates hold %q, want the unmerged rows as one array over the hashed linked ids:\n%s", banned, statement)
		}
	}
}
