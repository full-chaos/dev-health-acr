package devhealthsource

import (
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7080 cause fix: a row whose repository slug did not resolve (a LEFT
// JOIN to repos that found no row) gets a list that admits no repository,
// never an empty list -- which graphrank.UniqueSorted drops and falkorgraph's
// authorizationValue then writes as the "*" wildcard.
func TestChaos7080UnresolvedRepositorySlugNeverBecomesAWildcard(t *testing.T) {
	for _, slug := range []string{"", "  ", "\t"} {
		scope := repoAuthorization(slug)
		if !slices.Equal(scope.RepositorySlugs, []string{unresolvedRepositorySentinel}) {
			t.Fatalf("repoAuthorization(%q) = %v, want the unresolved sentinel", slug, scope.RepositorySlugs)
		}
		if len(graphrank.UniqueSorted(scope.RepositorySlugs)) == 0 {
			t.Fatalf("repoAuthorization(%q) collapses to an empty list, which projection writes as \"*\"", slug)
		}
		attrs := map[string]interface{}{"authorization_repositories": graphrank.UniqueSorted(scope.RepositorySlugs)}
		if graphrank.AuthorizedAttributes(storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/a"}}, contextfabric.RequestedScope{}, attrs) {
			t.Fatalf("repoAuthorization(%q) admits a repository-restricted caller", slug)
		}
		if !graphrank.AuthorizedAttributes(storage.Principal{OrgID: "org-1"}, contextfabric.RequestedScope{}, attrs) {
			t.Fatalf("repoAuthorization(%q) refuses an unrestricted caller (unchanged behaviour broken)", slug)
		}
	}
	if got := repoAuthorization("acme/a").RepositorySlugs; !slices.Equal(got, []string{"acme/a"}) {
		t.Fatalf("a resolved slug changed: %v", got)
	}
}
