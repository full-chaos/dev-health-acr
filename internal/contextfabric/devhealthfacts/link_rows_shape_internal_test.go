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

func TestTeamProjectLinkRowsReadWorkItemsOnceForTheOwnedProjects(t *testing.T) {
	statement := teamProjectLinkRowsSQL("ids", factTimeBound{})
	if got := strings.Count(statement, "FROM work_items"); got != 1 {
		t.Fatalf("team project link rows read work_items %d times, want 1:\n%s", got, statement)
	}
	for _, want := range []string{"work_graph_issue_pr AS l FINAL", "git_pull_requests AS pr FINAL", linkTierRankSQL + " > 0", "ON o.work_item_id = l.work_item_id", "pr.repo_id = lw.pr_repo_id AND pr.number = lw.pr_number", "cw.work_item_id IN " + linkedWorkItemIDsSQL, "(cw.provider, cw.project_id) IN", workItemIsIssueSQL("cw")} {
		if !strings.Contains(statement, want) {
			t.Fatalf("team project link rows lack %q:\n%s", want, statement)
		}
	}
}

func TestTeamProjectLinkRowsStreamTheOrganizationSideOfEveryJoin(t *testing.T) {
	statement := teamProjectLinkRowsSQL("ids", factTimeBound{})
	pullRequests := strings.Index(statement, "FROM git_pull_requests AS pr FINAL")
	links := strings.Index(statement, "FROM work_graph_issue_pr AS l FINAL")
	owned := strings.Index(statement, "ON o.work_item_id = l.work_item_id")
	if pullRequests < 0 || links < pullRequests || owned < links {
		t.Fatalf("the organization's pull requests and link rows must be the left side of their joins, the owned issues the right:\n%s", statement)
	}
}
