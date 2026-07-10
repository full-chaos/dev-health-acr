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
