package devhealthsource

import (
	"slices"
	"testing"
)

// CHAOS-12298 (c2): a source repository slug that carries the reserved
// repository-scope prefix is never projected as a repository: it is the
// unresolved sentinel, which denies every scoped principal, never a value a
// project-only principal's repository list also holds.
func TestRepoAuthorizationNeverProjectsAReservedSlug(t *testing.T) {
	for _, slug := range []string{"acr-reserved:project-only", "ACR-RESERVED:project-only", " acr-reserved:x "} {
		got := repoAuthorization(slug).RepositorySlugs
		if !slices.Equal(got, []string{unresolvedRepositorySentinel}) {
			t.Errorf("%q: projected %v, want the unresolved sentinel", slug, got)
		}
	}
	if got := repoAuthorization("acme/api").RepositorySlugs; !slices.Equal(got, []string{"acme/api"}) {
		t.Errorf("an ordinary slug projected %v", got)
	}
}
