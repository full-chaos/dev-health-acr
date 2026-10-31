package auth

import (
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r1 F1: a project-only principal is refused every repository,
// whatever its repository list holds: the project grant names no repository.
func TestAProjectOnlyPrincipalIsRefusedEveryRepository(t *testing.T) {
	t.Parallel()
	project := []string{"project.v2:github:P-1"}
	for _, scopes := range [][]string{{"*"}, nil, {}, {"acme/api"}, {"acme/*"}} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: project}
		if err := AuthorizeRepository(principal, "acme/api"); !errors.Is(err, ErrRepositoryForbidden) {
			t.Errorf("%q: AuthorizeRepository = %v, want ErrRepositoryForbidden", scopes, err)
		}
		if set := WorkItemGrantSelectorSet(principal); set.All || len(set.ExactSlugs) != 0 || len(set.Owners) != 0 {
			t.Errorf("%q: WorkItemGrantSelectorSet = %+v, want the zero (deny) set", scopes, set)
		}
	}
	for _, tc := range []struct {
		scopes []string
		want   error
	}{{[]string{"*"}, nil}, {[]string{"acme/api"}, nil}, {[]string{"acme/*"}, nil}, {[]string{"acme/other"}, ErrRepositoryForbidden}, {nil, ErrRepositoryForbidden}} {
		if err := AuthorizeRepository(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}, "acme/api"); !errors.Is(err, tc.want) && err != tc.want {
			t.Errorf("%q: AuthorizeRepository = %v, want %v as before", tc.scopes, err, tc.want)
		}
	}
	if set := WorkItemGrantSelectorSet(storage.Principal{OrgID: "org_1"}); !set.All {
		t.Errorf("an unrestricted principal's work-item grant = %+v, want All as before", set)
	}
}
