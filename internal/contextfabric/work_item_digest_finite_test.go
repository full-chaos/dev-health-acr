package contextfabric

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemAuthorizationDigestPreservesRawSelectorFiniteDomain(t *testing.T) {
	t.Parallel()
	basePrincipal := storage.Principal{RepositoryScopes: []string{"Acme/*", "Acme/tools", " acme/tools "}}
	baseRequested := []string{"other/repo", " Other/Repo "}
	baseDigest := mustWorkItemAuthorizationDigest(t, basePrincipal, baseRequested)

	cases := []struct {
		name      string
		principal storage.Principal
		requested []string
		same      bool
	}{
		{
			name:      "grant permutation",
			principal: storage.Principal{RepositoryScopes: []string{" acme/tools ", "Acme/tools", "Acme/*"}},
			requested: baseRequested,
			same:      true,
		},
		{
			name:      "requested permutation",
			principal: basePrincipal,
			requested: []string{" Other/Repo ", "other/repo"},
			same:      true,
		},
		{
			name:      "duplicate grant is preserved",
			principal: storage.Principal{RepositoryScopes: []string{"Acme/*", "Acme/tools", " acme/tools ", "Acme/*"}},
			requested: baseRequested,
			same:      false,
		},
		{
			name:      "duplicate requested selector is preserved",
			principal: basePrincipal,
			requested: []string{"other/repo", " Other/Repo ", "other/repo"},
			same:      false,
		},
		{
			name:      "grant case is raw",
			principal: storage.Principal{RepositoryScopes: []string{"acme/*", "Acme/tools", " acme/tools "}},
			requested: baseRequested,
			same:      false,
		},
		{
			name:      "grant whitespace is raw",
			principal: storage.Principal{RepositoryScopes: []string{"Acme/*", "Acme/tools", "acme/tools"}},
			requested: baseRequested,
			same:      false,
		},
		{
			name:      "invalid grant is raw",
			principal: storage.Principal{RepositoryScopes: []string{"Acme/*", "Acme/tools", "not-a-selector"}},
			requested: baseRequested,
			same:      false,
		},
		{
			name:      "requested case is raw",
			principal: basePrincipal,
			requested: []string{"OTHER/REPO", " Other/Repo "},
			same:      false,
		},
		{
			name:      "requested whitespace is raw",
			principal: basePrincipal,
			requested: []string{"other/repo ", " Other/Repo "},
			same:      false,
		},
		{
			name:      "invalid requested selector is raw",
			principal: basePrincipal,
			requested: []string{"not-a-selector", " Other/Repo "},
			same:      false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := mustWorkItemAuthorizationDigest(t, tc.principal, tc.requested)
			if (got == baseDigest) != tc.same {
				t.Fatalf("digest equality = %v, want %v (got %q, base %q)", got == baseDigest, tc.same, got, baseDigest)
			}
		})
	}
}

func TestWorkItemAuthorizationDigestNormalizesOnlyNilAndEmptySlices(t *testing.T) {
	t.Parallel()

	nilRequested := mustWorkItemAuthorizationDigest(t, storage.Principal{RepositoryScopes: []string{"acme/tools"}}, nil)
	emptyRequested := mustWorkItemAuthorizationDigest(t, storage.Principal{RepositoryScopes: []string{"acme/tools"}}, []string{})
	if nilRequested != emptyRequested {
		t.Fatalf("nil requested scope digest %q differs from empty requested scope digest %q", nilRequested, emptyRequested)
	}

	nilGrants := mustWorkItemAuthorizationDigest(t, storage.Principal{}, []string{"acme/tools"})
	emptyGrants := mustWorkItemAuthorizationDigest(t, storage.Principal{RepositoryScopes: []string{}}, []string{"acme/tools"})
	if nilGrants != emptyGrants {
		t.Fatalf("nil grant digest %q differs from empty grant digest %q", nilGrants, emptyGrants)
	}
}

func TestWorkItemAuthorizationDigestEncodesNilInputsAsEmptyArrays(t *testing.T) {
	t.Parallel()
	got := mustWorkItemAuthorizationDigest(t, storage.Principal{}, nil)
	const want = "11137547c4f99b925019e766b90745c9f7064263807120dcb61a7e58c488742e"
	if got != want {
		t.Fatalf("digest = %q, want the empty-array canonical digest %q", got, want)
	}
}

func TestWorkItemAuthorizationDigestDoesNotMutateInputs(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{RepositoryScopes: []string{"z/repo", "a/repo", "m/repo"}}
	requested := []string{"z/request", "a/request", "m/request"}
	wantPrincipal := principal
	wantPrincipal.RepositoryScopes = append([]string{}, principal.RepositoryScopes...)
	wantRequested := append([]string{}, requested...)

	first := mustWorkItemAuthorizationDigest(t, principal, requested)
	if !reflect.DeepEqual(principal, wantPrincipal) {
		t.Fatalf("principal repository scopes mutated: got %#v, want %#v", principal.RepositoryScopes, wantPrincipal.RepositoryScopes)
	}
	if !reflect.DeepEqual(requested, wantRequested) {
		t.Fatalf("requested repository scope mutated: got %#v, want %#v", requested, wantRequested)
	}
	second := mustWorkItemAuthorizationDigest(t, principal, requested)
	if second != first {
		t.Fatalf("repeated digest = %q, want unchanged %q", second, first)
	}
}
