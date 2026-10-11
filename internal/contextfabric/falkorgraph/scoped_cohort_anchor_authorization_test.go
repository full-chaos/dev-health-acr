package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// restrictedAnchorFake serves the scoped-anchor graph to a repository-restricted
// caller: every node and edge carries the granted repository except the ones
// named, which carry another repository the caller holds no grant for.
func restrictedAnchorFake(nodes []scopedAnchorNode, edges []scopedAnchorEdge, deniedNodeID, deniedRelation string) *fakeConn {
	inner := scopedAnchorFake(nodes, edges, nil)
	base := inner.queryFunc
	inner.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		rows, err := base(ctx, graphKey, cypher, params, readOnly)
		for _, r := range rows {
			for _, v := range r {
				switch x := v.(type) {
				case *node:
					repos := []string{"acme/granted"}
					if propStringValue(x.Properties[propCanonicalID]) == deniedNodeID {
						repos = []string{"acme/secret"}
					}
					x.Properties[propAuthzRepos] = repos
				case *edge:
					repos := []string{"acme/granted"}
					if deniedRelation != "" && propStringValue(x.Properties[propRelationType]) == deniedRelation {
						repos = []string{"acme/secret"}
					}
					x.Properties[propAuthzRepos] = repos
				}
			}
		}
		return rows, err
	}
	return inner
}

func discoverAsRestricted(t *testing.T, fake *fakeConn, anchor contextfabric.SubjectRef, member contextfabric.SubjectKind, question string) map[string]bool {
	t.Helper()
	frame := projectsOfAnchorFrame(anchor.Label)
	frame.SubjectExpression.Scoped.MemberKind = member
	request := ownershipRoutingRequest(frame, anchor)
	request.ScopeAnchorKind = anchor.Kind
	request.Request.Question = question
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/granted"}}
	result, err := newTeamAdapter(t, fake).DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	return cohortIDs(result.Cohort)
}

// A caller who cannot see the anchor is served none of its members, over any
// first hop of the path, not only an ownership hop.
func TestScopedCohortDeniedAnchorServesNoMember(t *testing.T) {
	cases := []struct {
		name    string
		anchor  contextfabric.SubjectRef
		member  contextfabric.SubjectKind
		nodes   []scopedAnchorNode
		edges   []scopedAnchorEdge
		member1 string
		q       string
	}{
		{
			name: "repository_anchor_pull_request_members", anchor: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:alpha", Label: "full-chaos/alpha"},
			member:  contextfabric.SubjectPullRequest,
			nodes:   []scopedAnchorNode{{"repository", "repository:alpha", "full-chaos/alpha"}, {"pull_request", "pull_request:1", "Alpha fix"}},
			edges:   []scopedAnchorEdge{{rel: "BELONGS_TO_REPOSITORY", srcKind: "pull_request", srcID: "pull_request:1", dstKind: "repository", dstID: "repository:alpha"}},
			member1: "pull_request:1", q: "which pull requests does repository alpha have?",
		},
		{
			name: "project_anchor_pull_request_members", anchor: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:alpha", Label: "Alpha"},
			member: contextfabric.SubjectPullRequest,
			nodes:  []scopedAnchorNode{{"project", "project:alpha", "Alpha"}, {"work_item", "issue:1", "ALPHA-1"}, {"pull_request", "pull_request:1", "Alpha fix"}},
			edges: []scopedAnchorEdge{
				{rel: "BELONGS_TO_PROJECT", srcKind: "work_item", srcID: "issue:1", dstKind: "project", dstID: "project:alpha"},
				{rel: "LINKS_PULL_REQUEST", srcKind: "work_item", srcID: "issue:1", dstKind: "pull_request", dstID: "pull_request:1", linkTier: "native"},
			},
			member1: "pull_request:1", q: "which pull requests does project Alpha have?",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			granted := restrictedAnchorFake(tc.nodes, tc.edges, "", "")
			if ids := discoverAsRestricted(t, granted, tc.anchor, tc.member, tc.q); !ids[tc.member1] {
				t.Fatalf("control: with every node granted the member must be served, got %v", ids)
			}
			denied := restrictedAnchorFake(tc.nodes, tc.edges, tc.anchor.CanonicalID, "")
			if ids := discoverAsRestricted(t, denied, tc.anchor, tc.member, tc.q); len(ids) != 0 {
				t.Errorf("DENIED ANCHOR SERVED MEMBERS: %v from %s the caller cannot see", ids, tc.anchor.CanonicalID)
			}
		})
	}
}

// A member is served only over relationships the caller can read.
func TestScopedCohortUnreadableRelationshipServesNoMember(t *testing.T) {
	cases := []struct {
		name     string
		anchor   contextfabric.SubjectRef
		member   contextfabric.SubjectKind
		nodes    []scopedAnchorNode
		edges    []scopedAnchorEdge
		denyRel  string
		memberID string
		q        string
	}{
		{
			name: "belongs_to_repository", anchor: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:alpha", Label: "full-chaos/alpha"},
			member:  contextfabric.SubjectPullRequest,
			nodes:   []scopedAnchorNode{{"repository", "repository:alpha", "full-chaos/alpha"}, {"pull_request", "pull_request:1", "Alpha fix"}},
			edges:   []scopedAnchorEdge{{rel: "BELONGS_TO_REPOSITORY", srcKind: "pull_request", srcID: "pull_request:1", dstKind: "repository", dstID: "repository:alpha"}},
			denyRel: "BELONGS_TO_REPOSITORY", memberID: "pull_request:1", q: "which pull requests does repository alpha have?",
		},
		{
			name: "links_pull_request", anchor: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:alpha", Label: "Alpha"},
			member: contextfabric.SubjectPullRequest,
			nodes:  []scopedAnchorNode{{"project", "project:alpha", "Alpha"}, {"work_item", "issue:1", "ALPHA-1"}, {"pull_request", "pull_request:1", "Alpha fix"}},
			edges: []scopedAnchorEdge{
				{rel: "BELONGS_TO_PROJECT", srcKind: "work_item", srcID: "issue:1", dstKind: "project", dstID: "project:alpha"},
				{rel: "LINKS_PULL_REQUEST", srcKind: "work_item", srcID: "issue:1", dstKind: "pull_request", dstID: "pull_request:1", linkTier: "native"},
			},
			denyRel: "LINKS_PULL_REQUEST", memberID: "pull_request:1", q: "which pull requests does project Alpha have?",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if ids := discoverAsRestricted(t, restrictedAnchorFake(tc.nodes, tc.edges, "", ""), tc.anchor, tc.member, tc.q); !ids[tc.memberID] {
				t.Fatalf("control: with every relationship readable the member must be served, got %v", ids)
			}
			ids := discoverAsRestricted(t, restrictedAnchorFake(tc.nodes, tc.edges, "", tc.denyRel), tc.anchor, tc.member, tc.q)
			if ids[tc.memberID] {
				t.Errorf("UNREADABLE RELATIONSHIP SERVED: %s reached over an unreadable %s edge: %v", tc.memberID, tc.denyRel, ids)
			}
		})
	}
}
