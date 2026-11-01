package sourcerow

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299: the direct data tools' repository decisions deny a project-only
// caller every repository and the "*" list, so every repository-anchored and
// team row is refused to it before any read, whatever its repository list.
func TestTheSourceRowRepositoryDecisionsDenyAProjectOnlyCaller(t *testing.T) {
	t.Parallel()
	for _, principal := range []storage.Principal{
		{OrgID: "org_1", ProjectScopes: []string{"project.v2:github:P-1"}},
		{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}, ProjectScopes: []string{"project.v2:github:P-1"}},
		{OrgID: "org_1", RepositoryScopes: []string{"*"}, ProjectScopes: []string{"project.v2:github:P-1"}},
	} {
		if admits(principal, "acme/api") {
			t.Errorf("%v: a repository was admitted to a project-only caller", principal.RepositoryScopes)
		}
		if admitsWildcard(principal) {
			t.Errorf("%v: the \"*\" list was admitted to a project-only caller", principal.RepositoryScopes)
		}
	}
	if !admits(storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}}, "acme/api") || !admitsWildcard(storage.Principal{OrgID: "org_1"}) {
		t.Error("a repository-restricted or unrestricted caller is decided differently than before")
	}
}
