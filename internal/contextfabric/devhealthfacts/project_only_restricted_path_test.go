package devhealthfacts

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r3: every devhealthfacts reader of "the caller sees every
// repository" takes the restricted path for a project-only caller, whatever
// its repository list: sourceHealthRestricted (organization source health,
// the organization theme mix and the unit listing) and orgWideFigureAllowed
// (team incident and blocker roll-ups). Other classes decide as before.
func TestAProjectOnlyCallerTakesTheRestrictedPathInFacts(t *testing.T) {
	t.Parallel()
	for _, scopes := range [][]string{nil, {}, {"*"}} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}
		if !sourceHealthRestricted(principal) {
			t.Errorf("%q: a project-only caller is not restricted for organization figures", scopes)
		}
		if orgWideFigureAllowed(principal, nil) {
			t.Errorf("%q: a project-only caller is allowed an organization-wide figure", scopes)
		}
	}
	for _, tc := range []struct {
		scopes              []string
		restricted, orgWide bool
	}{{nil, false, true}, {[]string{"*"}, false, true}, {[]string{"acme/api"}, true, false}, {[]string{" "}, true, false}} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}
		if got := sourceHealthRestricted(principal); got != tc.restricted {
			t.Errorf("%q: sourceHealthRestricted = %v, want %v as before", tc.scopes, got, tc.restricted)
		}
		if got := orgWideFigureAllowed(principal, nil); got != tc.orgWide {
			t.Errorf("%q: orgWideFigureAllowed = %v, want %v as before", tc.scopes, got, tc.orgWide)
		}
	}
}
