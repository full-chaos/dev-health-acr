package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestRequestedRepositoryWildcardRequiresPresentRepository(t *testing.T) {
	const noRepo = "acr-context-fabric:no-repository"
	const orphan = "acr-context-fabric:orphaned-repository"
	const unresolved = "acr-context-fabric:unresolved-repository"
	principals := map[string]storage.Principal{
		"org_wide":     {OrgID: "o1"},
		"star_granted": {OrgID: "o1", RepositoryScopes: []string{"*"}},
		"scoped":       {OrgID: "o1", RepositoryScopes: []string{"acme/granted"}},
	}
	nodes := map[string]string{
		"no_repository": noRepo, "orphan": orphan, "unresolved": unresolved,
		"granted": "acme/granted", "other": "acme/other",
	}
	requests := map[string][]string{
		"absent": nil, "exact": {"acme/granted"}, "owner_wildcard": {"acme/*"}, "star": {"*"},
	}
	for pn, p := range principals {
		for nn, slug := range nodes {
			for rn, req := range requests {
				attrs := map[string]interface{}{"authorization_repositories": []string{slug}}
				got := AuthorizedAttributes(p, contextfabric.RequestedScope{RepositorySlugs: req}, attrs)
				sentinel := nn == "no_repository" || nn == "orphan" || nn == "unresolved"
				want := true
				if sentinel && rn != "absent" {
					want = false // a requested repository selector needs the repository PRESENT
				}
				if pn == "scoped" && nn != "granted" {
					want = false
				}
				if rn == "exact" && nn != "granted" {
					want = false
				}
				if rn == "owner_wildcard" && (nn == "other" || sentinel) {
					want = nn == "other" && pn != "scoped"
				}
				if got != want {
					t.Errorf("principal=%s node=%s requested=%s got=%v want=%v", pn, nn, rn, got, want)
				}
			}
		}
	}
}
