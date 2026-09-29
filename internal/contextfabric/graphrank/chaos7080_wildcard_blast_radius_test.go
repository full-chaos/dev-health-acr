package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7080 blast radius, as a fixture: for every subject kind, one node
// whose repository resolved, one carrying the "*" wildcard (a project, a
// pre-4390 team, or a pre-7080 node whose slug did not resolve), and one
// carrying the unresolved-repository sentinel (a post-7080 unresolved slug);
// plus a wildcard edge (a project<->team ownership edge). Three callers:
// repository-restricted, unrestricted (no list), universal ("*").
//
// The state this exists to reach: a restricted caller sees ONLY the resolved
// node (and its own organization); unrestricted and universal callers see
// exactly what they saw before -- everything. On the baseline the restricted
// row admits every wildcard node and edge (the red run in the PR body).
func TestChaos7080WildcardBlastRadiusByKindAndCaller(t *testing.T) {
	const org = "org-1"
	restricted := storage.Principal{OrgID: org, RepositoryScopes: []string{"acme/a"}}
	unrestricted := storage.Principal{OrgID: org}
	universal := storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}

	resolved := map[string]interface{}{"authorization_repositories": []string{"acme/a"}}
	wildcard := map[string]interface{}{"authorization_repositories": "*"}
	sentinel := map[string]interface{}{"authorization_repositories": []string{"acr-context-fabric:unresolved-repository"}}
	ownOrg := map[string]interface{}{"authorization_repositories": "*", "authorization_projects": []string{contractsv1.ContextFabricReservedOrganizationScopePrefix + org}}
	otherOrg := map[string]interface{}{"authorization_repositories": "*", "authorization_projects": []string{contractsv1.ContextFabricReservedOrganizationScopePrefix + "org-2"}}
	edge := map[string]interface{}{"authorization_repositories": "*", "authorization_projects": []string{"project:p"}, "authorization_teams": []string{"team:t"}}

	kinds := contractsv1.ContextFabricSubjectKindVocabulary()
	cells := 0
	for _, kind := range kinds {
		for _, tc := range []struct {
			name  string
			attrs map[string]interface{}
			want  [3]bool // restricted, unrestricted, universal
		}{
			{"resolved", resolved, [3]bool{true, true, true}},
			{"wildcard", wildcard, [3]bool{false, true, true}},
			{"unresolved_sentinel", sentinel, [3]bool{false, true, true}},
		} {
			for index, principal := range []storage.Principal{restricted, unrestricted, universal} {
				if got := AuthorizedAttributes(principal, contextfabric.RequestedScope{}, tc.attrs); got != tc.want[index] {
					t.Errorf("%s/%s caller %d: admitted=%v, want %v", kind, tc.name, index, got, tc.want[index])
				}
				cells++
			}
		}
	}
	if cells != len(kinds)*9 {
		t.Fatalf("ran %d cells, want %d", cells, len(kinds)*9)
	}
	// The one wildcard node a restricted caller keeps: its own organization.
	for _, tc := range []struct {
		name  string
		attrs map[string]interface{}
		want  [3]bool
	}{
		{"own_organization", ownOrg, [3]bool{true, true, true}},
		{"other_organization_scope", otherOrg, [3]bool{false, true, true}},
		{"project_team_ownership_edge", edge, [3]bool{false, true, true}},
	} {
		for index, principal := range []storage.Principal{restricted, unrestricted, universal} {
			if got := AuthorizedAttributes(principal, contextfabric.RequestedScope{}, tc.attrs); got != tc.want[index] {
				t.Errorf("%s caller %d: admitted=%v, want %v", tc.name, index, got, tc.want[index])
			}
		}
	}
	// An owner-wildcard grant ("acme/*") is restricted too.
	if AuthorizedAttributes(storage.Principal{OrgID: org, RepositoryScopes: []string{"acme/*"}}, contextfabric.RequestedScope{}, wildcard) {
		t.Error(`owner-wildcard grant admitted a "*" node`)
	}
	if !AuthorizedAttributes(storage.Principal{OrgID: org, RepositoryScopes: []string{"acme/*"}}, contextfabric.RequestedScope{}, resolved) {
		t.Error("owner-wildcard grant refused a node in its owner")
	}
	// A blank org id never matches the organization scope.
	if AuthorizedAttributes(storage.Principal{RepositoryScopes: []string{"acme/a"}}, contextfabric.RequestedScope{}, map[string]interface{}{"authorization_repositories": "*", "authorization_projects": []string{contractsv1.ContextFabricReservedOrganizationScopePrefix}}) {
		t.Error("blank organization matched the reserved scope prefix")
	}
}
