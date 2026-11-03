package auth

import (
	"reflect"
	"strings"
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
	for _, scopes := range [][]string{nil, {}, {"owner"}, {"../owner/repo"}, {"owner/**"}, {"owner/grp/*"}, {"owner/*/repo"}} {
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

func TestNormalizeRepositorySlugAcceptsNestedGroupsAndLowercasesThem(t *testing.T) {
	for raw, want := range map[string]string{
		"ACME/Grp/Web":                 "acme/grp/web",
		" acme/grp/sub/web ":           "acme/grp/sub/web",
		"acme/grp/.GitHub":             "acme/grp/.github",
		strings.Repeat("g/", 19) + "x": strings.Repeat("g/", 19) + "x",
	} {
		got, err := NormalizeRepositorySlug(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeRepositorySlug(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestNormalizeRepositorySlugRefusesWhatTheKeyGrammarRefuses(t *testing.T) {
	long := strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 54)
	for name, raw := range map[string]string{
		"twenty-one segments": strings.Repeat("g/", 20) + "x",
		"256 bytes":           long,
		"one segment":         "acme",
		"trailing slash":      "acme/grp/",
		"empty segment":       "acme//web",
		"dot-dot segment":     "acme/../web",
		"dot segment":         "acme/./web",
		"leading dot group":   "acme/.grp/web",
		"wildcard segment":    "acme/*/web",
	} {
		if got, err := NormalizeRepositorySlug(raw); err == nil {
			t.Errorf("%s: NormalizeRepositorySlug(%q) = %q, want a refusal", name, raw, got)
		}
		if RepositoryAllowed([]string{"*"}, raw) {
			t.Errorf("%s: RepositoryAllowed accepted %q", name, raw)
		}
	}
}

// pm ruling (CHAOS-10708): owner/* matches at any depth below owner/, as a
// GitLab group grant reaches its subgroups. One cell per depth.
func TestOwnerWildcardMatchesAtDepthOneTwoAndThree(t *testing.T) {
	scopes := []string{"owner/*"}
	for depth, slug := range map[int]string{1: "owner/x", 2: "owner/g/x", 3: "owner/g/h/x"} {
		if !RepositoryAllowed(scopes, slug) {
			t.Errorf("depth %d: owner/* did not admit %q", depth, slug)
		}
		if err := AuthorizeRepository(storage.Principal{RepositoryScopes: scopes}, strings.ToUpper(slug)); err != nil {
			t.Errorf("depth %d: AuthorizeRepository(%q): %v", depth, slug, err)
		}
	}
	for _, slug := range []string{"owner2/x", "owner2/g/x", "ownerx/g/h/x", "owner", "other/owner/x", "owner-/g/x"} {
		if RepositoryAllowed(scopes, slug) {
			t.Errorf("owner/* admitted %q", slug)
		}
	}
}

func TestAnExactNestedScopeAdmitsOnlyItself(t *testing.T) {
	scopes := []string{"acme/grp/web"}
	if !RepositoryAllowed(scopes, "ACME/grp/web") {
		t.Fatal("exact nested scope refused itself")
	}
	for _, slug := range []string{"acme/grp/web2", "acme/grp", "acme/web", "acme/grp/web/x", "acme/other/web"} {
		if RepositoryAllowed(scopes, slug) {
			t.Errorf("exact nested scope admitted %q", slug)
		}
	}
}

func TestNormalizeRepositoryScopesAcceptsNestedRepositoriesAndRefusesNestedWildcards(t *testing.T) {
	got, err := NormalizeRepositoryScopes([]string{"Acme/Grp/Web", "acme/*", "acme/grp/sub/web"})
	if err != nil || !reflect.DeepEqual(got, []string{"acme/*", "acme/grp/sub/web", "acme/grp/web"}) {
		t.Fatalf("scopes = %#v, %v", got, err)
	}
	for _, refused := range []string{"acme/grp/*", strings.Repeat("g/", 20) + "x", "acme/grp/../x"} {
		if _, err := NormalizeRepositoryScopes([]string{refused}); err == nil {
			t.Errorf("NormalizeRepositoryScopes(%q) accepted", refused)
		}
	}
}

func TestAnOwnerScopeCoversNestedScopesOfThatOwnerOnly(t *testing.T) {
	for _, requested := range []string{"owner/x", "owner/g/x", "owner/g/h/x", "owner/*"} {
		if !repositoryScopeCovers("owner/*", requested) {
			t.Errorf("owner/* does not cover %q", requested)
		}
	}
	for _, requested := range []string{"owner2/x", "owner2/g/x", "*"} {
		if repositoryScopeCovers("owner/*", requested) {
			t.Errorf("owner/* covers %q", requested)
		}
	}
	if repositoryScopeCovers("owner/g/x", "owner/g/y") || repositoryScopeCovers("owner/g/x", "owner/g/x/y") {
		t.Error("an exact nested scope covers another repository")
	}
}
