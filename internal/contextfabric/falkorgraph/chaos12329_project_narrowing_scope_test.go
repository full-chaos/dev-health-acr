package falkorgraph

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12329: a requested project narrowing is SCOPE, not authorization. An
// issue the caller's grants admit but whose stated membership is another
// project is out of the requested scope: not a member, and not counted in the
// walk's authorization count (Denied), so it gives no authorization-gap
// disclosure. What the grants hide is still denied.
func TestAProjectNarrowingExclusionIsNotADenial(t *testing.T) {
	const (
		other    = "work_item.v2:11111111-1111-1111-1111-111111111111:1"
		member   = "work_item.v2:11111111-1111-1111-1111-111111111111:2"
		weakLink = "work_item.v2:" + zeroRepoID + ":ENG-3"
		// legacy carries a real (legacy) project list and no stated
		// membership: the narrowing refuses it by that list, which is scope.
		legacy = "work_item.v2:11111111-1111-1111-1111-111111111111:4"
	)
	stated := map[string][]string{
		other:    {"project.v2:linear:other"},
		member:   {"project.v2:linear:requested"},
		weakLink: {"project.v2:linear:requested"},
	}
	s := newMemberSeed()
	s.issue(other, []string{memberAnchorSlug})
	s.issue(member, []string{memberAnchorSlug})
	// A repository-less issue linked only by a heuristic match: a restricted
	// caller's grants never admit it through that link.
	s.issue(weakLink, nil)
	s.issue(legacy, []string{memberAnchorSlug})
	s.pullRequest("pull_request:github:acme/svc:1", memberAnchorSlug)
	s.link(other, "pull_request:github:acme/svc:1", "native")
	s.link(member, "pull_request:github:acme/svc:1", "native")
	s.link(weakLink, "pull_request:github:acme/svc:1", "heuristic")
	s.link(legacy, "pull_request:github:acme/svc:1", "native")
	// The production node shape: "*" project and team lists, and the stated
	// membership beside them (CHAOS-12226).
	production := func(conn *fakeConn) {
		inner := conn.queryFunc
		conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			rows, err := inner(ctx, key, cypher, params, readOnly)
			if err == nil && params["linkRel"] != nil {
				for _, r := range rows {
					for _, k := range []string{"m", "b"} {
						if n, ok := r[k].(*node); ok {
							n.Properties[propAuthzTeams], n.Properties[propAuthzProjects] = "*", "*"
							if canonicalIDOf(n) == legacy {
								n.Properties[propAuthzProjects] = []string{"other"}
							}
							if ids, ok := stated[canonicalIDOf(n)]; ok {
								n.Properties[graphrank.AuthorizationProjectIdentitiesAttr] = ids
							}
						}
					}
				}
			}
			return rows, err
		}
	}
	// A restricted caller's rows decided per row (no grant clause in the
	// read), so a row the grants refuse is returned and counted denied.
	productionPerRow := func(conn *fakeConn) {
		production(conn)
		perRowOnly(conn)
	}
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"requested"},
		ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:requested"}, Complete: true}}
	for name, tc := range map[string]struct {
		principal    storage.Principal
		scope        contextfabric.RequestedScope
		wantMembers  []string
		wantDenied   int
		wantExcluded int
	}{
		// weakLink is denied for the restricted caller (a heuristic link
		// grants no authority); other is out of the requested project.
		"unrestricted, no narrowing": {storage.Principal{OrgID: "org-1"}, contextfabric.RequestedScope{}, []string{other, member, weakLink, legacy}, 0, 0},
		"unrestricted, narrowed":     {storage.Principal{OrgID: "org-1"}, narrowed, []string{member, weakLink}, 0, 2},
		"restricted, no narrowing":   {storage.Principal{OrgID: "org-1", RepositoryScopes: []string{memberAnchorSlug}}, contextfabric.RequestedScope{}, []string{other, member, legacy}, 1, 0},
		"restricted, narrowed":       {storage.Principal{OrgID: "org-1", RepositoryScopes: []string{memberAnchorSlug}}, narrowed, []string{member}, 1, 2},
	} {
		t.Run(name, func(t *testing.T) {
			wrap := production
			if len(tc.principal.RepositoryScopes) > 0 {
				wrap = productionPerRow
			}
			walk := s.walk(t, tc.principal, tc.scope, 25, wrap)
			got := []string{}
			for _, m := range walk.Members {
				got = append(got, m.Subject.CanonicalID)
			}
			sort.Strings(got)
			sort.Strings(tc.wantMembers)
			if !slices.Equal(got, tc.wantMembers) {
				t.Fatalf("members = %v, want %v", got, tc.wantMembers)
			}
			// Only what the grants hide is denied; the narrowing is scope.
			if walk.Denied != tc.wantDenied {
				t.Fatalf("denied = %d, want %d (a project-narrowing exclusion is not a denial)", walk.Denied, tc.wantDenied)
			}
			// The scope exclusion is counted apart, for the walk's Info line.
			if walk.ProjectExcluded != tc.wantExcluded {
				t.Fatalf("project excluded = %d, want %d", walk.ProjectExcluded, tc.wantExcluded)
			}
		})
	}
}
