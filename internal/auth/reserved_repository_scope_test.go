package auth

import (
	"errors"
	"strings"
	"testing"
)

// CHAOS-12298 (c2): the reserved repository-scope prefix is refused where a
// repository scope enters acr through a credential, with its own reason. The
// slug grammar (validRepositoryScope) refuses it too; the explicit refusal
// keeps it refused if the grammar ever widens (for example to nested owner
// paths).
func TestNormalizeRepositoryScopesRefusesTheReservedPrefixByName(t *testing.T) {
	for _, scope := range []string{"acr-reserved:project-only", "ACR-RESERVED:project-only", " acr-reserved:x"} {
		_, err := NormalizeRepositoryScopes([]string{"acme/api", scope})
		if !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("%q: err = %v, want ErrInvalidCredential", scope, err)
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%q: err = %v, want the reserved-prefix reason", scope, err)
		}
	}
	if got, err := NormalizeRepositoryScopes([]string{"acme/api", "*"}); err != nil || len(got) != 2 {
		t.Fatalf("ordinary scopes: %v, %v", got, err)
	}
}

// RepositoryAllowed never reads a reserved scope as a grant. NOT pinned
// (equivalent): a reserved value can never equal a normalized slug, "*" or an
// owner wildcard, so the explicit skip cannot change the result today.
func TestRepositoryAllowedIgnoresAReservedScope(t *testing.T) {
	for _, slug := range []string{"acme/api", "acr-reserved/project-only"} {
		if RepositoryAllowed([]string{"acr-reserved:project-only"}, slug) {
			t.Errorf("a reserved scope allowed %q", slug)
		}
	}
	if !RepositoryAllowed([]string{"acr-reserved:project-only", "acme/api"}, "acme/api") {
		t.Error("a real scope beside a reserved one no longer allows its repository")
	}
}
