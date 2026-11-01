package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 E1/E2: a project-only caller's edge is decided by its endpoints;
// its own lists are not consulted, except that a real project list must hold
// a granted canonical identity. An attribution edge is denied.
func TestAProjectOnlyEdgeDefersToItsEndpointsAndDeniesAttribution(t *testing.T) {
	t.Parallel()
	principal := projectOnlyPrincipal()
	for _, tc := range []struct {
		name       string
		relation   string
		requested  contextfabric.RequestedScope
		attributes map[string]interface{}
		want       bool
	}{
		{name: "star project list", relation: "IN_PROJECT", attributes: map[string]interface{}{authorizationProjectsAttr: "*", authorizationRepositoriesAttr: []string{"acme/other"}}, want: true},
		{name: "absent project list", relation: "IN_PROJECT", attributes: map[string]interface{}{authorizationRepositoriesAttr: []string{"acme/other"}}, want: true},
		{name: "granted canonical project", relation: "IN_PROJECT", attributes: map[string]interface{}{authorizationProjectsAttr: []string{"project.v2:github:P-1"}}, want: true},
		{name: "raw project id", relation: "IN_PROJECT", attributes: map[string]interface{}{authorizationProjectsAttr: []string{"P-1"}}, want: false},
		{name: "empty project list", relation: "IN_PROJECT", attributes: map[string]interface{}{authorizationProjectsAttr: []string{}}, want: false},
		{name: "other bare string", relation: "IN_PROJECT", attributes: map[string]interface{}{authorizationProjectsAttr: "P-1"}, want: false},
		{name: "untyped list", relation: "IN_PROJECT", attributes: map[string]interface{}{authorizationProjectsAttr: []interface{}{"project.v2:github:P-1"}}, want: false},
		{name: "DOCUMENTED_BY", relation: "DOCUMENTED_BY", attributes: map[string]interface{}{authorizationProjectsAttr: "*"}, want: false},
		{name: "has_episode, any case", relation: "has_episode", attributes: map[string]interface{}{authorizationProjectsAttr: "*"}, want: false},
		{name: "requested project narrowing", relation: "IN_PROJECT", requested: contextfabric.RequestedScope{ProjectIDs: []string{"P-1"}}, attributes: map[string]interface{}{authorizationProjectsAttr: "*"}, want: false},
		{name: "requested repository narrowing is the endpoints'", relation: "IN_PROJECT", requested: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/api"}}, attributes: map[string]interface{}{authorizationProjectsAttr: "*", authorizationRepositoriesAttr: []string{"acme/other"}}, want: true},
	} {
		if got := AuthorizedEdge(principal, tc.requested, tc.relation, tc.attributes); got != tc.want {
			t.Errorf("%s: AuthorizedEdge = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Every other class: AuthorizedEdge is AuthorizedAttributes, whatever the
// relation.
func TestAnEdgeOfAnotherClassIsDecidedAsBefore(t *testing.T) {
	t.Parallel()
	attributes := []map[string]interface{}{
		{authorizationRepositoriesAttr: []string{"acme/api"}, authorizationProjectsAttr: []string{"P-1"}},
		{authorizationRepositoriesAttr: "*"},
		{authorizationRepositoriesAttr: []string{"acme/other"}},
		{},
	}
	for _, principal := range []storage.Principal{
		{OrgID: "org_1"},
		{OrgID: "org_1", RepositoryScopes: []string{"*"}},
		{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}},
	} {
		for _, relation := range []string{"IN_PROJECT", "DOCUMENTED_BY", "HAS_EPISODE"} {
			for index, attrs := range attributes {
				for _, requested := range []contextfabric.RequestedScope{{}, {ProjectIDs: []string{"P-1"}}} {
					if got, want := AuthorizedEdge(principal, requested, relation, attrs), AuthorizedAttributes(principal, requested, attrs); got != want {
						t.Errorf("%v %s edge %d %v: AuthorizedEdge = %v, AuthorizedAttributes = %v", principal.RepositoryScopes, relation, index, requested.ProjectIDs, got, want)
					}
				}
			}
		}
	}
}

// A kindless attribute map is a synthetic repository or team decision; a
// project-only caller is denied it, whatever it holds (repositories and teams
// are denied in c3).
func TestAProjectOnlyCallerIsDeniedAKindlessDecision(t *testing.T) {
	t.Parallel()
	principal := projectOnlyPrincipal()
	for name, attributes := range map[string]map[string]interface{}{
		"a repository":         {authorizationRepositoriesAttr: []string{"acme/api"}},
		"the wildcard":         {authorizationRepositoriesAttr: []string{"*"}},
		"the star string":      {authorizationRepositoriesAttr: "*"},
		"a stated membership":  {AuthorizationProjectIdentitiesAttr: []string{"project.v2:github:P-1"}},
		"no attributes at all": {},
	} {
		if AuthorizedAttributes(principal, contextfabric.RequestedScope{}, attributes) {
			t.Errorf("%s: a kindless map admitted a project-only caller", name)
		}
	}
}

// E2 in the observation traversal: a project-only caller never follows an
// attribution edge, even to a granted project with an open edge.
func TestAProjectOnlyCallerFollowsNoAttributionEdge(t *testing.T) {
	t.Parallel()
	principal := projectOnlyPrincipal()
	document := observationNode("node-document", "document_1234", "Ask Dev readiness review", 0.95)
	subject := candidateNode(contextfabric.SubjectProject, "project.v2:github:P-1", "Ask Dev", 0, "*")
	getEdges := func(context.Context, string) ([]CandidateEdge, error) {
		return []CandidateEdge{{
			UUID: "edge-documented-by", Name: "DOCUMENTED_BY", Fact: "documented",
			SourceNodeUUID: subject.UUID, TargetNodeUUID: document.UUID,
			Attributes: map[string]interface{}{authorizationProjectsAttr: "*"},
		}}, nil
	}
	getVerifiedNode := func(context.Context, string) (CandidateNode, bool) { return subject, true }
	if _, outcome := TraverseObservationToSubject(context.Background(), principal, contextfabric.RequestedScope{}, "readiness review", document, noInternalSubjects, true, getEdges, getVerifiedNode); outcome == ObservationParentFound {
		t.Fatal("a project-only caller followed a DOCUMENTED_BY edge")
	}
}
