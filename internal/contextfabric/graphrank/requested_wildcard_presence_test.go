package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestRequestedRepositoryWildcardRequiresPresentRepository(t *testing.T) {
	const noRepo = contractsv1.ContextFabricNoRepositorySentinel
	const orphan = contractsv1.ContextFabricOrphanedRepositorySentinel
	const unresolved = contractsv1.ContextFabricUnresolvedRepositorySentinel
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

func TestRequestedRepositoryWildcardAdmitsAMixedListAndRefusesEverySentinelKind(t *testing.T) {
	requested := contextfabric.RequestedScope{RepositorySlugs: []string{"*"}}
	principal := storage.Principal{OrgID: "o1"}
	cases := []struct {
		name string
		list []string
		want bool
	}{
		{"real slug", []string{"acme/granted"}, true},
		{"sentinel and real slug", []string{contractsv1.ContextFabricNoRepositorySentinel, "acme/granted"}, true},
		{"no project ownership", []string{contractsv1.ContextFabricNoProjectOwnershipSentinel}, false},
		{"no team ownership", []string{contractsv1.ContextFabricNoTeamOwnershipSentinel}, false},
		{"team ownership over bound", []string{contractsv1.ContextFabricTeamOwnershipOverBoundSentinel}, false},
		{"empty list", []string{}, false},
	}
	for _, c := range cases {
		got := AuthorizedAttributes(principal, requested, map[string]interface{}{"authorization_repositories": c.list})
		if got != c.want {
			t.Errorf("%s: got=%v want=%v", c.name, got, c.want)
		}
	}
}

func TestRequestedRepositoryWildcardKeepsTheUnrestrictedMarkerAndDeniesAMissingList(t *testing.T) {
	requested := contextfabric.RequestedScope{RepositorySlugs: []string{"*"}}
	principal := storage.Principal{OrgID: "o1"}
	if !AuthorizedAttributes(principal, requested, map[string]interface{}{"authorization_repositories": "*"}) {
		t.Error("unrestricted marker node must stay admitted by the all-repositories selector")
	}
	if AuthorizedAttributes(principal, requested, map[string]interface{}{}) {
		t.Error("a node with no repository list must be denied")
	}
}

func TestRequestedRepositorySelectorNamingASentinelAdmitsNothing(t *testing.T) {
	principal := storage.Principal{OrgID: "o1"}
	for _, sentinel := range []string{
		contractsv1.ContextFabricNoRepositorySentinel,
		contractsv1.ContextFabricOrphanedRepositorySentinel,
		contractsv1.ContextFabricUnresolvedRepositorySentinel,
		contractsv1.ContextFabricNoProjectOwnershipSentinel,
		contractsv1.ContextFabricNoTeamOwnershipSentinel,
		contractsv1.ContextFabricTeamOwnershipOverBoundSentinel,
		contractsv1.ContextFabricReferencedEndpointStubSentinel,
	} {
		attrs := map[string]interface{}{"authorization_repositories": []string{sentinel}}
		if AuthorizedAttributes(principal, contextfabric.RequestedScope{RepositorySlugs: []string{sentinel}}, attrs) {
			t.Errorf("selector %q admitted the node that records it", sentinel)
		}
		mixed := map[string]interface{}{"authorization_repositories": []string{sentinel, "acme/granted"}}
		if !AuthorizedAttributes(principal, contextfabric.RequestedScope{RepositorySlugs: []string{"acme/granted"}}, mixed) {
			t.Errorf("a real repository next to %q must stay admitted", sentinel)
		}
	}
}
