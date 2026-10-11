package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// scopedAnchorEdge is one stored edge of a synthetic graph: src -rel-> dst.
type scopedAnchorEdge struct {
	rel                    string
	srcKind, srcID         string
	dstKind, dstID         string
	linkTier               string
	relationshipIDOverride string
}

type scopedAnchorNode struct{ kind, id, label string }

// scopedAnchorFake serves one synthetic graph: the lexical arms return every
// listed text match (owned and unowned alike, as a question-text match does),
// the per-node edge read returns the edges touching the node, and a node read
// returns the node.
func scopedAnchorFake(nodes []scopedAnchorNode, edges []scopedAnchorEdge, textMatches []scopedAnchorNode) *fakeConn {
	byID := map[string]scopedAnchorNode{}
	for _, n := range nodes {
		byID[n.id] = n
	}
	seededByKey := map[string]seededNode{}
	for _, n := range nodes {
		sn := seededNode{kind: n.kind, id: n.id, label: n.label, repos: []string{"acme/granted"}}
		if n.kind == "work_item" {
			sn.workItemType = "story"
		}
		seededByKey[n.kind+"|"+n.id] = sn
	}
	var seededEdges []seededEdge
	for _, e := range edges {
		seededEdges = append(seededEdges, seededEdge{typ: e.rel, srcKind: e.srcKind, srcID: e.srcID, dstKind: e.dstKind, dstID: e.dstID, tier: e.linkTier})
	}
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case params["linkRel"] != nil:
			return seededProjectLinks(seededByKey, seededEdges, cypher, params), nil
		case strings.Contains(cypher, "fulltext"):
			var rows []row
			for _, n := range textMatches {
				rows = append(rows, row{"node": &node{Properties: map[string]interface{}{propKind: n.kind, propCanonicalID: n.id, propLabel: n.label}}, "score": 1.0})
			}
			return rows, nil
		case strings.Contains(cypher, "UNION"):
			id, _ := params["id"].(string)
			var rows []row
			for i, e := range edges {
				if e.srcID != id && e.dstID != id {
					continue
				}
				props := map[string]interface{}{propRelationType: e.rel, propRelationshipID: "rel_" + e.srcID + "_" + e.dstID + "_" + string(rune('a'+i))}
				if e.linkTier != "" {
					props[propPropertyPrefix+linkTierProperty] = e.linkTier
				}
				rows = append(rows, row{"r": &edge{Properties: props}, "srcKind": e.srcKind, "srcId": e.srcID, "dstKind": e.dstKind, "dstId": e.dstID})
			}
			return rows, nil
		default:
			id, _ := params["id"].(string)
			if n, ok := byID[id]; ok {
				return []row{fakeSubjectNodeRow(n.kind, n.id, n.label)}, nil
			}
			return nil, nil
		}
	}}
}

// TestScopedCohortAnchorMembership asks, per (anchor kind, member kind), for the
// members of a named anchor when one member of the kind has a path of the entity
// tree to the anchor and another member of the kind only matches the question
// text. Expected values come from the graph built here.
func TestScopedCohortAnchorMembership(t *testing.T) {
	const (
		linked = "owned"
		stray  = "unowned"
	)
	cases := []struct {
		name         string
		anchor       scopedAnchorNode
		member       contextfabric.SubjectKind
		nodes        []scopedAnchorNode
		edges        []scopedAnchorEdge
		ownedID      string
		unownedID    string
		noTreePath   string // non-empty: combination cannot be expressed on the entity tree
		questionText string
	}{
		{
			name:   "project_anchor_repository_members",
			anchor: scopedAnchorNode{"project", "project:alpha", "Alpha"},
			member: contextfabric.SubjectRepository,
			nodes: []scopedAnchorNode{
				{"project", "project:alpha", "Alpha"}, {"work_item", "issue:1", "ALPHA-1"}, {"pull_request", "pr:1", "PR 1"},
				{"repository", "repository:owned", "full-chaos/alpha-owned"}, {"repository", "repository:unowned", "full-chaos/alpha-unowned"},
			},
			edges: []scopedAnchorEdge{
				{rel: "BELONGS_TO_PROJECT", srcKind: "work_item", srcID: "issue:1", dstKind: "project", dstID: "project:alpha"},
				{rel: "LINKS_PULL_REQUEST", srcKind: "work_item", srcID: "issue:1", dstKind: "pull_request", dstID: "pr:1", linkTier: "native"},
				{rel: "BELONGS_TO_REPOSITORY", srcKind: "pull_request", srcID: "pr:1", dstKind: "repository", dstID: "repository:owned"},
			},
			ownedID: "repository:owned", unownedID: "repository:unowned", questionText: "which repositories does project Alpha have?",
		},
		{
			name:   "project_anchor_team_members",
			anchor: scopedAnchorNode{"project", "project:alpha", "Alpha"},
			member: contextfabric.SubjectTeam,
			nodes: []scopedAnchorNode{
				{"project", "project:alpha", "Alpha"}, {"team", "team:owned", "Alpha Owners"}, {"team", "team:unowned", "Alpha Fans"},
			},
			edges: []scopedAnchorEdge{
				{rel: "OWNED_BY_TEAM", srcKind: "project", srcID: "project:alpha", dstKind: "team", dstID: "team:owned"},
			},
			ownedID: "team:owned", unownedID: "team:unowned", questionText: "which teams own project Alpha?",
		},
		{
			name:   "project_anchor_pull_request_members",
			anchor: scopedAnchorNode{"project", "project:alpha", "Alpha"},
			member: contextfabric.SubjectPullRequest,
			nodes: []scopedAnchorNode{
				{"project", "project:alpha", "Alpha"}, {"work_item", "issue:1", "ALPHA-1"},
				{"pull_request", "pull_request:owned", "Alpha fix"}, {"pull_request", "pull_request:unowned", "Alpha typo"},
			},
			edges: []scopedAnchorEdge{
				{rel: "BELONGS_TO_PROJECT", srcKind: "work_item", srcID: "issue:1", dstKind: "project", dstID: "project:alpha"},
				{rel: "LINKS_PULL_REQUEST", srcKind: "work_item", srcID: "issue:1", dstKind: "pull_request", dstID: "pull_request:owned", linkTier: "native"},
			},
			ownedID: "pull_request:owned", unownedID: "pull_request:unowned", questionText: "which pull requests does project Alpha have?",
		},
		{
			name:   "repository_anchor_project_members",
			anchor: scopedAnchorNode{"repository", "repository:alpha", "full-chaos/alpha"},
			member: contextfabric.SubjectProject,
			nodes: []scopedAnchorNode{
				{"repository", "repository:alpha", "full-chaos/alpha"}, {"pull_request", "pr:1", "PR 1"}, {"work_item", "issue:1", "ALPHA-1"},
				{"project", "project:owned", "Alpha Delivery"}, {"project", "project:unowned", "Alpha Docs"},
			},
			edges: []scopedAnchorEdge{
				{rel: "BELONGS_TO_REPOSITORY", srcKind: "pull_request", srcID: "pr:1", dstKind: "repository", dstID: "repository:alpha"},
				{rel: "LINKS_PULL_REQUEST", srcKind: "work_item", srcID: "issue:1", dstKind: "pull_request", dstID: "pr:1", linkTier: "native"},
				{rel: "BELONGS_TO_PROJECT", srcKind: "work_item", srcID: "issue:1", dstKind: "project", dstID: "project:owned"},
			},
			ownedID: "project:owned", unownedID: "project:unowned", questionText: "which projects does repository alpha have?",
		},
		{
			name:   "repository_anchor_pull_request_members",
			anchor: scopedAnchorNode{"repository", "repository:alpha", "full-chaos/alpha"},
			member: contextfabric.SubjectPullRequest,
			nodes: []scopedAnchorNode{
				{"repository", "repository:alpha", "full-chaos/alpha"},
				{"pull_request", "pull_request:owned", "Alpha fix"}, {"pull_request", "pull_request:unowned", "Alpha typo"},
			},
			edges: []scopedAnchorEdge{
				{rel: "BELONGS_TO_REPOSITORY", srcKind: "pull_request", srcID: "pull_request:owned", dstKind: "repository", dstID: "repository:alpha"},
			},
			ownedID: "pull_request:owned", unownedID: "pull_request:unowned", questionText: "which pull requests does repository alpha have?",
		},
		{
			name:   "team_anchor_pull_request_members",
			anchor: scopedAnchorNode{"team", "team:alpha", "Alpha"},
			member: contextfabric.SubjectPullRequest,
			nodes: []scopedAnchorNode{
				{"team", "team:alpha", "Alpha"}, {"repository", "repository:alpha", "full-chaos/alpha"},
				{"pull_request", "pull_request:owned", "Alpha fix"}, {"pull_request", "pull_request:unowned", "Alpha typo"},
			},
			edges: []scopedAnchorEdge{
				{rel: "OWNED_BY_TEAM", srcKind: "repository", srcID: "repository:alpha", dstKind: "team", dstID: "team:alpha"},
				{rel: "BELONGS_TO_REPOSITORY", srcKind: "pull_request", srcID: "pull_request:owned", dstKind: "repository", dstID: "repository:alpha"},
			},
			ownedID: "pull_request:owned", unownedID: "pull_request:unowned", questionText: "which pull requests does team Alpha have?",
		},
		{
			name:       "project_anchor_incident_members",
			anchor:     scopedAnchorNode{"project", "project:alpha", "Alpha"},
			member:     contextfabric.SubjectIncident,
			noTreePath: "incident is not a position of the entity tree (treeNodes), so no actual-link path exists to build",
		},
		{
			name:       "repository_anchor_incident_members",
			anchor:     scopedAnchorNode{"repository", "repository:alpha", "full-chaos/alpha"},
			member:     contextfabric.SubjectIncident,
			noTreePath: "incident is not a position of the entity tree (treeNodes), so no actual-link path exists to build",
		},
		{
			name:       "team_anchor_incident_members",
			anchor:     scopedAnchorNode{"team", "team:alpha", "Alpha"},
			member:     contextfabric.SubjectIncident,
			noTreePath: "incident is not a position of the entity tree (treeNodes), so no actual-link path exists to build",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.noTreePath != "" {
				t.Skip(tc.noTreePath)
			}
			var text []scopedAnchorNode
			for _, n := range tc.nodes {
				if n.id == tc.ownedID || n.id == tc.unownedID {
					text = append(text, n)
				}
			}
			anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(tc.anchor.kind), CanonicalID: tc.anchor.id, Label: tc.anchor.label}
			frame := projectsOfAnchorFrame(tc.anchor.label)
			frame.SubjectExpression.Scoped.MemberKind = tc.member
			request := ownershipRoutingRequest(frame, anchor)
			request.ScopeAnchorKind = anchor.Kind
			request.Request.Question = tc.questionText
			adapter := newTeamAdapter(t, scopedAnchorFake(tc.nodes, tc.edges, text))
			result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			ids := cohortIDs(result.Cohort)
			t.Logf("served members = %v", ids)
			if ids[tc.unownedID] {
				t.Errorf("UNOWNED ADMITTED: %s has no entity-tree path to %s %s but is in the served cohort %v", tc.unownedID, tc.anchor.kind, tc.anchor.id, ids)
			}
			if !ids[tc.ownedID] {
				t.Errorf("OWNED MISSING: %s has a path to %s %s but is not in the served cohort %v", tc.ownedID, tc.anchor.kind, tc.anchor.id, ids)
			}
		})
	}
}

// TestScopedCohortAnchorMemberNamesItsLinkTier: a member reached over the issue
// and pull request link carries the tier of that link in its inclusion reasons,
// whether the link ends the path (pull requests of a project) or feeds the last
// hop (repositories of a project), and a text or inferred link is never stated
// as native.
func TestScopedCohortAnchorMemberNamesItsLinkTier(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:alpha", Label: "Alpha"}
	tiers := map[string]string{
		"native":        "Reached through a native issue and pull request link.",
		"explicit_text": "Reached through an issue and pull request link read from text, not a native link.",
		"heuristic":     "Reached through an inferred issue and pull request link, not a native link.",
	}
	for _, shape := range []string{"link_ends_path", "link_feeds_last_hop"} {
		for tier, want := range tiers {
			shape, tier, want := shape, tier, want
			t.Run(shape+"/"+tier, func(t *testing.T) {
				nodes := []scopedAnchorNode{{"project", "project:alpha", "Alpha"}, {"work_item", "issue:1", "ALPHA-1"}}
				edges := []scopedAnchorEdge{
					{rel: "BELONGS_TO_PROJECT", srcKind: "work_item", srcID: "issue:1", dstKind: "project", dstID: "project:alpha"},
				}
				member, memberID, question := contextfabric.SubjectPullRequest, "pull_request:owned", "which pull requests does project Alpha have?"
				if shape == "link_ends_path" {
					nodes = append(nodes, scopedAnchorNode{"pull_request", memberID, "Alpha fix"})
					edges = append(edges, scopedAnchorEdge{rel: "LINKS_PULL_REQUEST", srcKind: "work_item", srcID: "issue:1", dstKind: "pull_request", dstID: memberID, linkTier: tier})
				} else {
					member, memberID, question = contextfabric.SubjectRepository, "repository:owned", "which repositories does project Alpha have?"
					nodes = append(nodes, scopedAnchorNode{"pull_request", "pr:1", "PR 1"}, scopedAnchorNode{"repository", memberID, "full-chaos/alpha"})
					edges = append(edges,
						scopedAnchorEdge{rel: "LINKS_PULL_REQUEST", srcKind: "work_item", srcID: "issue:1", dstKind: "pull_request", dstID: "pr:1", linkTier: tier},
						scopedAnchorEdge{rel: "BELONGS_TO_REPOSITORY", srcKind: "pull_request", srcID: "pr:1", dstKind: "repository", dstID: memberID})
				}
				frame := projectsOfAnchorFrame(anchor.Label)
				frame.SubjectExpression.Scoped.MemberKind = member
				request := ownershipRoutingRequest(frame, anchor)
				request.ScopeAnchorKind = anchor.Kind
				request.Request.Question = question
				result, err := newTeamAdapter(t, scopedAnchorFake(nodes, edges, nil)).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
				if err != nil {
					t.Fatalf("DiscoverContext() error = %v", err)
				}
				if result.Cohort == nil || len(result.Cohort.Members) != 1 || result.Cohort.Members[0].Subject.CanonicalID != memberID {
					t.Fatalf("cohort = %+v, want exactly %s", result.Cohort, memberID)
				}
				var got []string
				for _, reason := range result.Cohort.Members[0].InclusionReasons {
					got = append(got, reason)
				}
				found := false
				for _, reason := range got {
					found = found || reason == want
					if tier != "native" && strings.Contains(reason, "native issue") {
						t.Errorf("reason %q presents a %s link as native", reason, tier)
					}
				}
				if !found {
					t.Errorf("inclusion reasons = %v, want %q", got, want)
				}
			})
		}
	}
}
