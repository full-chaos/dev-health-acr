package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestChaos5405_TheAuthorizationKeyDecidesTheComparisonNotTheValueShape pins
// codex r2 F1 at the PRODUCTION entry point rather than on ScopeMatch alone.
//
// The defect was never in the comparison itself; it was in WHERE the kind came
// from. The first version inferred it from the value's shape, so a project or
// team id that happens to parse as "owner/repo" was folded case-insensitively
// against the authorization list:
//
//	project id "acme/team" vs authorization_projects ["ACME/TEAM"] -> authorized
//
// Reproduced in both arms before the fix. The kind now comes from the
// attribute KEY (scopeValueKindForAttr), which every call site already holds,
// so this test drives AuthorizedAttributes -- the function that chooses the
// key -- and not the matcher underneath it. A pin on ScopeMatch alone would
// still pass if the key mapping were wrong.
//
// BOTH DIRECTIONS, in one test on purpose. The denial half alone is satisfied
// by a matcher that compares everything exactly, which would silently undo the
// name half and the cross-layer agreement with the pushed-down SQL predicate
// that depends on it. The admission half alone is satisfied by the defect.
func TestChaos5405_TheAuthorizationKeyDecidesTheComparisonNotTheValueShape(t *testing.T) {
	t.Parallel()

	principal := storage.Principal{OrgID: "org-1"}

	t.Run("ids under authorization_projects and authorization_teams stay case-sensitive", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name      string
			key       string
			entry     string
			requested contextfabric.RequestedScope
		}{
			{
				name:      "project id shaped like a repository slug",
				key:       authorizationProjectsAttr,
				entry:     "ACME/TEAM",
				requested: contextfabric.RequestedScope{ProjectIDs: []string{"acme/team"}},
			},
			{
				name:      "team id shaped like a repository slug",
				key:       authorizationTeamsAttr,
				entry:     "ACME/PLATFORM",
				requested: contextfabric.RequestedScope{TeamIDs: []string{"acme/platform"}},
			},
			{
				name:      "project id that does not parse as a slug",
				key:       authorizationProjectsAttr,
				entry:     "PROJ-1",
				requested: contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"}},
			},
		} {
			attributes := map[string]interface{}{tc.key: []string{tc.entry}}
			if AuthorizedAttributes(principal, tc.requested, attributes) {
				t.Errorf("%s: %s=[%q] admitted a case-differing id -- ids are case-sensitive by ruling, whatever they are shaped like",
					tc.name, tc.key, tc.entry)
			}
			// THE CONTROL: the same id, exact, still admits. Without this
			// the denial above is also satisfied by a matcher that admits
			// nothing at all under this key.
			exact := contextfabric.RequestedScope{}
			switch tc.key {
			case authorizationProjectsAttr:
				exact.ProjectIDs = []string{tc.entry}
			case authorizationTeamsAttr:
				exact.TeamIDs = []string{tc.entry}
			}
			if !AuthorizedAttributes(principal, exact, attributes) {
				t.Errorf("%s: the exact id did not admit -- the control is broken, not the rule", tc.name)
			}
		}
	})

	t.Run("repository names under authorization_repositories still match case-insensitively", func(t *testing.T) {
		t.Parallel()
		attributes := map[string]interface{}{
			authorizationRepositoriesAttr: []string{"example-org/widget-service"},
		}
		if !AuthorizedAttributes(principal,
			contextfabric.RequestedScope{RepositorySlugs: []string{"EXAMPLE-ORG/WIDGET-SERVICE"}},
			attributes) {
			t.Fatal("a case-differing repository SLUG was denied -- a repository is the same repository however it is cased, and the pushed-down work-item predicate agrees with this side")
		}
		// The principal-scope arm reads the same key and must agree with
		// the requested-scope arm; they are separate code paths in
		// AuthorizedAttributes and have diverged before.
		if !AuthorizedAttributes(
			storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"EXAMPLE-ORG/WIDGET-SERVICE"}},
			contextfabric.RequestedScope{}, attributes) {
			t.Fatal("the principal repository-scope arm denied a case-differing slug the requested-scope arm admits")
		}
	})

	// THE OWNER WILDCARD ARM, both directions. Ruled by team-lead 2026-09-10
	// after this fix first landed on the exact arm only: chris's rule binds
	// the WHOLE function, so an arm that still folds an identifier's owner is
	// a half-implementation of it, not a seam boundary.
	//
	// This is a LIVE AUTHORIZATION TIGHTENING, present on main before this
	// change: a wildcard project or team scope that differs from the stored
	// id only in case stops matching. Reproduced on the tip before the fix:
	//
	//	scope "ACME/*" authorized project id "acme/team"
	//	scope "ACME/*" authorized team id "acme/platform"
	t.Run("an owner wildcard over ids compares the owner byte for byte", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name      string
			key       string
			entry     string
			folding   contextfabric.RequestedScope
			exact     contextfabric.RequestedScope
			unrelated contextfabric.RequestedScope
		}{
			{
				name:      "project ids",
				key:       authorizationProjectsAttr,
				entry:     "acme/team",
				folding:   contextfabric.RequestedScope{ProjectIDs: []string{"ACME/*"}},
				exact:     contextfabric.RequestedScope{ProjectIDs: []string{"acme/*"}},
				unrelated: contextfabric.RequestedScope{ProjectIDs: []string{"acm/*"}},
			},
			{
				name:      "team ids",
				key:       authorizationTeamsAttr,
				entry:     "acme/platform",
				folding:   contextfabric.RequestedScope{TeamIDs: []string{"ACME/*"}},
				exact:     contextfabric.RequestedScope{TeamIDs: []string{"acme/*"}},
				unrelated: contextfabric.RequestedScope{TeamIDs: []string{"acm/*"}},
			},
		} {
			attributes := map[string]interface{}{tc.key: []string{tc.entry}}
			if AuthorizedAttributes(principal, tc.folding, attributes) {
				t.Errorf("%s: a case-differing owner wildcard authorized %q -- ids are case-sensitive in every arm, not only the exact one", tc.name, tc.entry)
			}
			// THE CONTROL: the byte-exact owner wildcard still authorizes, so
			// the denial above is about CASE and not about the prefix arm
			// having been switched off for identifiers.
			if !AuthorizedAttributes(principal, tc.exact, attributes) {
				t.Errorf("%s: the byte-exact owner wildcard did not authorize %q -- the control is broken, not the rule", tc.name, tc.entry)
			}
			// A PREFIX IS NOT A SUBSTRING. "acm" must not reach "acme/...",
			// which is what dropping the separator check would allow.
			if AuthorizedAttributes(principal, tc.unrelated, attributes) {
				t.Errorf("%s: owner prefix \"acm\" reached %q -- the owner segment must end at the separator", tc.name, tc.entry)
			}
		}
	})

	t.Run("an owner wildcard over repository names still folds case", func(t *testing.T) {
		t.Parallel()
		attributes := map[string]interface{}{
			authorizationRepositoriesAttr: []string{"acme/thing"},
		}
		if !AuthorizedAttributes(principal,
			contextfabric.RequestedScope{RepositorySlugs: []string{"ACME/*"}}, attributes) {
			t.Fatal("a case-differing repository owner wildcard was denied -- a repository owner is a NAME and the tightening above must not reach it")
		}
		// A malformed entry must still not satisfy the wildcard by prefix
		// alone; that rule predates the kind parameter and survives it.
		malformed := map[string]interface{}{
			authorizationRepositoriesAttr: []string{"acme/not/real"},
		}
		if AuthorizedAttributes(principal,
			contextfabric.RequestedScope{RepositorySlugs: []string{"acme/*"}}, malformed) {
			t.Fatal("a malformed repository entry satisfied an owner wildcard by prefix alone")
		}
	})

	// The GLOBAL wildcard is kind-independent by definition -- it authorizes
	// unconditionally whatever the list holds -- and the tightening above must
	// not have reached it.
	t.Run("the global wildcard is unchanged for every kind", func(t *testing.T) {
		t.Parallel()
		for key, requested := range map[string]contextfabric.RequestedScope{
			authorizationProjectsAttr:     {ProjectIDs: []string{"*"}},
			authorizationTeamsAttr:        {TeamIDs: []string{"*"}},
			authorizationRepositoriesAttr: {RepositorySlugs: []string{"*"}},
		} {
			attributes := map[string]interface{}{key: []string{"anything/at-all"}}
			if !AuthorizedAttributes(principal, requested, attributes) {
				t.Errorf("the global wildcard stopped authorizing under %s", key)
			}
		}
	})

	// The kind is chosen by key, so an UNKNOWN key must not fold. This is the
	// fail-closed half of scopeValueKindForAttr, and the reason
	// ScopeValueIdentifier is the zero value.
	t.Run("an unrecognised authorization key gets the case-sensitive comparison", func(t *testing.T) {
		t.Parallel()
		if scopeValueKindForAttr("authorization_something_new") != ScopeValueIdentifier {
			t.Fatal("an unknown authorization key defaulted to name matching -- a key added later must fail closed, not fold")
		}
	})
}
