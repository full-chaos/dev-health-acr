package falkorgraph

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestTheWalkReadsReturnTheWalkPropertiesNotWholeNodes: the link read and a
// hop that feeds the next one return a map of the properties the walk decides
// with, never a whole node (which can carry wide text and an embedding); the
// members' hop and a project position stay whole.
func TestTheWalkReadsReturnTheWalkPropertiesNotWholeNodes(t *testing.T) {
	for _, pair := range [][2]treePosition{{treeRepository, treeIssue}, {treeProject, treeDeployment}} {
		path, ok := treePath(pair[0], pair[1])
		if !ok {
			t.Fatalf("no path %s -> %s", pair[0], pair[1])
		}
		for _, restricted := range []bool{false, true} {
			cypher := linkSegmentCypher(path[0], path[1], newTemporalFilter(contextfabric.TimeContext{}), restricted)
			if strings.Contains(cypher, "RETURN m, b, rl") || strings.Contains(cypher, propEmbedding) || strings.Contains(cypher, propSearchText) {
				t.Errorf("%s -> %s: the link read returns whole nodes: %s", pair[0], pair[1], cypher)
			}
			for _, property := range walkNodeProperties {
				if !strings.Contains(cypher, property+": m."+property) || !strings.Contains(cypher, property+": b."+property) {
					t.Errorf("%s -> %s: the link read does not return %s of both ends", pair[0], pair[1], property)
				}
			}
			if !strings.Contains(cypher, propPropertyPrefix+linkTierProperty+": rl."+propPropertyPrefix+linkTierProperty) {
				t.Errorf("%s -> %s: the link read does not return the link tier", pair[0], pair[1])
			}
		}
	}
	step := walkStep{fromKind: treeNodes[treePullRequest].kind, toKind: treeNodes[treeRepository].kind, relation: "BELONGS_TO_REPOSITORY", direction: walkOut}
	if whole := walkStepCypher(step, temporalFilter{}); !strings.Contains(whole, "RETURN id, b, r ") {
		t.Errorf("a members' hop does not read whole nodes: %s", whole)
	}
	step.projected = true
	if projected := walkStepCypher(step, temporalFilter{}); strings.Contains(projected, "RETURN id, b, r ") || !strings.Contains(projected, propAuthzRepos+": b."+propAuthzRepos) {
		t.Errorf("a feeding hop reads whole nodes: %s", projected)
	}
	if walkNodeProjected(treeProject) {
		t.Error("a project position is projected: the project reach rewrite needs the whole node")
	}
}

// TestAProjectedNodeReadsAsTheWholeNode: a projection map decodes to the same
// property values the whole node decodes to (all-string lists as []string).
func TestAProjectedNodeReadsAsTheWholeNode(t *testing.T) {
	values := map[string]interface{}{
		propKind: "work_item", propCanonicalID: "work_item.v2:r:1", propLabel: "Issue",
		propAuthzRepos: []interface{}{"acme/svc"}, propAuthzProjects: "*", propAuthzTeams: nil,
	}
	whole := &node{Properties: normalizeProperties(values)}
	if got := walkNode(values); !reflect.DeepEqual(got.Properties, whole.Properties) {
		t.Fatalf("projected %#v, whole %#v", got.Properties, whole.Properties)
	}
	if got := walkNode(whole); got != whole {
		t.Fatal("a whole node is not read as itself")
	}
	if walkNode("x") != nil || walkEdge(3) != nil {
		t.Fatal("a non-node column read as a node")
	}
	tier, ok := linkTierOf(walkEdge(map[string]interface{}{propPropertyPrefix + linkTierProperty: "native"}))
	if !ok || tier.name != "native" {
		t.Fatalf("a projected link reads tier %v %t", tier, ok)
	}
}

// TestAWalkTheLinkEndsServesIdentitiesNotProjectedNodes: when the link ends
// the path its far side is read as a projection, so the members are handed
// on as identities (linkSubjects), never as nodes a reader could ask for a
// property the read did not return.
func TestAWalkTheLinkEndsServesIdentitiesNotProjectedNodes(t *testing.T) {
	s := newMemberSeed()
	s.issue("work_item.v2:p:1", []string{memberAnchorSlug})
	s.pullRequest("pull_request:p:1", memberAnchorSlug)
	s.link("work_item.v2:p:1", "pull_request:p:1", "native")
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	walk, err := adapter.treeMembers(context.Background(), "key", "org-1", open(), contextfabric.RequestedScope{}, anchor, treeIssue, 25, temporalFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(walk.nodes) != 0 || len(walk.linkSubjects) != 1 || walk.linkSubjects[0].CanonicalID != "work_item.v2:p:1" {
		t.Fatalf("nodes %d, link subjects %v: want the one member as an identity and no node", len(walk.nodes), walk.linkSubjects)
	}
}
