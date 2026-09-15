package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemAuthorizationDigestCanonicalizesOnlyRepositoryInputOrder(t *testing.T) {
	t.Parallel()
	base := storage.Principal{
		Subject:             "principal-a",
		OrgID:               "org-a",
		RepositoryScopes:    []string{" ACME/Tools ", "Acme/*", "acme/tools"},
		Permissions:         []string{"context:read"},
		ProductEntitlements: []string{"acr"},
	}
	baseDigest := mustWorkItemAuthorizationDigest(t, base, []string{" Other/Repo ", "other/repo", "ACME/*"})

	tests := []struct {
		name      string
		principal storage.Principal
		requested []string
		same      bool
	}{
		{
			name:      "grant and request order is equivalent",
			principal: storage.Principal{RepositoryScopes: []string{"Acme/*", "acme/tools", " ACME/Tools "}},
			requested: []string{"ACME/*", "other/repo", " Other/Repo "},
			same:      true,
		},
		{
			name:      "case change differs even when authorization is equivalent",
			principal: storage.Principal{RepositoryScopes: []string{"acme/*", "acme/tools", " acme/tools "}},
			requested: []string{"ACME/*", "other/repo", " Other/Repo "},
			same:      false,
		},
		{
			name:      "request spelling change differs even when authorization is equivalent",
			principal: base,
			requested: []string{" Other/Repo ", "other/repo ", "ACME/*"},
			same:      false,
		},
		{
			name:      "organization and unrelated principal fields are excluded",
			principal: storage.Principal{Subject: "other", OrgID: "org-other", RepositoryScopes: base.RepositoryScopes, Permissions: []string{"admin"}, ProductEntitlements: []string{"other"}},
			requested: []string{" Other/Repo ", "other/repo", "ACME/*"},
			same:      true,
		},
		{
			name:      "changed grant is different",
			principal: storage.Principal{RepositoryScopes: []string{" ACME/Tools ", "Acme/*", "acme/other"}},
			requested: []string{" Other/Repo ", "other/repo", "ACME/*"},
			same:      false,
		},
		{
			name:      "changed requested scope is different",
			principal: base,
			requested: []string{" Other/Repo ", "other/repo", "ACME/tools"},
			same:      false,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := WorkItemAuthorizationDigest(tc.principal, tc.requested)
			if err != nil {
				t.Fatalf("WorkItemAuthorizationDigest() error = %v", err)
			}
			if (got == baseDigest) != tc.same {
				t.Fatalf("digest equality = %v, want %v (got %q, base %q)", got == baseDigest, tc.same, got, baseDigest)
			}
		})
	}
	emptyGrantDigest := mustWorkItemAuthorizationDigest(t, storage.Principal{}, []string{"other/repo"})
	wildcardGrantDigest := mustWorkItemAuthorizationDigest(t, storage.Principal{RepositoryScopes: []string{"*"}}, []string{"other/repo"})
	if emptyGrantDigest == wildcardGrantDigest {
		t.Fatalf("empty grant digest %q equals explicit wildcard digest %q; raw grant inputs must remain distinguishable", emptyGrantDigest, wildcardGrantDigest)
	}

	nilRequested := mustWorkItemAuthorizationDigest(t, storage.Principal{RepositoryScopes: []string{"acme/tools"}}, nil)
	emptyRequested := mustWorkItemAuthorizationDigest(t, storage.Principal{RepositoryScopes: []string{"acme/tools"}}, []string{})
	if nilRequested != emptyRequested {
		t.Fatalf("nil requested scope digest %q differs from empty requested scope digest %q", nilRequested, emptyRequested)
	}

	permissionChanged := base
	permissionChanged.Permissions = []string{"context:write"}
	if got := mustWorkItemAuthorizationDigest(t, permissionChanged, []string{" Other/Repo ", "other/repo", "ACME/*"}); got != baseDigest {
		t.Fatalf("permission change changed digest to %q, want %q", got, baseDigest)
	}
	orgChanged := base
	orgChanged.OrgID = "org-other"
	if got := mustWorkItemAuthorizationDigest(t, orgChanged, []string{" Other/Repo ", "other/repo", "ACME/*"}); got != baseDigest {
		t.Fatalf("organization change changed digest to %q, want %q", got, baseDigest)
	}
}

func TestWorkItemAuthorizationDigestHasStableCanonicalBytes(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{RepositoryScopes: []string{" BETA/Repo ", "alpha/*", "beta/repo"}}
	got := mustWorkItemAuthorizationDigest(t, principal, []string{" Zeta/Repo ", "ALPHA/Repo"})
	// This pins the versioned JSON projection as well as the hash. The exact
	// bytes are:
	// {"version":"work-item-authorization.v1","principal_repository_grants":[" BETA/Repo ","alpha/*","beta/repo"],"requested_repository_scope":[" Zeta/Repo ","ALPHA/Repo"]}
	// It catches a field accidentally joining the authorization identity or a
	// change from sorted raw selectors to caller order.
	const want = "170fb315615e79f9d0d2a6f8116233797d9405d55a5c83b908a6f0b8f5cf9f6c"
	if got != want {
		t.Fatalf("digest = %q, want the canonical digest %q", got, want)
	}
}

func TestWorkItemAuthorizationDigestPreservesMalformedRawSelectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		principal storage.Principal
		requested []string
	}{
		{name: "malformed principal scope", principal: storage.Principal{RepositoryScopes: []string{"not-a-scope"}}},
		{name: "malformed requested scope", principal: storage.Principal{RepositoryScopes: []string{"acme/tools"}}, requested: []string{"not-a-scope"}},
		{name: "empty owner", principal: storage.Principal{RepositoryScopes: []string{"acme/"}}},
		{name: "extra path segment", principal: storage.Principal{RepositoryScopes: []string{"acme/tools/extra"}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := WorkItemAuthorizationDigest(tc.principal, tc.requested)
			if err != nil {
				t.Fatalf("WorkItemAuthorizationDigest() error = %v, want raw selector encoding", err)
			}
			if got == "" {
				t.Fatal("digest is empty, want raw malformed selector encoded")
			}
		})
	}
}

func mustWorkItemAuthorizationDigest(t *testing.T, principal storage.Principal, requested []string) string {
	t.Helper()
	digest, err := WorkItemAuthorizationDigest(principal, requested)
	if err != nil {
		t.Fatalf("WorkItemAuthorizationDigest() error = %v", err)
	}
	return digest
}
