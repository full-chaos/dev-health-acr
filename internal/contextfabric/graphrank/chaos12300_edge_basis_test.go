package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12300: AuthorizedEdgeBasis names each E1/E2 basis, and AuthorizedEdge
// is exactly "the basis is admitted" for every case, so the counted basis and
// the decision can never disagree.
func TestAuthorizedEdgeBasisNamesEachBasisAndMatchesTheDecision(t *testing.T) {
	projectOnly := storage.Principal{OrgID: "o", ProjectScopes: []string{"project.v2:linear:P-1"}}
	restricted := storage.Principal{OrgID: "o", RepositoryScopes: []string{"acme/a"}}
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"P-1"}}
	cases := []struct {
		name      string
		principal storage.Principal
		requested contextfabric.RequestedScope
		relation  string
		attrs     map[string]interface{}
		want      EdgeBasis
	}{
		{"absent project list defers to the ends", projectOnly, contextfabric.RequestedScope{}, "OWNED_BY_TEAM", map[string]interface{}{}, EdgeBasisAdmitted},
		{"wildcard project list defers to the ends", projectOnly, contextfabric.RequestedScope{}, "OWNED_BY_TEAM", map[string]interface{}{"authorization_projects": "*"}, EdgeBasisAdmitted},
		{"granted identity in the list", projectOnly, contextfabric.RequestedScope{}, "OWNED_BY_TEAM", map[string]interface{}{"authorization_projects": []string{"x", "project.v2:linear:P-1"}}, EdgeBasisAdmitted},
		{"list without a granted identity", projectOnly, contextfabric.RequestedScope{}, "OWNED_BY_TEAM", map[string]interface{}{"authorization_projects": []string{"project.v2:linear:P-2"}}, EdgeBasisProjectList},
		{"non-wildcard string list", projectOnly, contextfabric.RequestedScope{}, "OWNED_BY_TEAM", map[string]interface{}{"authorization_projects": "P-1"}, EdgeBasisProjectList},
		{"unreadable list shape", projectOnly, contextfabric.RequestedScope{}, "OWNED_BY_TEAM", map[string]interface{}{"authorization_projects": 7}, EdgeBasisProjectList},
		{"DOCUMENTED_BY", projectOnly, contextfabric.RequestedScope{}, "DOCUMENTED_BY", map[string]interface{}{}, EdgeBasisAttributionRelation},
		{"HAS_EPISODE", projectOnly, contextfabric.RequestedScope{}, "HAS_EPISODE", map[string]interface{}{}, EdgeBasisAttributionRelation},
		{"a project narrowing is refused before the relation", projectOnly, narrowed, "DOCUMENTED_BY", map[string]interface{}{}, EdgeBasisProjectNarrowing},
		{"restricted, refused attributes", restricted, contextfabric.RequestedScope{}, "OWNED_BY_TEAM", map[string]interface{}{"authorization_repositories": []string{"acme/b"}}, EdgeBasisAttributes},
		{"restricted, admitted attributes", restricted, contextfabric.RequestedScope{}, "OWNED_BY_TEAM", map[string]interface{}{"authorization_repositories": []string{"acme/a"}}, EdgeBasisAdmitted},
		{"restricted, an attribution relation is decided on its attributes", restricted, contextfabric.RequestedScope{}, "DOCUMENTED_BY", map[string]interface{}{"authorization_repositories": []string{"acme/a"}}, EdgeBasisAdmitted},
	}
	for _, tc := range cases {
		got := AuthorizedEdgeBasis(tc.principal, tc.requested, tc.relation, tc.attrs)
		if got != tc.want {
			t.Errorf("%s: basis %q, want %q", tc.name, got, tc.want)
		}
		if decided := AuthorizedEdge(tc.principal, tc.requested, tc.relation, tc.attrs); decided != (got == EdgeBasisAdmitted) {
			t.Errorf("%s: AuthorizedEdge = %v but basis %q", tc.name, decided, got)
		}
	}
	seen := map[EdgeBasis]bool{}
	for _, tc := range cases {
		seen[tc.want] = true
	}
	for _, basis := range EdgeBasisVocabulary() {
		if !seen[basis] {
			t.Errorf("no case covers basis %q", basis)
		}
	}
}
