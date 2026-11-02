package auth

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12592 (lead ruling (a) on codex r1, #1941): a "*" consent grant is the
// whole organization whatever else it lists, so the kept grant is exactly "*".
// A grant that does not hold "*" is never widened.
func TestKeepUniversalGrantMakesTheGrantExactlyStar(t *testing.T) {
	t.Parallel()
	for _, grant := range [][]string{{"*"}, {"*", "acme/api"}, {"acme/api", " * "}} {
		ctx, err := KeepUniversalGrant(context.WithValue(context.Background(), principalKey{}, storage.Principal{OrgID: "org_1", RepositoryScopes: grant}))
		if err != nil {
			t.Fatalf("%q: %v", grant, err)
		}
		kept, _ := PrincipalFromContext(ctx)
		if !slices.Equal(kept.RepositoryScopes, []string{"*"}) || ClassifyGrant(kept) != GrantUniversal {
			t.Fatalf("%q: kept %q (class %q), want exactly [\"*\"], universal", grant, kept.RepositoryScopes, ClassifyGrant(kept))
		}
	}
	for name, principal := range map[string]storage.Principal{
		"a list of repositories": {OrgID: "org_1", RepositoryScopes: []string{"acme/api", "acme/web"}},
		"an owner wildcard":      {OrgID: "org_1", RepositoryScopes: []string{"acme/*"}},
		"no grant list":          {OrgID: "org_1"},
		"a project grant":        {OrgID: "org_1", RepositoryScopes: []string{"*"}, ProjectScopes: []string{"project.v2:linear:P-1"}},
	} {
		in := context.WithValue(context.Background(), principalKey{}, principal)
		out, err := KeepUniversalGrant(in)
		if !errors.Is(err, ErrGrantNotUniversal) || out != in {
			t.Fatalf("%s: err %v (ctx unchanged %t), want ErrGrantNotUniversal and ctx unchanged", name, err, out == in)
		}
	}
}
