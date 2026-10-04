package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const projectAnchorID = "project:payments"

type projectSeed struct {
	nodes []seededNode
	edges []seededEdge
	// served maps a deployment id to the provider row that links it.
	served map[string]string
}

func (s *projectSeed) repository(slug string, deployments int) string {
	repoID := "repository:github:" + slug
	s.nodes = append(s.nodes, seededNode{kind: "repository", id: repoID, label: slug, repos: []string{slug}})
	for d := 0; d < deployments; d++ {
		depID := fmt.Sprintf("deployment:%s:%d", slug, d)
		s.nodes = append(s.nodes, seededNode{kind: "deployment", id: depID, label: depID, repos: []string{slug}})
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", depID, "repository", repoID})
	}
	return repoID
}

// link seeds one issue of the project and its native pull-request link: the
// pull-request work item relates to the issue (pull request is the edge
// source) and belongs to its own repository.
func (s *projectSeed) link(row, issueID string, issueRepos []string, prID, prType, repoID, slug string, issueIsSource bool) {
	if issueRepos == nil {
		issueRepos = []string{noRepositoryScope}
	}
	s.nodes = append(s.nodes,
		seededNode{kind: "work_item", id: issueID, label: issueID, repos: issueRepos, workItemType: "issue"},
		seededNode{kind: "work_item", id: prID, label: prID, repos: []string{slug}, workItemType: prType})
	relation := seededEdge{"RELATES_TO", "work_item", prID, "work_item", issueID}
	if issueIsSource {
		relation = seededEdge{"RELATES_TO", "work_item", issueID, "work_item", prID}
	}
	s.edges = append(s.edges,
		seededEdge{"BELONGS_TO_PROJECT", "work_item", issueID, "project", projectAnchorID},
		relation,
		seededEdge{"BELONGS_TO_REPOSITORY", "work_item", prID, "repository", repoID})
	for d := 0; d < 2; d++ {
		s.served[fmt.Sprintf("deployment:%s:%d", slug, d)] = row
	}
}

// seedProject builds a project whose issues reach three repositories through
// the link shapes the ops writers produce (a github closing reference, a
// linear attachment to a github pull request, a jira dev-status link to a
// github pull request), beside the shapes that must NOT reach a repository.
func seedProject() projectSeed {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})

	githubRepo := s.repository("acme/github-linked", 2)
	linearRepo := s.repository("acme/linear-linked", 2)
	jiraRepo := s.repository("acme/jira-linked", 2)
	s.link("github", "work_item:gh:1", []string{"acme/github-linked"}, "work_item:ghpr:1", "pr", githubRepo, "acme/github-linked", false)
	s.link("linear", "work_item:linear:ENG-1", nil, "work_item:ghpr:2", "pr", linearRepo, "acme/linear-linked", false)
	s.link("jira", "work_item:jira:PAY-1", nil, "work_item:ghpr:3", "pr", jiraRepo, "acme/jira-linked", true)

	// An issue's OWN repository is not a path to deployments.
	ownRepo := s.repository("acme/issue-own", 1)
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item:gh:1", "repository", ownRepo})
	// An issue related to another issue is not a pull-request link.
	otherIssueRepo := s.repository("acme/issue-neighbour", 1)
	s.nodes = append(s.nodes, seededNode{kind: "work_item", id: "work_item:gh:2", label: "gh:2", repos: []string{"acme/issue-neighbour"}, workItemType: "issue"})
	s.edges = append(s.edges,
		seededEdge{"RELATES_TO", "work_item", "work_item:gh:1", "work_item", "work_item:gh:2"},
		seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item:gh:2", "repository", otherIssueRepo})
	// A non pull-request work item linked to an issue does not count.
	taskRepo := s.repository("acme/task-linked", 1)
	s.nodes = append(s.nodes, seededNode{kind: "work_item", id: "work_item:task:1", label: "task:1", repos: []string{"acme/task-linked"}, workItemType: "task"})
	s.edges = append(s.edges,
		seededEdge{"RELATES_TO", "work_item", "work_item:task:1", "work_item", "work_item:gh:1"},
		seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item:task:1", "repository", taskRepo})
	// A pull request that is itself a member of the project, linked to another
	// pull request, is not an issue: the second pull request's repository is
	// not reached.
	chainRepo := s.repository("acme/pr-chain", 1)
	s.nodes = append(s.nodes,
		seededNode{kind: "work_item", id: "work_item:ghpr:member", label: "ghpr:member", repos: []string{"acme/github-linked"}, workItemType: "pr"},
		seededNode{kind: "work_item", id: "work_item:ghpr:chained", label: "ghpr:chained", repos: []string{"acme/pr-chain"}, workItemType: "pr"})
	s.edges = append(s.edges,
		seededEdge{"BELONGS_TO_PROJECT", "work_item", "work_item:ghpr:member", "project", projectAnchorID},
		seededEdge{"RELATES_TO", "work_item", "work_item:ghpr:chained", "work_item", "work_item:ghpr:member"},
		seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item:ghpr:chained", "repository", chainRepo})
	// A repository the project does not reach.
	s.repository("acme/foreign", 1)
	// A pull request linked by a non-RELATES_TO edge is not a link either.
	blockRepo := s.repository("acme/blocks-linked", 1)
	s.nodes = append(s.nodes, seededNode{kind: "work_item", id: "work_item:ghpr:9", label: "ghpr:9", repos: []string{"acme/blocks-linked"}, workItemType: "pr"})
	s.edges = append(s.edges,
		seededEdge{"BLOCKS", "work_item", "work_item:ghpr:9", "work_item", "work_item:gh:1"},
		seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item:ghpr:9", "repository", blockRepo})
	return s
}

func projectDeploymentsRequest() contextfabric.GraphDiscoveryRequest {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID, Label: "payments"}
	request := ownershipRoutingRequest(deploymentMembersFrame(), anchor)
	request.Request.Options.MaxCohortMembers = 50
	return request
}

func discoverProjectDeployments(t *testing.T, s projectSeed, principal storage.Principal, wrap func(*fakeConn)) ([]string, contextfabric.GraphContext) {
	t.Helper()
	conn := seededGraphConn(s.nodes, s.edges)
	if wrap != nil {
		wrap(conn)
	}
	adapter := newFakeAdapter(t, conn)
	result, err := adapter.DiscoverContext(context.Background(), principal, projectDeploymentsRequest())
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	var got []string
	if result.Cohort != nil {
		for _, m := range result.Cohort.Members {
			got = append(got, m.Subject.CanonicalID)
		}
	}
	sort.Strings(got)
	return got, result
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestProjectDeploymentMembersAreTheDeploymentsOfItsLinkedPullRequestsRepositories(t *testing.T) {
	s := seedProject()
	got, result := discoverProjectDeployments(t, s, storage.Principal{OrgID: "org-1"}, nil)
	want := sortedKeys(s.served)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("project reached %v, want exactly the deployments of the three linked repositories %v", got, want)
	}
	perRow := map[string]int{}
	for _, id := range got {
		perRow[s.served[id]]++
	}
	for _, row := range []string{"github", "linear", "jira"} {
		if perRow[row] != 2 {
			t.Errorf("provider row %s: reached %d deployments, want 2", row, perRow[row])
		}
	}
	if result.CohortPopulation != len(want) || result.Cohort == nil || !result.Cohort.Complete {
		t.Fatalf("population=%d cohort=%+v, want %d and a complete cohort", result.CohortPopulation, result.Cohort, len(want))
	}
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked {
			t.Fatalf("a project with linked pull requests carries the unlinked limitation: %+v", detail)
		}
	}
	if result.Coverage.Partial {
		t.Fatalf("coverage partial = true with reasons %v, want a clean read", result.Coverage.DegradedReasons)
	}
}

func TestProjectDeploymentMembersFollowTheCallersRepositoryGrant(t *testing.T) {
	s := seedProject()
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/github-linked", "acme/linear-linked"}}
	got, result := discoverProjectDeployments(t, s, principal, nil)
	for _, id := range got {
		if row := s.served[id]; row != "github" && row != "linear" {
			t.Fatalf("restricted caller reached %s (%s) outside the granted repositories", id, row)
		}
	}
	if len(got) != 4 || result.CohortPopulation != 4 {
		t.Fatalf("restricted caller reached %v population=%d, want the 4 granted deployments and a population of 4 (denied members must not count)", got, result.CohortPopulation)
	}
	if result.Coverage.Partial {
		t.Fatalf("an ungranted repository must vanish without a disclosure, got reasons %v", result.Coverage.DegradedReasons)
	}
}

func TestProjectDeploymentMembersLeakNoForeignDeploymentThroughTheLexicalArm(t *testing.T) {
	s := seedProject()
	got, _ := discoverProjectDeployments(t, s, storage.Principal{OrgID: "org-1"}, func(conn *fakeConn) {
		inner := conn.queryFunc
		conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, "fulltext") {
				foreign := fakeSubjectNodeRow("deployment", "deployment:acme/foreign:0", "payments deploy")
				foreign["n"].(*node).Properties[propAuthzRepos] = []string{"acme/foreign"}
				return []row{{"node": foreign["n"], "score": 1.0}}, nil
			}
			return inner(ctx, key, cypher, params, readOnly)
		}
	})
	for _, id := range got {
		if strings.Contains(id, "acme/foreign") {
			t.Fatalf("the lexical arm admitted %s, a deployment of a repository the project does not reach", id)
		}
	}
}

func unlinkedDetail(result contextfabric.GraphContext) *contextfabric.CoverageDetail {
	for i := range result.Coverage.Details {
		if result.Coverage.Details[i].Code == contractsv1.ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked {
			return &result.Coverage.Details[i]
		}
	}
	return nil
}

func TestProjectWithoutALinkedPullRequestIsANamedLimitationNotAnEmptyCohort(t *testing.T) {
	// A GitLab project: its issues carry no native pull-request link, and a
	// merge request is a work item no issue relates to.
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	repoID := s.repository("acme/gitlab-repo", 2)
	for i := 0; i < 2; i++ {
		issue := fmt.Sprintf("work_item:gitlab:%d", i)
		s.nodes = append(s.nodes, seededNode{kind: "work_item", id: issue, label: issue, repos: []string{"acme/gitlab-repo"}, workItemType: "issue"})
		s.edges = append(s.edges,
			seededEdge{"BELONGS_TO_PROJECT", "work_item", issue, "project", projectAnchorID},
			seededEdge{"BELONGS_TO_REPOSITORY", "work_item", issue, "repository", repoID})
	}
	s.nodes = append(s.nodes, seededNode{kind: "work_item", id: "work_item:gitlab:mr", label: "mr", repos: []string{"acme/gitlab-repo"}, workItemType: "merge_request"})
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item:gitlab:mr", "repository", repoID})

	got, result := discoverProjectDeployments(t, s, storage.Principal{OrgID: "org-1"}, nil)
	if len(got) != 0 {
		t.Fatalf("a project with no linked pull request reached %v through its issues' own repository", got)
	}
	detail := unlinkedDetail(result)
	if detail == nil || !detail.Degrading || detail.Count == nil || *detail.Count != 2 {
		t.Fatalf("details = %+v, want the degrading unlinked limitation naming the 2 issues examined", result.Coverage.Details)
	}
	if err := detail.Validate(); err != nil {
		t.Fatalf("detail does not validate: %v", err)
	}
	if !result.Coverage.Partial {
		t.Fatal("an unreachable population must be partial coverage, not a clean empty answer")
	}
}

func TestProjectWithNoIssuesIsTheSameNamedLimitation(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	_, result := discoverProjectDeployments(t, s, storage.Principal{OrgID: "org-1"}, nil)
	detail := unlinkedDetail(result)
	if detail == nil || detail.Count == nil || *detail.Count != 0 {
		t.Fatalf("details = %+v, want the unlinked limitation with 0 issues examined", result.Coverage.Details)
	}
}

func TestLinkedPullRequestsDeniedByAuthorizationAreNotTheUnlinkedLimitation(t *testing.T) {
	s := seedProject()
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/somewhere-else"}}
	got, result := discoverProjectDeployments(t, s, principal, nil)
	if len(got) != 0 {
		t.Fatalf("caller with no granted repository reached %v", got)
	}
	if unlinkedDetail(result) != nil {
		t.Fatal("pull requests that exist but are not visible to the caller must not read as 'no issue links a pull request'")
	}
	if detail := deniedDetail(result); detail == nil || detail.Count == nil || *detail.Count != 0 || !result.Coverage.Partial {
		t.Fatalf("details = %+v partial=%v, want the restricted-visibility limitation (denied by authorization) and partial coverage", result.Coverage.Details, result.Coverage.Partial)
	}
}

func deniedDetail(result contextfabric.GraphContext) *contextfabric.CoverageDetail {
	for i := range result.Coverage.Details {
		if result.Coverage.Details[i].Code == contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
			return &result.Coverage.Details[i]
		}
	}
	return nil
}

func TestNoLinkedPullRequestIsUnlinkedOnlyForAnUnrestrictedCaller(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes,
		seededNode{kind: "project", id: projectAnchorID, label: "payments"},
		seededNode{kind: "work_item", id: "work_item:gh:1", label: "issue", repos: []string{"acme/x"}, workItemType: "issue"})
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_PROJECT", "work_item", "work_item:gh:1", "project", projectAnchorID})
	_, open := discoverProjectDeployments(t, s, storage.Principal{OrgID: "org-1"}, nil)
	if detail := unlinkedDetail(open); detail == nil || detail.Count == nil || *detail.Count != 1 || deniedDetail(open) != nil {
		t.Fatalf("unrestricted: details = %+v, want only the unlinked limitation with 1 issue examined", open.Coverage.Details)
	}
	_, restricted := discoverProjectDeployments(t, s, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/granted"}}, nil)
	if detail := deniedDetail(restricted); detail == nil || detail.Count == nil || *detail.Count != 0 || unlinkedDetail(restricted) != nil {
		t.Fatalf("restricted: details = %+v, want only the neutral denied code (no link existence, no issue count)", restricted.Coverage.Details)
	}
}

func TestProjectDeploymentWalkAdmitsAnIssueByTheWorkItemRule(t *testing.T) {
	build := func(issueRepos []string) projectSeed {
		s := projectSeed{served: map[string]string{}}
		s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
		repo := s.repository("acme/granted", 1)
		s.link("row", "work_item:issue:1", issueRepos, "work_item:ghpr:1", "pr", repo, "acme/granted", false)
		return s
	}
	granted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/granted"}}
	for _, c := range []struct {
		name       string
		issueRepos []string
		want       int
	}{
		{"repository-less issue linked to a granted pull request", nil, 1},
		{"issue in the granted repository", []string{"acme/granted"}, 1},
		{"issue in a repository the caller cannot see", []string{"acme/hidden"}, 0},
	} {
		walk := walkProject(t, build(c.issueRepos), granted, 50)
		if len(walk.nodes) != c.want {
			t.Errorf("%s: served %d deployments, want %d", c.name, len(walk.nodes), c.want)
		}
		if c.want == 0 {
			if outcome := projectDeploymentWalkOutcome(walk, true, nil); outcome != ProjectDeploymentWalkDenied {
				t.Errorf("%s: outcome = %q, want denied: a restricted caller never reads unlinked", c.name, outcome)
			}
		}
	}
}

func walkProject(t *testing.T, s projectSeed, principal storage.Principal, limit int) projectDeploymentWalk {
	t.Helper()
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	walk, err := adapter.projectDeploymentMembers(context.Background(), "key", "org-1", principal, contextfabric.RequestedScope{},
		contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID}, limit, newTemporalFilter(contextfabric.TimeContext{}))
	if err != nil {
		t.Fatalf("projectDeploymentMembers() error = %v", err)
	}
	return walk
}

// manyIssuesOneRepository is a project with several issues linking one pull
// request into one repository holding a single deployment, so a frontier cut
// and a member cap can be told apart.
func manyIssuesOneRepository(issues int) projectSeed {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	repoID := s.repository("acme/one", 1)
	s.nodes = append(s.nodes, seededNode{kind: "work_item", id: "work_item:ghpr:one", label: "pr", repos: []string{"acme/one"}, workItemType: "pr"})
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item:ghpr:one", "repository", repoID})
	for i := 0; i < issues; i++ {
		issue := fmt.Sprintf("work_item:gh:%02d", i)
		s.nodes = append(s.nodes, seededNode{kind: "work_item", id: issue, label: issue, repos: []string{noRepositoryScope}, workItemType: "issue"})
		s.edges = append(s.edges,
			seededEdge{"BELONGS_TO_PROJECT", "work_item", issue, "project", projectAnchorID},
			seededEdge{"RELATES_TO", "work_item", "work_item:ghpr:one", "work_item", issue})
	}
	return s
}

// linkedIssues is a project with issues issues, each linked to a pull request
// of its own in a repository of its own holding deployments deployments.
func linkedIssues(issues, deployments int) projectSeed {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	for i := 0; i < issues; i++ {
		slug := fmt.Sprintf("acme/linked-%02d", i)
		repoID := s.repository(slug, deployments)
		s.link("github", fmt.Sprintf("work_item:gh:%02d", i), nil, fmt.Sprintf("work_item:ghpr:%02d", i), "pr", repoID, slug, false)
	}
	return s
}

func TestProjectDeploymentWalkReportsMoreLinksThanTheBudgetAsTruncation(t *testing.T) {
	walk := walkProject(t, linkedIssues(5, 1), storage.Principal{OrgID: "org-1"}, 2)
	if !walk.truncated {
		t.Fatalf("5 linked pull requests under a budget of 2 must report truncation, got %+v", walk)
	}
	whole := walkProject(t, linkedIssues(5, 1), storage.Principal{OrgID: "org-1"}, 50)
	if whole.truncated || len(whole.nodes) != 5 {
		t.Fatalf("a walk inside its budget must not report truncation, got truncated=%v nodes=%d", whole.truncated, len(whole.nodes))
	}
}

// TestManyIssuesLinkingOnePullRequestAreNotACut: the budget counts linked pull
// requests, so many issues linking one pull request fit in it.
func TestManyIssuesLinkingOnePullRequestAreNotACut(t *testing.T) {
	walk := walkProject(t, manyIssuesOneRepository(5), storage.Principal{OrgID: "org-1"}, 2)
	if walk.truncated || len(walk.nodes) != 1 || walk.issues != 5 || walk.linkedPullRequests != 1 {
		t.Fatalf("walk = %+v, want the one deployment, 5 issues, 1 linked pull request, uncut", walk)
	}
}

func TestProjectDeploymentWalkReportsAMemberCapAsTruncation(t *testing.T) {
	s := seedProject()
	walk := walkProject(t, s, storage.Principal{OrgID: "org-1"}, 5)
	if !walk.truncated || len(walk.nodes) != 5 {
		t.Fatalf("6 deployments under a budget of 5 (frontiers within it) must serve 5 and report truncation, got truncated=%v nodes=%d", walk.truncated, len(walk.nodes))
	}
}

// The three guards below are pinned one at a time with scopes that differ
// between the pull request, the repository and the deployment. A real graph
// gives all three the same repository slug, so the cohort's own downstream
// authorization hides a missing guard; the walk must hold each on its own.
func TestProjectDeploymentWalkAuthorizesEachDisclosedHop(t *testing.T) {
	build := func(prRepos, repoRepos, deploymentRepos []string) projectSeed {
		s := projectSeed{served: map[string]string{}}
		s.nodes = append(s.nodes,
			seededNode{kind: "project", id: projectAnchorID, label: "payments"},
			seededNode{kind: "work_item", id: "work_item:gh:1", label: "issue", repos: []string{noRepositoryScope}, workItemType: "issue"},
			seededNode{kind: "work_item", id: "work_item:ghpr:1", label: "pr", repos: prRepos, workItemType: "pr"},
			seededNode{kind: "repository", id: "repository:r", label: "r", repos: repoRepos},
			seededNode{kind: "deployment", id: "deployment:d", label: "d", repos: deploymentRepos})
		s.edges = append(s.edges,
			seededEdge{"BELONGS_TO_PROJECT", "work_item", "work_item:gh:1", "project", projectAnchorID},
			seededEdge{"RELATES_TO", "work_item", "work_item:ghpr:1", "work_item", "work_item:gh:1"},
			seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item:ghpr:1", "repository", "repository:r"},
			seededEdge{"BELONGS_TO_REPOSITORY", "deployment", "deployment:d", "repository", "repository:r"})
		return s
	}
	granted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/granted"}}
	g, h := []string{"acme/granted"}, []string{"acme/hidden"}
	for _, c := range []struct {
		name                       string
		pr, repository, deployment []string
		want                       int
	}{
		{"all visible", g, g, g, 1},
		{"pull request hidden", h, g, g, 0},
		{"repository hidden", g, h, g, 0},
		{"deployment hidden", g, g, h, 0},
	} {
		walk := walkProject(t, build(c.pr, c.repository, c.deployment), granted, 50)
		if len(walk.nodes) != c.want {
			t.Errorf("%s: walk served %d deployments, want %d", c.name, len(walk.nodes), c.want)
		}
	}
}

func linklessThenLinked(linkless int) projectSeed {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	repoID := s.repository("acme/one", 1)
	for i := 0; i < linkless; i++ {
		issue := fmt.Sprintf("work_item:gh:%02d", i)
		s.nodes = append(s.nodes, seededNode{kind: "work_item", id: issue, label: issue, repos: []string{noRepositoryScope}, workItemType: "issue"})
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_PROJECT", "work_item", issue, "project", projectAnchorID})
	}
	s.link("late", "work_item:gh:zz", nil, "work_item:ghpr:late", "pr", repoID, "acme/one", false)
	return s
}

// TestALinkPastMoreLinklessIssuesThanTheBudgetIsFound: issues with no link do
// not use up the read, so a link past them is found and the cohort is whole.
func TestALinkPastMoreLinklessIssuesThanTheBudgetIsFound(t *testing.T) {
	s := linklessThenLinked(6)
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	adapter.config.MaxResults = 3
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, projectDeploymentsRequest())
	if err != nil {
		t.Fatal(err)
	}
	if unlinkedDetail(result) != nil {
		t.Fatalf("details = %+v: a later issue has a link, so 'unlinked' is false", result.Coverage.Details)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 || !result.Cohort.Complete {
		t.Fatalf("cohort = %+v, want the late link's deployment as a complete cohort", result.Cohort)
	}
}

func TestProjectDeploymentStepQueriesAreBounded(t *testing.T) {
	s := linkedIssues(40, 1)
	conn := seededGraphConn(s.nodes, s.edges)
	adapter := newFakeAdapter(t, conn)
	var limits []int
	inner := conn.queryFunc
	conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, ro bool) ([]row, error) {
		if strings.Contains(cypher, "UNWIND $ids") || strings.Contains(cypher, "SKIP $skip") {
			if !strings.Contains(cypher, "LIMIT $limit") {
				t.Errorf("step query has no LIMIT: %s", cypher)
			}
			if limit, ok := params["limit"].(int); ok {
				limits = append(limits, limit)
			}
		}
		return inner(ctx, key, cypher, params, ro)
	}
	walk, err := adapter.projectDeploymentMembers(context.Background(), "key", "org-1", storage.Principal{OrgID: "org-1"}, contextfabric.RequestedScope{},
		contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID}, 5, newTemporalFilter(contextfabric.TimeContext{}))
	if err != nil {
		t.Fatal(err)
	}
	if !walk.truncated {
		t.Fatal("40 linked pull requests under a budget of 5 must report truncation")
	}
	if len(limits) == 0 {
		t.Fatal("no step query observed")
	}
	linkReads := 0
	for _, l := range limits {
		if l == 6 {
			linkReads++
		}
	}
	if linkReads > 4 {
		t.Errorf("%d reads at the budget's page size, want the link read to stop once the budget is spent", linkReads)
	}
	for _, l := range limits {
		if l > 6 {
			t.Errorf("step read limit %d exceeds budget+1", l)
		}
	}
}

func TestALexicallyMatchedRepositoryAddsNoDeploymentToThePaths(t *testing.T) {
	s := seedProject()
	s.nodes = append(s.nodes,
		seededNode{kind: "repository", id: "repository:payments", label: "payments", repos: []string{"acme/payments"}},
		seededNode{kind: "deployment", id: "deployment:payments:0", label: "payments deploy", repos: []string{"acme/payments"}})
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", "deployment:payments:0", "repository", "repository:payments"})
	got, result := discoverProjectDeployments(t, s, storage.Principal{OrgID: "org-1"}, func(c *fakeConn) {
		inner := c.queryFunc
		c.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, ro bool) ([]row, error) {
			if strings.Contains(cypher, "fulltext") {
				n := fakeSubjectNodeRow("repository", "repository:payments", "payments")["n"].(*node)
				n.Properties[propAuthzRepos] = []string{"acme/payments"}
				d := fakeSubjectNodeRow("deployment", "deployment:payments:0", "payments deploy")["n"].(*node)
				d.Properties[propAuthzRepos] = []string{"acme/payments"}
				return []row{{"node": n, "score": 1.0}, {"node": d, "score": 0.9}}, nil
			}
			return inner(ctx, key, cypher, params, ro)
		}
	})
	for _, id := range got {
		if id == "deployment:payments:0" {
			t.Fatalf("cohort carries the lexically matched repository's deployment")
		}
	}
	for _, p := range result.Paths {
		for _, ref := range p.Nodes {
			if ref.CanonicalID == "deployment:payments:0" {
				t.Fatalf("paths carry the lexically matched repository's deployment: %+v", p)
			}
		}
	}
}

func TestADeploymentReachedFromAStrayCommittedSubjectIsNotAProjectMember(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	s.repository("acme/stray", 1)
	conn := seededGraphConn(s.nodes, s.edges)
	adapter := newFakeAdapter(t, conn)
	request := projectDeploymentsRequest()
	request.Resolution.Committed = append(request.Resolution.Committed,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/stray", Label: "acme/stray"})
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cohort != nil && len(result.Cohort.Members) != 0 {
		t.Fatalf("a deployment of a stray committed repository became a member: %+v", result.Cohort.Members)
	}
	for _, p := range result.Paths {
		for _, ref := range p.Nodes {
			if ref.Kind == contextfabric.SubjectDeployment {
				t.Fatalf("paths carry a deployment reached from a stray committed subject: %+v", p)
			}
		}
	}
}

func TestACutFrontierWithNoMemberIsPartialAndNamesTheTruncation(t *testing.T) {
	s := linkedIssues(6, 0)
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	adapter.config.MaxResults = 3
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, projectDeploymentsRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Coverage.Partial {
		t.Fatalf("a cut frontier with no member must be partial coverage: %+v", result.Coverage)
	}
	var cut *contextfabric.CoverageDetail
	for i := range result.Coverage.Details {
		if result.Coverage.Details[i].Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated {
			cut = &result.Coverage.Details[i]
		}
	}
	if cut == nil || cut.Kind != contextfabric.SubjectDeployment || unlinkedDetail(result) != nil {
		t.Fatalf("details = %+v, want the deployment truncation disclosure and no unlinked", result.Coverage.Details)
	}
}
