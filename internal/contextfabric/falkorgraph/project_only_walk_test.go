package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func projectOnlyWalkPrincipal() storage.Principal {
	return storage.Principal{OrgID: "org_1", ProjectScopes: []string{"project.v2:github:P-1"}}
}

func projectOnlyWalkNode(kind contextfabric.SubjectKind, id string, extra map[string]interface{}) *node {
	properties := map[string]interface{}{propKind: string(kind), propCanonicalID: id, propLabel: id, propAuthzRepos: "*", propAuthzProjects: "*", propAuthzTeams: "*"}
	for key, value := range extra {
		properties[key] = value
	}
	return &node{Properties: properties}
}

// CHAOS-12299: inside a link read, a project-only caller's issue is decided by
// the node rule. A native link grants it no authority, a repository-less issue
// is not admitted for want of a repository list, and a pull request is denied.
func TestAProjectOnlyLinkPositionIsDecidedByTheNodeRule(t *testing.T) {
	t.Parallel()
	state := treeWalkState{principal: projectOnlyWalkPrincipal()}
	native := linkTier{name: "native", grantsAuthority: true}
	repositoryLessIssue := projectOnlyWalkNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-1", map[string]interface{}{propAuthzRepos: []string{noRepositoryScope}})
	member := projectOnlyWalkNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-2", map[string]interface{}{propAuthzRepos: []string{"acme/api"}, propAuthzProjectIdentities: []string{"project.v2:github:P-1"}})
	repositoryLessMember := projectOnlyWalkNode(contextfabric.SubjectWorkItem, "work_item.v2:r:W-3", map[string]interface{}{propAuthzRepos: []string{noRepositoryScope}, propAuthzProjectIdentities: []string{"project.v2:github:P-1"}})
	pullRequest := projectOnlyWalkNode(contextfabric.SubjectPullRequest, "pull_request:acme/api:1", map[string]interface{}{propAuthzRepos: []string{"acme/api"}})
	for _, tc := range []struct {
		name     string
		position treePosition
		node     *node
		want     bool
	}{
		{name: "a repository-less issue with no stated membership, on a native link", position: treeIssue, node: repositoryLessIssue, want: false},
		{name: "a member issue", position: treeIssue, node: member, want: true},
		{name: "a repository-less member issue", position: treeIssue, node: repositoryLessMember, want: true},
		{name: "a pull request", position: treePullRequest, node: pullRequest, want: false},
	} {
		if got := state.admitted(tc.position, tc.node, native); got != tc.want {
			t.Errorf("%s: admitted = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// E1 and E2 at the falkorgraph edge half: a structural edge with a "*" project
// list defers to its ends, a raw project list and an attribution edge do not.
func TestAProjectOnlyEdgeHalfDefersToItsEnds(t *testing.T) {
	t.Parallel()
	principal := projectOnlyWalkPrincipal()
	star := map[string]interface{}{propAuthzProjects: "*", propAuthzRepos: []string{"acme/other"}}
	if !authorizedEdgeAttributes(principal, contextfabric.RequestedScope{}, "BELONGS_TO_PROJECT", star) {
		t.Error(`a "*" structural edge was denied before its ends were read`)
	}
	if authorizedEdgeAttributes(principal, contextfabric.RequestedScope{}, "BELONGS_TO_PROJECT", map[string]interface{}{propAuthzProjects: []string{"P-1"}}) {
		t.Error("a raw project list admitted a project-only caller")
	}
	if authorizedEdgeAttributes(principal, contextfabric.RequestedScope{}, "DOCUMENTED_BY", star) {
		t.Error("an attribution edge admitted a project-only caller")
	}
}

// A project-only caller reads no project reach: the node rule decides a
// project by its canonical id. It is still restricted wherever the walk asks
// whether the caller sees only some repositories.
func TestAProjectOnlyCallerReadsNoProjectReachAndIsRestricted(t *testing.T) {
	t.Parallel()
	for _, principal := range []storage.Principal{
		projectOnlyWalkPrincipal(),
		{OrgID: "org_1", RepositoryScopes: []string{"*"}, ProjectScopes: []string{"project.v2:github:P-1"}},
		{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}, ProjectScopes: []string{"project.v2:github:P-1"}},
	} {
		// needsProjectReach is what statusCensusPage, DiscoverContext's
		// deployment walk outcome, linkSegment and treeWalkState.narrowed
		// read (CHAOS-12299 r3).
		if !needsProjectReach(principal) {
			t.Errorf("%v: a project-only caller is not restricted", principal.RepositoryScopes)
		}
		if !(treeWalkState{principal: principal}).narrowed() {
			t.Errorf("%v: a project-only caller's tree walk is not narrowed", principal.RepositoryScopes)
		}
		ctx, err := (&Adapter{}).withProjectReach(context.Background(), "key", principal)
		if err != nil || ctx.Value(projectReachContextKey{}) != nil {
			t.Errorf("%v: project reach armed for a project-only caller (err %v)", principal.RepositoryScopes, err)
		}
	}
	for _, tc := range []struct {
		scopes []string
		want   bool
	}{{nil, false}, {[]string{"*"}, false}, {[]string{" * "}, false}, {[]string{"acme/api"}, true}, {[]string{" "}, true}} {
		if got := needsProjectReach(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}); got != tc.want {
			t.Errorf("%q: needsProjectReach = %v, want %v", tc.scopes, got, tc.want)
		}
	}
}

// E2 in the generic walk: resolveEdge refuses an attribution edge to a
// project-only caller on the edge half, before it reads either end.
func TestResolveEdgeRefusesAProjectOnlyCallerAnAttributionEdgeBeforeItsEnds(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("resolveEdge read the ends of an attribution edge for a project-only caller: %v", r)
		}
	}()
	ce := graphrank.CandidateEdge{UUID: "e", Name: "DOCUMENTED_BY", SourceNodeUUID: "work_item:W-1", TargetNodeUUID: "document:D-1", Attributes: map[string]interface{}{propAuthzProjects: "*"}}
	_, resolution, reason, err := (&Adapter{}).resolveEdge(context.Background(), "key", "org_1", projectOnlyWalkPrincipal(), contextfabric.RequestedScope{}, ce, temporalFilter{})
	if err != nil || resolution != edgeFiltered || reason != edgeFilterReasonAuthz {
		t.Fatalf("resolveEdge = %v/%v/%v, want filtered on authorization", resolution, reason, err)
	}
}

// CHAOS-12299 r3: the link read binds no repository grant for a project-only
// caller, whatever its repository list, so "*" never matches every
// repository in the read; the node rule then decides each row.
func TestTheLinkReadBindsNoRepositoryGrantForAProjectOnlyCaller(t *testing.T) {
	t.Parallel()
	for _, scopes := range [][]string{nil, {"*"}, {"acme/api"}} {
		params := linkSegmentGrants(map[string]interface{}{}, storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}})
		for _, key := range []string{"grantRaw", "grantNorm", "grantOwners"} {
			if values, _ := params[key].([]interface{}); len(values) != 0 {
				t.Errorf("%q: %s = %v, want empty", scopes, key, values)
			}
		}
	}
	params := linkSegmentGrants(map[string]interface{}{}, storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}})
	if values, _ := params["grantRaw"].([]interface{}); len(values) != 1 {
		t.Errorf("a restricted caller's link grant = %v, want its one repository as before", params["grantRaw"])
	}
}
