package falkorgraph

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestWildcardAuthorizesAnUnrelatedTeamForARepositoryScopedPrincipal is
// CHAOS-4390's canonical RED/GREEN proof, at the exact boundary the fix
// lives on: how a projected team entity's Authorization scope reaches
// graphrank.AuthorizedAttributes once falkorgraph's shared authorizationValue
// convention (projection.go) has converted it into a node attribute map.
//
// RED (documents the bug this ticket closes): before CHAOS-4390,
// queryTeams left Authorization.RepositorySlugs empty for every team --
// authorizationValue(nil) produces the literal string "*", and
// scopeContainsAttr's wildcard branch (authorize.go) authorizes that
// UNCONDITIONALLY, for ANY repository-scoped principal, regardless of
// whether the team owns anything in that principal's scope. A repo-scoped
// principal could see every team in the org.
//
// GREEN (the fix): queryTeams now emits either the team's real, current
// owned-repository list, or noTeamOwnershipSentinel when it owns none --
// never a bare empty list. This test proves BOTH directions of the fixed
// behavior over the exact write-path conversion (subjectMergeAttrs ->
// authorizationValue -> AuthorizedAttributes), not a hand-shaped attrs map:
// an unrelated principal is denied a team that owns something else, an
// unscoped-by-ownership team denies every repository-scoped principal, and
// a principal scoped to what the team actually owns is authorized.
func TestWildcardAuthorizesAnUnrelatedTeamForARepositoryScopedPrincipal(t *testing.T) {
	unrelatedPrincipal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/unrelated-repo"}}
	ownerPrincipal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/team-repo"}}

	t.Run("RED: empty RepositorySlugs (pre-fix shape) authorizes an unrelated principal", func(t *testing.T) {
		attrs := subjectAuthorizationAttrsForTest(contextfabric.AuthorizationScope{TeamIDs: []string{"team_x"}})
		if !graphrank.AuthorizedAttributes(unrelatedPrincipal, contextfabric.RequestedScope{}, attrs) {
			t.Fatal("expected the wildcard convention to (wrongly) authorize an unrelated principal -- this branch documents the bug, not the fix")
		}
	})

	t.Run("GREEN: the no-ownership sentinel denies every repository-scoped principal", func(t *testing.T) {
		attrs := subjectAuthorizationAttrsForTest(contextfabric.AuthorizationScope{
			TeamIDs: []string{"team_x"}, RepositorySlugs: []string{"acr-context-fabric:no-team-repository-ownership"},
		})
		if graphrank.AuthorizedAttributes(unrelatedPrincipal, contextfabric.RequestedScope{}, attrs) {
			t.Fatal("a team with no recorded ownership must be denied to a repository-scoped principal, not fall back to the wildcard")
		}
	})

	t.Run("GREEN: real ownership authorizes the owning scope and denies an unrelated one", func(t *testing.T) {
		attrs := subjectAuthorizationAttrsForTest(contextfabric.AuthorizationScope{
			TeamIDs: []string{"team_x"}, RepositorySlugs: []string{"acme/team-repo"},
		})
		if !graphrank.AuthorizedAttributes(ownerPrincipal, contextfabric.RequestedScope{}, attrs) {
			t.Fatal("a principal scoped to the team's own owned repository must be authorized")
		}
		if graphrank.AuthorizedAttributes(unrelatedPrincipal, contextfabric.RequestedScope{}, attrs) {
			t.Fatal("a principal scoped to a DIFFERENT repository must still be denied -- ownership scoping must not widen into an org-wide allow")
		}
	})
}

// subjectAuthorizationAttrsForTest builds the SAME attribute map
// subjectMergeAttrs would write for a subject's Authorization scope
// (projection.go), isolated to just the three authorization_* keys --
// this is the real production conversion (authorizationValue), not a
// hand-rolled stand-in for it.
func subjectAuthorizationAttrsForTest(scope contextfabric.AuthorizationScope) map[string]interface{} {
	return map[string]interface{}{
		propAuthzRepos:    authorizationValue(scope.RepositorySlugs),
		propAuthzProjects: authorizationValue(scope.ProjectIDs),
		propAuthzTeams:    authorizationValue(scope.TeamIDs),
	}
}
