package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299: the project-only node rule. A project-only caller is admitted
// only to its own organization node, a granted project node and a work item
// whose stated membership meets the grant; every other node, including a work
// item with no stated membership, is denied, and the repository grant is
// never consulted.
func projectOnlyPrincipal() storage.Principal {
	return storage.Principal{OrgID: "org_1", ProjectScopes: []string{"project.v2:github:P-1"}}
}

func projectOnlyNode(kind contextfabric.SubjectKind, canonicalID string, extra map[string]interface{}) map[string]interface{} {
	attributes := map[string]interface{}{
		"subject_kind": string(kind), "canonical_id": canonicalID, "label": canonicalID,
		authorizationRepositoriesAttr: "*", authorizationProjectsAttr: "*", authorizationTeamsAttr: "*",
	}
	for key, value := range extra {
		attributes[key] = value
	}
	return attributes
}

func TestAProjectOnlyCallerIsAdmittedToExactlyItsOrganizationItsProjectsAndTheirWorkItems(t *testing.T) {
	t.Parallel()
	principal := projectOnlyPrincipal()
	ownOrg := map[string]interface{}{authorizationProjectsAttr: []string{contractsv1.ContextFabricReservedOrganizationScopePrefix + "org_1"}}
	otherOrg := map[string]interface{}{authorizationProjectsAttr: []string{contractsv1.ContextFabricReservedOrganizationScopePrefix + "org_2"}}
	member := map[string]interface{}{AuthorizationProjectIdentitiesAttr: []string{"project.v2:github:P-1"}}
	otherProject := map[string]interface{}{AuthorizationProjectIdentitiesAttr: []string{"project.v2:github:P-2"}}
	noProject := map[string]interface{}{AuthorizationProjectIdentitiesAttr: []string{}}
	cases := []struct {
		name       string
		attributes map[string]interface{}
		want       bool
	}{
		{name: "own organization", attributes: projectOnlyNode(contextfabric.SubjectOrganization, "organization:org_1", ownOrg), want: true},
		{name: "another organization", attributes: projectOnlyNode(contextfabric.SubjectOrganization, "organization:org_2", otherOrg), want: false},
		{name: "a granted project", attributes: projectOnlyNode(contextfabric.SubjectProject, "project.v2:github:P-1", nil), want: true},
		{name: "a project outside the grant", attributes: projectOnlyNode(contextfabric.SubjectProject, "project.v2:github:P-2", nil), want: false},
		{name: "a project named by its raw id", attributes: projectOnlyNode(contextfabric.SubjectProject, "P-1", nil), want: false},
		{name: "a granted project id with spaces around it", attributes: projectOnlyNode(contextfabric.SubjectProject, " project.v2:github:P-1 ", nil), want: false},
		{name: "a member work item", attributes: projectOnlyNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-1", member), want: true},
		{name: "a work item of another project", attributes: projectOnlyNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-2", otherProject), want: false},
		{name: "a work item in no project", attributes: projectOnlyNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-3", noProject), want: false},
		{name: "a work item with no stated membership", attributes: projectOnlyNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-4", nil), want: false},
		{name: "a team", attributes: projectOnlyNode(contextfabric.SubjectTeam, "team:t", member), want: false},
		{name: "a pull request", attributes: projectOnlyNode(contextfabric.SubjectPullRequest, "pull_request:r:1", member), want: false},
		{name: "a repository", attributes: projectOnlyNode(contextfabric.SubjectRepository, "repository:r", nil), want: false},
		{name: "a deployment", attributes: projectOnlyNode(contextfabric.SubjectDeployment, "deployment:d", nil), want: false},
		{name: "an incident", attributes: projectOnlyNode(contextfabric.SubjectIncident, "incident:i", nil), want: false},
		{name: "an unknown kind", attributes: projectOnlyNode(contextfabric.SubjectKind("document"), "document:x", member), want: false},
		// A kindless map is never a node; only AuthorizedEdge decides an edge.
		{name: "a kindless map", attributes: map[string]interface{}{authorizationRepositoriesAttr: "*", authorizationProjectsAttr: "*"}, want: false},
	}
	for _, tc := range cases {
		if got := AuthorizedAttributes(principal, contextfabric.RequestedScope{}, tc.attributes); got != tc.want {
			t.Errorf("%s: AuthorizedAttributes = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Ruling B: a project-only request that names projects is refused (no node,
// no edge), never answered over the whole grant; a requested repository
// narrowing still narrows.
func TestAProjectOnlyRequestNamingProjectsAdmitsNothing(t *testing.T) {
	t.Parallel()
	principal := projectOnlyPrincipal()
	member := projectOnlyNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-1", map[string]interface{}{AuthorizationProjectIdentitiesAttr: []string{"project.v2:github:P-1"}})
	for name, attributes := range map[string]map[string]interface{}{
		"a member work item": member,
		"a granted project":  projectOnlyNode(contextfabric.SubjectProject, "project.v2:github:P-1", nil),
	} {
		if AuthorizedAttributes(principal, contextfabric.RequestedScope{ProjectIDs: []string{"P-1"}}, attributes) {
			t.Errorf("%s: admitted under a requested project narrowing; it must be refused", name)
		}
	}
	if AuthorizedEdge(principal, contextfabric.RequestedScope{ProjectIDs: []string{"P-1"}}, "IN_PROJECT", map[string]interface{}{authorizationProjectsAttr: "*"}) {
		t.Error("an edge was admitted under a requested project narrowing; it must be refused")
	}
	member[authorizationRepositoriesAttr] = []string{"acme/api"}
	if AuthorizedAttributes(principal, contextfabric.RequestedScope{RepositorySlugs: []string{"acme/other"}}, member) {
		t.Error("a requested repository narrowing that excludes the node did not narrow")
	}
	if !AuthorizedAttributes(principal, contextfabric.RequestedScope{RepositorySlugs: []string{"acme/api"}}, member) {
		t.Error("a requested repository narrowing that includes the node refused it")
	}
	member[authorizationTeamsAttr] = []string{"team:a"}
	if AuthorizedAttributes(principal, contextfabric.RequestedScope{TeamIDs: []string{"team:b"}}, member) {
		t.Error("a requested team narrowing that excludes the node did not narrow")
	}
	if !AuthorizedAttributes(principal, contextfabric.RequestedScope{TeamIDs: []string{"team:a"}}, member) {
		t.Error("a requested team narrowing that includes the node refused it")
	}
}

// The project grant decides even when the principal also carries a repository
// list: a project-only caller never takes the repository-grant path.
func TestAProjectOnlyCallerNeverTakesTheRepositoryGrantPath(t *testing.T) {
	t.Parallel()
	principal := projectOnlyPrincipal()
	principal.RepositoryScopes = []string{"acme/api"}
	repository := projectOnlyNode(contextfabric.SubjectRepository, "repository:acme/api", map[string]interface{}{authorizationRepositoriesAttr: []string{"acme/api"}})
	if AuthorizedAttributes(principal, contextfabric.RequestedScope{}, repository) {
		t.Error("a project-only caller was admitted to a repository through its repository list")
	}
}

// Every other class decides as before: the project-only branch is taken only
// for a non-empty project grant.
func TestOtherGrantClassesDecideAsBefore(t *testing.T) {
	t.Parallel()
	repoNode := projectOnlyNode(contextfabric.SubjectRepository, "repository:acme/api", map[string]interface{}{authorizationRepositoriesAttr: []string{"acme/api"}})
	workItem := projectOnlyNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-1", map[string]interface{}{authorizationRepositoriesAttr: []string{"acme/api"}})
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		node      map[string]interface{}
		want      bool
	}{
		{name: "unrestricted, repository", principal: storage.Principal{OrgID: "org_1"}, node: repoNode, want: true},
		{name: "universal, repository", principal: storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, node: repoNode, want: true},
		{name: "restricted to it", principal: storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}}, node: workItem, want: true},
		{name: "restricted elsewhere", principal: storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/other"}}, node: workItem, want: false},
		{name: "empty project grant, unrestricted", principal: storage.Principal{OrgID: "org_1", ProjectScopes: []string{}}, node: repoNode, want: true},
	} {
		if got := AuthorizedAttributes(tc.principal, contextfabric.RequestedScope{}, tc.node); got != tc.want {
			t.Errorf("%s: AuthorizedAttributes = %v, want %v", tc.name, got, tc.want)
		}
	}
}
