package contextfabric

import (
	"testing"

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
	} {
		if got := classifyStoredResultPrincipalScope(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}); got != tc.want {
			t.Errorf("%s: classifyStoredResultPrincipalScope = %q, want %q", tc.name, got, tc.want)
		}
	}
}
