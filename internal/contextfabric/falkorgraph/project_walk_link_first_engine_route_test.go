package falkorgraph

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// manyIssueProject seeds the project "alpha" with issues issues of its own,
// none linked, and links only the issues whose index is in linked to a pull
// request of the alpha repository. Issue ids sort by index.
func manyIssueProject(issues int, linked map[int]bool, issueRepos []string) routeSeed {
	s := routeSeed{text: map[string]string{}, reach: map[string][]string{}, deployments: map[string][]string{}}
	slug := "acme/alpha-service"
	repoID := "repository:github:" + slug
	s.nodes = append(s.nodes,
		seededNode{kind: "project", id: routeProjectAlpha, label: "alpha"},
		seededNode{kind: "repository", id: repoID, label: slug, repos: []string{slug}})
	s.text["project|"+routeProjectAlpha] = "alpha"
	if issueRepos == nil {
		issueRepos = []string{noRepositoryScope}
	}
	for i := 0; i < issues; i++ {
		issueID := fmt.Sprintf("work_item:linear:alpha-%03d", i)
		s.nodes = append(s.nodes, seededNode{kind: "work_item", id: issueID, label: issueID, repos: issueRepos, workItemType: "issue"})
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_PROJECT", "work_item", issueID, "project", routeProjectAlpha})
		if !linked[i] {
			continue
		}
		prID := fmt.Sprintf("work_item:ghpr:alpha-%03d", i)
		s.nodes = append(s.nodes, seededNode{kind: "work_item", id: prID, label: prID, repos: []string{slug}, workItemType: "pr"})
		s.edges = append(s.edges,
			seededEdge{"RELATES_TO", "work_item", prID, "work_item", issueID},
			seededEdge{"BELONGS_TO_REPOSITORY", "work_item", prID, "repository", repoID})
	}
	for d := 0; d < 2; d++ {
		depID := fmt.Sprintf("deployment:%s:%d", slug, d)
		s.nodes = append(s.nodes, seededNode{kind: "deployment", id: depID, label: depID, repos: []string{slug}})
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", depID, "repository", repoID})
		s.deployments[routeProjectAlpha] = append(s.deployments[routeProjectAlpha], depID)
	}
	return s
}

func TestAProjectWithManyIssuesAndNoLinkIsUnlinkedThroughTheEngine(t *testing.T) {
	answer := askProjectDeployments(t, manyIssueProject(71, nil, nil), storage.Principal{OrgID: "org-1"}, "alpha")
	requireNameCommit(t, answer, routeProjectAlpha)
	if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "unlinked" {
		t.Fatalf("walk lines = %v, want one line with outcome=unlinked: no issue of the project links a pull request", answer.walkLines)
	}
	detail := answer.detail(contractsv1.ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked)
	if detail == nil || detail.Count == nil || *detail.Count != 71 {
		t.Fatalf("coverage details = %+v, want the unlinked limitation counting the project's 71 issues", answer.result.Coverage.Details)
	}
	for _, d := range answer.result.Coverage.Details {
		if d.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated {
			t.Fatalf("coverage details = %+v, want no truncation: nothing was cut", answer.result.Coverage.Details)
		}
	}
}

func TestAProjectWhoseLinkedIssueSortsPastTheBudgetServesItsDeploymentsThroughTheEngine(t *testing.T) {
	answer := askProjectDeployments(t, manyIssueProject(71, map[int]bool{70: true}, nil), storage.Principal{OrgID: "org-1"}, "alpha")
	requireNameCommit(t, answer, routeProjectAlpha)
	want := []string{"deployment:acme/alpha-service:0", "deployment:acme/alpha-service:1"}
	got := answer.members()
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") || !answer.result.Cohort.Complete {
		t.Fatalf("served %v, want %v as a complete cohort: the one linked issue sorts after the first 25", got, want)
	}
	if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "members" || answer.walkLines[0]["truncated"] != false {
		t.Fatalf("walk lines = %v, want one uncut members line", answer.walkLines)
	}
}

func TestHiddenIssuesDoNotCrowdOutAVisibleLinkThroughTheEngine(t *testing.T) {
	s := manyIssueProject(40, map[int]bool{39: true}, nil)
	// Thirty-nine issues of a repository the caller is not granted sort first.
	for i := range s.nodes {
		if s.nodes[i].kind == "work_item" && s.nodes[i].workItemType == "issue" && !strings.HasSuffix(s.nodes[i].id, "-039") {
			s.nodes[i].repos = []string{"acme/hidden"}
		}
	}
	s.reach[routeProjectAlpha] = []string{"acme/alpha-service"}
	answer := askProjectDeployments(t, s, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/alpha-service"}}, "alpha")
	requireNameCommit(t, answer, routeProjectAlpha)
	got := answer.members()
	if len(got) != 2 || !answer.result.Cohort.Complete {
		t.Fatalf("served %v (cohort %+v), want the 2 deployments of the granted repository as a complete cohort: hidden issues must not use up the read", got, answer.result.Cohort)
	}
}

// TestARestrictedCallerOfAProjectWhoseLinksAreAllHiddenGetsOnlyTheNeutralReason:
// the project has links, the caller can see none of them; it learns neither
// that links exist nor how many issues the project has.
func TestARestrictedCallerOfAProjectWhoseLinksAreAllHiddenGetsOnlyTheNeutralReason(t *testing.T) {
	linked := map[int]bool{}
	for i := 0; i < 30; i++ {
		linked[i] = true
	}
	s := manyIssueProject(30, linked, nil)
	s.reach[routeProjectAlpha] = []string{"acme/other"}
	answer := askProjectDeployments(t, s, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/other"}}, "alpha")
	requireNameCommit(t, answer, routeProjectAlpha)
	if got := answer.members(); len(got) != 0 {
		t.Fatalf("served %v, want no member: every link is hidden", got)
	}
	denied := answer.detail(contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization)
	if denied == nil || denied.Count == nil || *denied.Count != 0 || len(answer.result.Coverage.Details) != 1 {
		t.Fatalf("coverage details = %+v, want only the neutral denied reason with no count", answer.result.Coverage.Details)
	}
	if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "denied" {
		t.Fatalf("walk lines = %v, want one denied line", answer.walkLines)
	}
}

// TestManyHiddenLinksDoNotCrowdOutAVisibleLinkThroughTheEngine: more hidden
// links than the read's pages hold sort before the one link the caller can
// see; the caller is still served that link's deployments.
func TestManyHiddenLinksDoNotCrowdOutAVisibleLinkThroughTheEngine(t *testing.T) {
	linked := map[int]bool{}
	for i := 0; i < 300; i++ {
		linked[i] = true
	}
	s := manyIssueProject(300, linked, nil)
	for i := range s.nodes {
		if s.nodes[i].workItemType == "pr" && !strings.HasSuffix(s.nodes[i].id, "-299") {
			s.nodes[i].repos = []string{"acme/hidden"}
		}
	}
	s.reach[routeProjectAlpha] = []string{"acme/alpha-service"}
	answer := askProjectDeployments(t, s, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/alpha-service"}}, "alpha")
	requireNameCommit(t, answer, routeProjectAlpha)
	if got := answer.members(); len(got) != 2 || !answer.result.Cohort.Complete {
		t.Fatalf("served %v (cohort %+v), want the 2 deployments of the one visible link as a complete cohort", got, answer.result.Cohort)
	}
}

// TestAMergeRequestLinkIsALink: a merge request work item linked to an issue
// reaches its repository's deployments as a pull request does.
func TestAMergeRequestLinkIsALink(t *testing.T) {
	s := manyIssueProject(1, map[int]bool{0: true}, nil)
	for i := range s.nodes {
		if s.nodes[i].workItemType == "pr" {
			s.nodes[i].workItemType = "merge_request"
		}
	}
	answer := askProjectDeployments(t, s, storage.Principal{OrgID: "org-1"}, "alpha")
	if got := answer.members(); len(got) != 2 {
		t.Fatalf("served %v, want the 2 deployments the merge request's repository holds", got)
	}
}

// TestMoreLinkRowsThanThePagesHoldIsACut: many issues linking one pull request
// fill every page before the rows end, so the read is cut.
func TestMoreLinkRowsThanThePagesHoldIsACut(t *testing.T) {
	s := manyIssuesOneRepository(300)
	walk := walkProject(t, s, storage.Principal{OrgID: "org-1"}, 25)
	if !walk.truncated || len(walk.nodes) != 1 {
		t.Fatalf("walk = %+v, want the one deployment and a cut: 300 link rows exceed 8 pages of 26", walk)
	}
}
