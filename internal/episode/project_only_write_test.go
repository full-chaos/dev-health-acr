package episode

import (
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r3 extra site: an episode write is refused to a project-only
// principal, whatever its repository list holds; a universal writer is not.
func TestAProjectOnlyPrincipalIsRefusedAnEpisodeWrite(t *testing.T) {
	t.Parallel()
	for _, scopes := range [][]string{{"*"}, {"acme/api"}} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}, Permissions: []string{auth.ScopeEpisodeWrite}, ProductEntitlements: []string{"agent_context_runtime"}}
		if err := authorizeWrite(principal); !errors.Is(err, auth.ErrRepositoryForbidden) {
			t.Errorf("%q: authorizeWrite = %v, want ErrRepositoryForbidden", scopes, err)
		}
	}
	if err := authorizeWrite(storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}, Permissions: []string{auth.ScopeEpisodeWrite}, ProductEntitlements: []string{"agent_context_runtime"}}); err != nil {
		t.Errorf("a universal writer was refused: %v", err)
	}
}
