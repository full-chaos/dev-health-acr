package falkorgraph

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func pathPositions(path []treeHop) []treePosition {
	if len(path) == 0 {
		return nil
	}
	out := []treePosition{path[0].from}
	for _, hop := range path {
		out = append(out, hop.to)
	}
	return out
}

// TestTheWalkPathIsThePathOfTheEntityTree pins the path every pair of
// positions walks: the tree Repository <> Pull request <> Issue <> Project,
// with deployments and teams hanging off it.
func TestTheWalkPathIsThePathOfTheEntityTree(t *testing.T) {
	for _, c := range []struct {
		from, to treePosition
		want     []treePosition
	}{
		{treeProject, treeDeployment, []treePosition{treeProject, treeIssue, treePullRequest, treeRepository, treeDeployment}},
		{treeRepository, treeDeployment, []treePosition{treeRepository, treeDeployment}},
		{treeTeam, treeDeployment, []treePosition{treeTeam, treeRepository, treeDeployment}},
		{treeRepository, treeIssue, []treePosition{treeRepository, treePullRequest, treeIssue}},
		{treeProject, treeRepository, []treePosition{treeProject, treeIssue, treePullRequest, treeRepository}},
		{treeRepository, treeProject, []treePosition{treeRepository, treePullRequest, treeIssue, treeProject}},
		{treeTeam, treeIssue, []treePosition{treeTeam, treeProject, treeIssue}},
	} {
		path, ok := treePath(c.from, c.to)
		if got := pathPositions(path); !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s -> %s: path %v (ok %t), want %v", c.from, c.to, got, ok, c.want)
		}
	}
}

// TestALeafIsNeverPassedThrough: a team owns both a project and a repository,
// but ownership is not the tree. A project reaches a repository through its
// issues' linked pull requests, never through a team that owns both.
func TestALeafIsNeverPassedThrough(t *testing.T) {
	for _, pair := range [][2]treePosition{{treeProject, treeRepository}, {treeProject, treeDeployment}, {treeRepository, treeIssue}} {
		path, ok := treePath(pair[0], pair[1])
		if !ok {
			t.Fatalf("%s -> %s: no path", pair[0], pair[1])
		}
		for _, hop := range path[:len(path)-1] {
			if treeNodes[hop.to].leaf {
				t.Fatalf("%s -> %s passes through the leaf %s: %v", pair[0], pair[1], hop.to, pathPositions(path))
			}
		}
	}
}

// TestPairsOffTheTreeHaveNoPath: a position to itself, an unknown position,
// and a pair with two shortest paths have no path; the walk refuses them.
func TestPairsOffTheTreeHaveNoPath(t *testing.T) {
	for _, pair := range [][2]treePosition{{treeTeam, treeTeam}, {treeProject, "work_item"}, {"work_item", treeDeployment}} {
		if path, ok := treePath(pair[0], pair[1]); ok {
			t.Errorf("%s -> %s: path %v, want none", pair[0], pair[1], pathPositions(path))
		}
	}
	saved := entityTree
	t.Cleanup(func() { entityTree = saved })
	// An edge between repository and project makes repository -> pull
	// request -> issue and repository -> project -> issue two shortest paths.
	entityTree = append(append([]treeEdge(nil), saved...), treeEdge{child: treeRepository, parent: treeProject, relation: contractsv1.ContextFabricRelationshipBelongsToProject})
	if path, ok := treePath(treeRepository, treeIssue); ok {
		t.Fatalf("repository -> issue with two shortest paths: path %v, want none", pathPositions(path))
	}
}

// TestTheDerivedStepsAreTheStepsTheWalksRead pins each hop's read: kinds,
// relation, direction and the work-item type of the neighbour.
func TestTheDerivedStepsAreTheStepsTheWalksRead(t *testing.T) {
	project, _ := treePath(treeProject, treeDeployment)
	team, _ := treePath(treeTeam, treeDeployment)
	repository, _ := treePath(treeRepository, treeIssue)
	for name, c := range map[string]struct {
		got, want walkStep
	}{
		"project: issues": {project[0].step, walkStep{fromKind: contractsv1.ContextFabricSubjectProject, toKind: contractsv1.ContextFabricSubjectWorkItem,
			relation: contractsv1.ContextFabricRelationshipBelongsToProject, direction: walkIn, notToTypes: pullRequestWorkItemTypes}},
		"project: linked pull requests": {project[1].step, walkStep{fromKind: contractsv1.ContextFabricSubjectWorkItem, toKind: contractsv1.ContextFabricSubjectWorkItem,
			relation: contractsv1.ContextFabricRelationshipRelatesTo, direction: walkEither, toTypes: pullRequestWorkItemTypes}},
		"project: repositories": {project[2].step, walkStep{fromKind: contractsv1.ContextFabricSubjectWorkItem, toKind: contractsv1.ContextFabricSubjectRepository,
			relation: contractsv1.ContextFabricRelationshipBelongsToRepository, direction: walkOut}},
		"project: deployments": {project[3].step, walkStep{fromKind: contractsv1.ContextFabricSubjectRepository, toKind: contractsv1.ContextFabricSubjectDeployment,
			relation: contractsv1.ContextFabricRelationshipBelongsToRepository, direction: walkIn}},
		"team: owned repositories": {team[0].step, walkStep{fromKind: contractsv1.ContextFabricSubjectTeam, toKind: contractsv1.ContextFabricSubjectRepository,
			relation: contractsv1.ContextFabricRelationshipOwnedByTeam, direction: walkIn}},
		"repository: pull requests": {repository[0].step, walkStep{fromKind: contractsv1.ContextFabricSubjectRepository, toKind: contractsv1.ContextFabricSubjectWorkItem,
			relation: contractsv1.ContextFabricRelationshipBelongsToRepository, direction: walkIn, toTypes: pullRequestWorkItemTypes}},
		"repository: linked issues": {repository[1].step, walkStep{fromKind: contractsv1.ContextFabricSubjectWorkItem, toKind: contractsv1.ContextFabricSubjectWorkItem,
			relation: contractsv1.ContextFabricRelationshipRelatesTo, direction: walkEither, notToTypes: pullRequestWorkItemTypes}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: step %+v, want %+v", name, c.got, c.want)
		}
	}
	if !team[0].edge.ownership || project[2].edge.ownership {
		t.Error("only the ownership hop reads under the ownership window and census bound")
	}
}

// TestALinkOrderRanksLinksBeforeTheirEndpoints: a link property, once the
// tree names one, orders the link rows first, highest first, so a cut keeps
// the higher-ranked links; without one the order is the endpoints'.
func TestALinkOrderRanksLinksBeforeTheirEndpoints(t *testing.T) {
	feed, link := projectLinkHops(t)
	plain := linkSegmentCypher(feed, link, temporalFilter{}, false)
	if !strings.Contains(plain, " ORDER BY m."+propCanonicalID+", b.") {
		t.Fatalf("link read = %q, want the endpoints' order", plain)
	}
	link.edge.linkOrder = "link_rank"
	ranked := linkSegmentCypher(feed, link, temporalFilter{}, false)
	if !strings.Contains(ranked, " ORDER BY rl."+propPropertyPrefix+"link_rank DESC, m."+propCanonicalID+", b.") {
		t.Fatalf("link read = %q, want the link property first, then the endpoints", ranked)
	}
}

func walkTreeWithScope(t *testing.T, s projectSeed, anchor contextfabric.SubjectRef, scope contextfabric.RequestedScope, limit int) treeWalk {
	t.Helper()
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	walk, err := adapter.anchorDeploymentMembers(context.Background(), "key", "org-1", storage.Principal{OrgID: "org-1"}, scope,
		anchor, limit, newTemporalFilter(contextfabric.TimeContext{}))
	if err != nil {
		t.Fatalf("anchorDeploymentMembers() error = %v", err)
	}
	return walk
}

func walkMemberIDs(walk treeWalk) []string {
	var out []string
	for _, n := range walk.nodes {
		if subject, ok := graphrank.NodeSubject(n); ok {
			out = append(out, subject.CanonicalID)
		}
	}
	sort.Strings(out)
	return out
}

// TestEachLinkRowIsAuthorizedAtBothEnds: a caller with no repository grant
// but a requested repository scope gets no grant clause in the link read, so
// the walk's own check of each row is what keeps a link out. A pull request
// outside the scope is not followed, and neither is a link from an issue whose
// own repository is outside it.
func TestEachLinkRowIsAuthorizedAtBothEnds(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	inScope := s.repository("acme/in-scope", 1)
	farOut := s.repository("acme/out-of-scope", 1)
	scopedOnly := s.repository("acme/scoped-only", 1)
	s.link("in", "work_item:gh:1", []string{"acme/in-scope"}, "work_item:ghpr:1", "pr", inScope, "acme/in-scope", false)
	s.link("far", "work_item:linear:ENG-1", nil, "work_item:ghpr:2", "pr", farOut, "acme/out-of-scope", false)
	s.link("near", "work_item:gh:9", []string{"acme/elsewhere"}, "work_item:ghpr:3", "pr", scopedOnly, "acme/scoped-only", false)
	scope := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/in-scope", "acme/scoped-only"}}
	walk := walkTreeWithScope(t, s, contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID}, scope, 25)
	if got := walkMemberIDs(walk); strings.Join(got, ",") != "deployment:acme/in-scope:0" {
		t.Fatalf("members %v, want only the deployment reached by the link whose issue and pull request are both in scope", got)
	}
	if walk.denied != 2 {
		t.Fatalf("denied %d, want the two links refused at one end each", walk.denied)
	}
}

// TestALinkReadStopsAtItsPageCapAsTruncation: links the caller cannot see
// fill whole pages; the read stops at its page cap and says it was cut,
// rather than reading every page of the anchor.
func TestALinkReadStopsAtItsPageCapAsTruncation(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	hidden := s.repository("acme/hidden", 1)
	for i := 0; i < 2*linkSegmentPageCap+1; i++ {
		s.link("hidden", fmt.Sprintf("work_item:gh:a%03d", i), []string{"acme/hidden"}, fmt.Sprintf("work_item:ghpr:a%03d", i), "pr", hidden, "acme/hidden", false)
	}
	visible := s.repository("acme/visible", 1)
	s.link("visible", "work_item:gh:z", []string{"acme/visible"}, "work_item:ghpr:z", "pr", visible, "acme/visible", false)
	walk := walkTreeWithScope(t, s, contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID},
		contextfabric.RequestedScope{RepositorySlugs: []string{"acme/visible"}}, 1)
	if !walk.truncated || len(walk.nodes) != 0 {
		t.Fatalf("truncated %t, members %v; want a cut read past %d pages of hidden links", walk.truncated, walkMemberIDs(walk), linkSegmentPageCap)
	}
}

// TestADeploymentOfTwoReachedRepositoriesIsOneMember: a member reached from
// two nodes of the frontier is one member.
func TestADeploymentOfTwoReachedRepositoriesIsOneMember(t *testing.T) {
	s := seedParentDeployments("team")
	shared := "deployment:github:0:0"
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", shared, "repository", "repository:github:acme/github-repo1"})
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	walk, err := adapter.anchorDeploymentMembers(context.Background(), "key", "org-1", storage.Principal{OrgID: "org-1"}, contextfabric.RequestedScope{},
		contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:payments"}, 50, newTemporalFilter(contextfabric.TimeContext{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := walkMemberIDs(walk); strings.Join(got, ",") != strings.Join(s.ownedDeployments, ",") {
		t.Fatalf("members %v, want each owned deployment once: %v", got, s.ownedDeployments)
	}
}

// TestALinkWhoseFarEndIsTheMemberAdmitsOnlyFarEndsTheCallerMaySee: when the
// link ends the path (a repository's issues), the far end of each link row is
// the member, and the walk's own check of that end is the only thing keeping
// an unseen issue out. The caller holds grants for both repositories, so the
// link read's grant clause keeps every row; the request is narrowed to the
// anchor's repository, which only the walk's check of each row applies. An
// issue whose own repository is outside the request is not a member; a
// repository-less issue is admitted by its link to the granted pull request.
func TestALinkWhoseFarEndIsTheMemberAdmitsOnlyFarEndsTheCallerMaySee(t *testing.T) {
	repo := "repository:github:acme/svc"
	nodes := []seededNode{{kind: "repository", id: repo, label: "acme/svc", repos: []string{"acme/svc"}}}
	var edges []seededEdge
	for i, issue := range []seededNode{
		{kind: "work_item", id: "work_item:gh:visible", label: "gh:visible", repos: []string{"acme/svc"}, workItemType: "issue"},
		{kind: "work_item", id: "work_item:gh:secret", label: "gh:secret", repos: []string{"acme/secret"}, workItemType: "issue"},
		{kind: "work_item", id: "work_item:linear:ENG-1", label: "linear:ENG-1", repos: []string{noRepositoryScope}, workItemType: "issue"},
	} {
		pr := seededNode{kind: "work_item", id: fmt.Sprintf("work_item:ghpr:%d", i), label: fmt.Sprintf("ghpr:%d", i), repos: []string{"acme/svc"}, workItemType: "pr"}
		nodes = append(nodes, issue, pr)
		edges = append(edges,
			seededEdge{"BELONGS_TO_REPOSITORY", "work_item", pr.id, "repository", repo},
			seededEdge{"RELATES_TO", "work_item", pr.id, "work_item", issue.id})
	}
	adapter := newFakeAdapter(t, seededGraphConn(nodes, edges))
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/svc", "acme/secret"}}
	scope := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/svc"}}
	walk, err := adapter.treeMembers(context.Background(), "key", "org-1", principal, scope,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: repo, Label: "acme/svc"}, treeIssue, 25, newTemporalFilter(contextfabric.TimeContext{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(walkMemberIDs(walk), ","); got != "work_item:gh:visible,work_item:linear:ENG-1" {
		t.Fatalf("members %s, want the issue in the requested repository and the repository-less issue, never the issue of acme/secret", got)
	}
	if walk.denied != 1 || walk.linkTargets != 3 || walk.linkSources != 3 {
		t.Fatalf("denied %d, link targets %d, link sources %d; want 1 denied of 3 linked issues from 3 pull requests", walk.denied, walk.linkTargets, walk.linkSources)
	}
}
