package memory

import (
	"context"
	"errors"
	"testing"
	"time"

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

// CHAOS-12299 r3 extra sites: a project-only principal is refused every
// context-packet repository and every principal episode purge, whatever its
// repository list holds.
func TestAProjectOnlyPrincipalIsRefusedPacketsAndPurges(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}, ProjectScopes: []string{"project.v2:github:P-1"}}
	if principalPacketRepositoryAllowed(principal, "acme/api") {
		t.Error("a project-only principal was allowed a context-packet repository")
	}
	if !principalPacketRepositoryAllowed(storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, "acme/api") {
		t.Error("a universal principal was refused a context-packet repository")
	}
	if _, err := (&EpisodeStore{}).PurgeExpiredForPrincipal(context.Background(), principal, time.Now(), 10); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("PurgeExpiredForPrincipal = %v, want not found for a project-only principal", err)
	}
}
