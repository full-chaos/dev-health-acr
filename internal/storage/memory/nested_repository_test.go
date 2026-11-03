package memory

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestOwnerWildcardAdmitsNestedRepositoriesAtDepthOneTwoAndThree(t *testing.T) {
	scopes := []string{"owner/*"}
	for depth, slug := range map[int]string{1: "owner/x", 2: "Owner/g/x", 3: "owner/g/h/x"} {
		if !repositoryAllowed(scopes, slug) {
			t.Errorf("packet, depth %d: owner/* refused %q", depth, slug)
		}
		if !episodeRepositoryAllowed(scopes, slug) {
			t.Errorf("episode, depth %d: owner/* refused %q", depth, slug)
		}
		if !principalPacketRepositoryAllowed(storage.Principal{RepositoryScopes: scopes}, slug) || !principalEpisodeRepositoryAllowed(storage.Principal{RepositoryScopes: scopes}, slug) {
			t.Errorf("principal, depth %d: owner/* refused %q", depth, slug)
		}
	}
	for _, slug := range []string{"owner2/x", "owner2/g/x", "ownerx/g/h/x"} {
		if repositoryAllowed(scopes, slug) || episodeRepositoryAllowed(scopes, slug) {
			t.Errorf("owner/* admitted %q", slug)
		}
	}
	if repositoryAllowed(scopes, "owner") {
		t.Error("owner/* admitted the bare owner")
	}
}

func TestAnExactNestedScopeAdmitsOnlyItselfAndTheKeyGrammarHolds(t *testing.T) {
	if !repositoryAllowed([]string{"acme/grp/web"}, "ACME/grp/web") || !episodeRepositoryAllowed([]string{"acme/grp/web"}, "ACME/grp/web") {
		t.Fatal("exact nested scope refused itself")
	}
	if repositoryAllowed([]string{"acme/grp/web"}, "acme/grp/web2") || repositoryAllowed([]string{"acme/grp/web"}, "acme/web") {
		t.Fatal("exact nested scope admitted another repository")
	}
	for _, slug := range []string{strings.Repeat("g/", 20) + "x", "acme/../web", "acme//web", "acme/grp/", strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 54)} {
		if repositoryAllowed([]string{"*"}, slug) {
			t.Errorf("repositoryAllowed accepted %q", slug)
		}
	}
	if !repositoryAllowed([]string{"*"}, strings.Repeat("g/", 19)+"x") {
		t.Error("twenty segments refused")
	}
}
