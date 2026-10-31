package memory

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r1 F1: a project-only principal is refused every episode
// repository, whatever its repository list holds; other principals are
// decided by their repository list as before.
func TestAProjectOnlyPrincipalIsRefusedEveryEpisodeRepository(t *testing.T) {
	t.Parallel()
	for _, scopes := range [][]string{{"*"}, nil, {"acme/api"}} {
		if principalEpisodeRepositoryAllowed(storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}, "acme/api") {
			t.Errorf("%q: a project-only principal was allowed an episode repository", scopes)
		}
	}
	if !principalEpisodeRepositoryAllowed(storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, "acme/api") {
		t.Error("a universal principal was refused an episode repository")
	}
}
