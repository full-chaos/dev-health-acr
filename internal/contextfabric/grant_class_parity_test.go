package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12297: the stored-result principal scope reads a grant exactly as
// it did before the typed grant class existed (main 194d9dc4).
func TestTheStoredResultScopeReadsAGrantAsBefore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		scopes []string
		want   StoredResultPrincipalScope
	}{
		{name: "no grant list", scopes: nil, want: StoredResultScopeUnrestricted},
		{name: "empty grant list", scopes: []string{}, want: StoredResultScopeUnrestricted},
		{name: "star", scopes: []string{"*"}, want: StoredResultScopeUniversal},
		{name: "star with spaces", scopes: []string{" * "}, want: StoredResultScopeUniversal},
		{name: "one repository", scopes: []string{"a/b"}, want: StoredResultScopeRestricted},
		{name: "repository and star", scopes: []string{"a/b", "*"}, want: StoredResultScopeUniversal},
		{name: "star before a repository", scopes: []string{"*", "a/b"}, want: StoredResultScopeUniversal},
		{name: "duplicate repositories", scopes: []string{"a/b", "a/b"}, want: StoredResultScopeRestricted},
		{name: "duplicate stars", scopes: []string{"*", "*"}, want: StoredResultScopeUniversal},
		{name: "whitespace-only entry", scopes: []string{" "}, want: StoredResultScopeRestricted},
	} {
		if got := classifyStoredResultPrincipalScope(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}); got != tc.want {
			t.Errorf("%s: classifyStoredResultPrincipalScope = %q, want %q", tc.name, got, tc.want)
		}
	}
	// CHAOS-12299: a project grant is project_only, stored as restricted,
	// whatever the repository list holds.
	for _, scopes := range [][]string{nil, {}, {"*"}, {"a/b"}} {
		if got := classifyStoredResultPrincipalScope(storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}); got != StoredResultScopeRestricted {
			t.Errorf("project grant beside %q: classifyStoredResultPrincipalScope = %q, want restricted", scopes, got)
		}
	}
}

// project_only reads as restricted, so its results meet the grant-digest
// check, never the unrestricted bypass.
func TestEveryGrantClassMapsOntoTheStoredResultScope(t *testing.T) {
	t.Parallel()
	want := map[auth.GrantClass]StoredResultPrincipalScope{
		auth.GrantUnrestricted: StoredResultScopeUnrestricted,
		auth.GrantUniversal:    StoredResultScopeUniversal,
		auth.GrantRestricted:   StoredResultScopeRestricted,
		auth.GrantProjectOnly:  StoredResultScopeRestricted,
	}
	for _, grant := range auth.GrantClassVocabulary() {
		if got := storedResultScopeForGrant(grant); got != want[grant] {
			t.Errorf("storedResultScopeForGrant(%s) = %q, want %q", grant, got, want[grant])
		}
	}
	if storedResultScopeForGrant("bogus") != StoredResultScopeRestricted {
		t.Error("an unknown grant class must map to restricted")
	}
}
