package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r3 (codex repro at the predicate): a project-only caller with an
// empty or "*" repository list is never "unscoped", so an organization-wide
// census count never takes the unscoped commit path for it.
// Every resolve-side reader of "the caller sees every repository" takes the
// restricted path for a project-only caller, whatever its repository list.
func TestAProjectOnlyCallerIsNeverUnscopedInResolve(t *testing.T) {
	t.Parallel()
	for _, scopes := range [][]string{nil, {}, {"*"}} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}
		if unscopedVisibilityFor(principal, contextfabric.InvestigationRequest{}) {
			t.Errorf("%q: a project-only caller has unscoped visibility", scopes)
		}
		narrowed := contextfabric.InvestigationRequest{RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/api"}}}
		if repositoryNarrowedByUnrestrictedPrincipal(principal, narrowed) {
			t.Errorf("%q: a project-only caller counts as an unrestricted principal narrowing by repository", scopes)
		}
	}
	for _, tc := range []struct {
		scopes []string
		want   bool
	}{{nil, true}, {[]string{"*"}, true}, {[]string{"acme/api"}, false}, {[]string{" * "}, false}} {
		if got := unscopedVisibilityFor(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}, contextfabric.InvestigationRequest{}); got != tc.want {
			t.Errorf("%q: unscopedVisibilityFor = %v, want %v as before", tc.scopes, got, tc.want)
		}
	}
}
