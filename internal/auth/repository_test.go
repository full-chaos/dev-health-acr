package auth

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestNormalizeRepositoryScopes(t *testing.T) {
	actual, err := NormalizeRepositoryScopes([]string{"Full-Chaos/Dev-Health-Ops", "full-chaos/*", "full-chaos/dev-health-ops"})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"full-chaos/*", "full-chaos/dev-health-ops"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("unexpected scopes: %#v", actual)
	}
}

func TestRepositoryAuthorization(t *testing.T) {
	principal := storage.Principal{RepositoryScopes: []string{"full-chaos/dev-health-acr", "openai/*"}}
	for _, allowed := range []string{"FULL-CHAOS/dev-health-acr", "openai/codex"} {
		if err := AuthorizeRepository(principal, allowed); err != nil {
			t.Fatalf("expected %q to be allowed: %v", allowed, err)
		}
	}
	for _, denied := range []string{"full-chaos/dev-health-web", "openai", "../secret/repo", "other/repo"} {
		if err := AuthorizeRepository(principal, denied); err == nil {
			t.Fatalf("expected %q to be denied", denied)
		}
	}
}

func TestNormalizeRepositoryScopesRejectsEmptyAndUnsafe(t *testing.T) {
	for _, scopes := range [][]string{nil, {}, {"owner"}, {"../owner/repo"}, {"owner/**"}, {"owner/repo/extra"}} {
		if _, err := NormalizeRepositoryScopes(scopes); err == nil {
			t.Fatalf("expected scopes %#v to fail", scopes)
		}
	}
}

// CHAOS-11996: GitHub accepts a leading '.' in a repository name (.github is the
// standard organization configuration repository). Every grammar accepts it,
// and the path-traversal names stay refused.
func TestRepositoryScopesAcceptADotLeadingRepositoryName(t *testing.T) {
	actual, err := NormalizeRepositoryScopes([]string{"Full-Chaos/.GitHub", "full-chaos/ask-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, []string{"full-chaos/.github", "full-chaos/ask-dev"}) {
		t.Fatalf("scopes = %#v", actual)
	}
	principal := storage.Principal{RepositoryScopes: []string{"full-chaos/.github"}}
	if err := AuthorizeRepository(principal, "FULL-CHAOS/.github"); err != nil {
		t.Fatalf("exact dot-leading repository denied: %v", err)
	}
	owner := storage.Principal{RepositoryScopes: []string{"full-chaos/*"}}
	if err := AuthorizeRepository(owner, "full-chaos/.github"); err != nil {
		t.Fatalf("owner wildcard denied a dot-leading repository: %v", err)
	}
	for _, refused := range []string{"full-chaos/.", "full-chaos/..", ".full-chaos/x", "full-chaos/.github/x", "full-chaos/.a."} {
		if _, err := NormalizeRepositorySlug(refused); err == nil {
			t.Errorf("NormalizeRepositorySlug(%q) accepted", refused)
		}
		if _, err := NormalizeRepositoryScopes([]string{refused}); err == nil {
			t.Errorf("NormalizeRepositoryScopes(%q) accepted", refused)
		}
	}
}
