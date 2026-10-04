package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type seededNode struct {
	kind, id, label string
	repos           []string
	// workItemType is the stored work item `type` property, empty elsewhere.
	workItemType string
}

type seededEdge struct {
	typ, srcKind, srcID, dstKind, dstID string
}

// seededGraphConn answers the adapter's two read shapes (a node by key, the
// edges touching a node in both directions) from an explicit node and edge
// list, so a walk's reach is decided by the seeded topology and not by the
// query's call order.
func seededGraphConn(nodes []seededNode, edges []seededEdge) *fakeConn {
	byKey := map[string]seededNode{}
	for _, n := range nodes {
		byKey[n.kind+"|"+n.id] = n
	}
	return &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		kind, _ := params["kind"].(string)
		id, _ := params["id"].(string)
		switch {
		case params["relates"] != nil:
			return seededProjectLinks(byKey, edges, cypher, params), nil
		case params["fromKind"] != nil:
			return seededWalkStep(byKey, edges, cypher, params), nil
		case strings.Contains(cypher, "fulltext"), strings.Contains(cypher, "$kinds"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			var rows []row
			for i, e := range edges {
				if !(e.srcKind == kind && e.srcID == id) && !(e.dstKind == kind && e.dstID == id) {
					continue
				}
				props := map[string]interface{}{
					propRelationType: e.typ, propRelationshipID: fmt.Sprintf("rel_%03d", i),
					propEvidenceRefs: []string{fmt.Sprintf("evidence_%03d", i)},
				}
				for _, end := range [][2]string{{e.srcKind, e.srcID}, {e.dstKind, e.dstID}} {
					if end[0] == "repository" {
						props[propAuthzRepos] = byKey[end[0]+"|"+end[1]].repos
					}
				}
				rows = append(rows, row{
					"r":       &edge{Properties: props},
					"srcKind": e.srcKind, "srcId": e.srcID, "dstKind": e.dstKind, "dstId": e.dstID,
				})
			}
			return rows, nil
		default:
			n, ok := byKey[kind+"|"+id]
			if !ok {
				return nil, nil
			}
			r := fakeSubjectNodeRow(n.kind, n.id, n.label)
			if len(n.repos) > 0 {
				r["n"].(*node).Properties[propAuthzRepos] = n.repos
			}
			return []row{r}, nil
		}
	}}
}

type parentSeed struct {
	nodes []seededNode
	edges []seededEdge
	// ownedDeployments are the deployment ids that belong to repositories the parent owns.
	ownedDeployments  []string
	foreignDeployment string
}

// seedParentDeployments builds, for each repository host, two owned
// repositories with two deployments each and one repository the parent does
// not own. The owned repositories are linked to the team only by the edge the
// projection emits: repository -OWNED_BY_TEAM-> team.
func seedParentDeployments(parentKind string) parentSeed {
	var s parentSeed
	parentID := parentKind + ":payments"
	s.nodes = append(s.nodes, seededNode{kind: parentKind, id: parentID, label: "payments"})
	for _, host := range []string{"github", "gitlab"} {
		for r := 0; r < 2; r++ {
			slug := fmt.Sprintf("acme/%s-repo%d", host, r)
			repoID := fmt.Sprintf("repository:%s:%s", host, slug)
			s.nodes = append(s.nodes, seededNode{kind: "repository", id: repoID, label: slug, repos: []string{slug}})
			switch parentKind {
			case "team":
				s.nodes[0].repos = append(s.nodes[0].repos, slug)
				s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "repository", repoID, "team", parentID})
			}
			for d := 0; d < 2; d++ {
				depID := fmt.Sprintf("deployment:%s:%d:%d", host, r, d)
				s.nodes = append(s.nodes, seededNode{kind: "deployment", id: depID, label: depID, repos: []string{slug}})
				s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", depID, "repository", repoID})
				s.ownedDeployments = append(s.ownedDeployments, depID)
			}
		}
	}
	foreignRepo := "repository:github:acme/foreign"
	s.foreignDeployment = "deployment:foreign:0"
	s.nodes = append(s.nodes,
		seededNode{kind: "repository", id: foreignRepo, label: "acme/foreign", repos: []string{"acme/foreign"}},
		seededNode{kind: "deployment", id: s.foreignDeployment, label: s.foreignDeployment, repos: []string{"acme/foreign"}})
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", s.foreignDeployment, "repository", foreignRepo})
	sort.Strings(s.ownedDeployments)
	return s
}

func deploymentMembersFrame() *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"payments"}, MemberKind: contextfabric.SubjectDeployment},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
}

func reachedDeployments(t *testing.T, parentKind string, principal storage.Principal) ([]string, contextfabric.GraphContext) {
	t.Helper()
	s := seedParentDeployments(parentKind)
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(parentKind), CanonicalID: parentKind + ":payments", Label: "payments"}
	request := ownershipRoutingRequest(deploymentMembersFrame(), anchor)
	request.Request.Options.MaxCohortMembers = 50
	result, err := adapter.DiscoverContext(context.Background(), principal, request)
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

func TestTeamDeploymentMembersAreReachedPerProviderHost(t *testing.T) {
	got, result := reachedDeployments(t, "team", storage.Principal{OrgID: "org-1"})
	want := seedParentDeployments("team").ownedDeployments
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("team reached %v, want every owned deployment %v", got, want)
	}
	for _, host := range []string{"github", "gitlab"} {
		n := 0
		for _, id := range got {
			if strings.HasPrefix(id, "deployment:"+host+":") {
				n++
			}
		}
		if n != 4 {
			t.Errorf("host %s: reached %d deployments, want 4", host, n)
		}
	}
	for _, id := range got {
		if id == seedParentDeployments("team").foreignDeployment {
			t.Fatalf("reached %s, a deployment of a repository the team does not own", id)
		}
	}
	if result.CohortPopulation != len(want) || result.Cohort == nil || !result.Cohort.Complete {
		t.Fatalf("population=%d cohort=%+v, want %d and a complete cohort", result.CohortPopulation, result.Cohort, len(want))
	}
}

func TestTeamDeploymentMembersFollowTheCallersRepositoryGrant(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/github-repo0", "acme/github-repo1"}}
	got, result := reachedDeployments(t, "team", principal)
	for _, id := range got {
		if !strings.HasPrefix(id, "deployment:github:") {
			t.Fatalf("restricted caller reached %s outside the granted repositories", id)
		}
	}
	if len(got) != 4 || result.CohortPopulation != 4 {
		t.Fatalf("restricted caller reached %v population=%d, want the 4 granted deployments and a population of 4 (denied members must not count)", got, result.CohortPopulation)
	}
}

// seededProjectLinks answers the project walk's link read and issue count from
// the seeded topology: the project's issues (work items that are not pull
// requests, joined by BELONGS_TO_PROJECT) and, for the link read, one row per
// RELATES_TO link from such an issue to a pull-request work item, in the
// read's order, paged by skip and limit.
func seededProjectLinks(byKey map[string]seededNode, edges []seededEdge, cypher string, params map[string]interface{}) []row {
	project, _ := params["project"].(string)
	isPullRequest := func(n seededNode) bool { return n.workItemType == "pr" || n.workItemType == "merge_request" }
	issues := map[string]seededNode{}
	for _, e := range edges {
		if e.typ != "BELONGS_TO_PROJECT" || e.dstKind != "project" || e.dstID != project || e.srcKind != "work_item" {
			continue
		}
		if n, ok := byKey["work_item|"+e.srcID]; ok && !isPullRequest(n) {
			issues[n.id] = n
		}
	}
	if strings.Contains(cypher, "count(DISTINCT i)") {
		return []row{{"issues": int64(len(issues))}}
	}
	type link struct {
		issue, pullRequest seededNode
		rel                string
	}
	var links []link
	for i, e := range edges {
		if e.typ != "RELATES_TO" || e.srcKind != "work_item" || e.dstKind != "work_item" {
			continue
		}
		for _, pair := range [][2]string{{e.srcID, e.dstID}, {e.dstID, e.srcID}} {
			issue, isIssue := issues[pair[0]]
			pullRequest, ok := byKey["work_item|"+pair[1]]
			if isIssue && ok && isPullRequest(pullRequest) {
				links = append(links, link{issue, pullRequest, fmt.Sprintf("rel_%03d", i)})
			}
		}
	}
	sort.Slice(links, func(a, b int) bool {
		if links[a].issue.id != links[b].issue.id {
			return links[a].issue.id < links[b].issue.id
		}
		if links[a].pullRequest.id != links[b].pullRequest.id {
			return links[a].pullRequest.id < links[b].pullRequest.id
		}
		return links[a].rel < links[b].rel
	})
	skip, _ := params["skip"].(int)
	limit, _ := params["limit"].(int)
	if skip > len(links) {
		skip = len(links)
	}
	links = links[skip:]
	if limit >= 0 && len(links) > limit {
		links = links[:limit]
	}
	asNode := func(n seededNode) *node {
		out := fakeSubjectNodeRow(n.kind, n.id, n.label)["n"].(*node)
		if len(n.repos) > 0 {
			out.Properties[propAuthzRepos] = n.repos
		}
		if n.workItemType != "" {
			out.Properties[propWorkItemType] = n.workItemType
		}
		return out
	}
	rows := make([]row, 0, len(links))
	for _, l := range links {
		rows = append(rows, row{
			"i": asNode(l.issue), "pr": asNode(l.pullRequest),
			"rl": &edge{Properties: map[string]interface{}{propRelationType: "RELATES_TO", propRelationshipID: l.rel}},
		})
	}
	return rows
}

// seededWalkStep answers one project-walk step from the seeded topology: the
// neighbours of the batched ids along one relationship type, in the direction
// the query pattern names, with the optional work item type filter applied.
func seededWalkStep(byKey map[string]seededNode, edges []seededEdge, cypher string, params map[string]interface{}) []row {
	fromKind, _ := params["fromKind"].(string)
	toKind, _ := params["toKind"].(string)
	relation, _ := params["rel"].(string)
	ids, _ := params["ids"].([]interface{})
	include := strings.Contains(cypher, "b."+propWorkItemType+" IN $btypes") && !strings.Contains(cypher, "NOT b."+propWorkItemType)
	exclude := strings.Contains(cypher, "NOT b."+propWorkItemType)
	types := map[string]bool{}
	if list, ok := params["btypes"].([]interface{}); ok {
		for _, v := range list {
			types[v.(string)] = true
		}
	}
	incoming := strings.Contains(cypher, "<-[r")
	outgoing := !incoming && strings.Contains(cypher, "]->")
	var rows []row
	for _, rawID := range ids {
		id := rawID.(string)
		for i, e := range edges {
			if e.typ != relation {
				continue
			}
			var otherKind, otherID string
			switch {
			case outgoing && e.srcKind == fromKind && e.srcID == id:
				otherKind, otherID = e.dstKind, e.dstID
			case incoming && e.dstKind == fromKind && e.dstID == id:
				otherKind, otherID = e.srcKind, e.srcID
			case !incoming && !outgoing && e.srcKind == fromKind && e.srcID == id:
				otherKind, otherID = e.dstKind, e.dstID
			case !incoming && !outgoing && e.dstKind == fromKind && e.dstID == id:
				otherKind, otherID = e.srcKind, e.srcID
			default:
				continue
			}
			other, ok := byKey[otherKind+"|"+otherID]
			if !ok || otherKind != toKind {
				continue
			}
			if include && !types[other.workItemType] {
				continue
			}
			if exclude && types[other.workItemType] {
				continue
			}
			n := fakeSubjectNodeRow(other.kind, other.id, other.label)["n"].(*node)
			if len(other.repos) > 0 {
				n.Properties[propAuthzRepos] = other.repos
			}
			if other.workItemType != "" {
				n.Properties[propWorkItemType] = other.workItemType
			}
			rows = append(rows, row{
				"id": id, "b": n,
				"r": &edge{Properties: map[string]interface{}{propRelationType: e.typ, propRelationshipID: fmt.Sprintf("rel_%03d", i), propEvidenceRefs: []string{fmt.Sprintf("evidence_%03d", i)}}},
			})
		}
	}
	if limit, ok := params["limit"].(int); ok && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}
