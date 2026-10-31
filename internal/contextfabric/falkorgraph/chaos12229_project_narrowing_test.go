package falkorgraph

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12229: the resolver maps requested raw project ids to canonical
// identities through the project nodes, never widening a raw id that names
// none or more than one project.

// narrowingLookupRows answers the lookup as the server would: one row per (requested
// raw id, project node carrying it).
func narrowingLookupRows(byRawID map[string][]string) *fakeConn {
	return &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if cypher != projectNarrowingLookupCypher || !readOnly || params["kind"] != string(contractsv1.ContextFabricSubjectProject) || params["org"] != "org_1" {
			return nil, errors.New("unexpected query")
		}
		var rows []row
		for _, id := range params["ids"].([]interface{}) {
			for _, identity := range byRawID[id.(string)] {
				rows = append(rows, row{"rid": id, "identity": identity})
			}
		}
		return rows, nil
	}}
}

func TestTheResolverMapsEachRequestedIdToExactlyOneProject(t *testing.T) {
	adapter := newFakeAdapter(t, narrowingLookupRows(map[string][]string{
		"proj-1":   {"project.v2:linear:proj-1"},
		"P-SHARED": {"project.v2:github:P-SHARED", "project.v2:jira:P-SHARED"},
		"proj-bad": {"not-a-project-identity"},
		"proj-np":  {"project.v2::proj-np"},
		"proj-ni":  {"project.v2:linear:"},
	}))
	principal := storage.Principal{OrgID: "org_1"}
	cases := map[string]struct {
		ids      []string
		want     []string
		complete bool
	}{
		"one id, one project":           {[]string{"proj-1"}, []string{"project.v2:linear:proj-1"}, true},
		"repeats and blanks are one id": {[]string{"proj-1", " proj-1 ", ""}, []string{"project.v2:linear:proj-1"}, true},
		"an id under two providers":     {[]string{"P-SHARED"}, []string{}, false},
		"an id no project carries":      {[]string{"proj-9"}, []string{}, false},
		"a resolved and an unknown id":  {[]string{"proj-1", "proj-9"}, []string{"project.v2:linear:proj-1"}, false},
		"a non-canonical identity row":  {[]string{"proj-bad"}, []string{}, false},
		"an identity with no provider":  {[]string{"proj-np"}, []string{}, false},
		"an identity with no id":        {[]string{"proj-ni"}, []string{}, false},
	}
	for name, tc := range cases {
		got, err := adapter.ResolveProjectNarrowing(context.Background(), principal, contextfabric.ResolvedGraphBinding{}, tc.ids)
		if err != nil {
			t.Fatalf("%s: error = %v", name, err)
		}
		if got == nil || got.Complete != tc.complete || len(got.Identities) != len(tc.want) {
			t.Fatalf("%s: narrowing = %+v, want %v complete=%v", name, got, tc.want, tc.complete)
		}
		for i := range tc.want {
			if got.Identities[i] != tc.want[i] {
				t.Fatalf("%s: identities = %v, want %v", name, got.Identities, tc.want)
			}
		}
	}
	// A requested "*" narrows nothing: nothing is resolved or read.
	if got, err := newFakeAdapter(t, &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return nil, errors.New("a requested wildcard must not be looked up")
	}}).ResolveProjectNarrowing(context.Background(), principal, contextfabric.ResolvedGraphBinding{}, []string{"*"}); err != nil || got != nil {
		t.Fatalf("a requested wildcard: narrowing %+v, error %v, want none", got, err)
	}
	// A failed read is an error, never a narrowing.
	if got, err := newFakeAdapter(t, &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return nil, errors.New("graph down")
	}}).ResolveProjectNarrowing(context.Background(), principal, contextfabric.ResolvedGraphBinding{}, []string{"proj-1"}); err == nil || got != nil {
		t.Fatalf("a failed read: narrowing %+v, error %v, want an error", got, err)
	}
}

// Q4: an edge's own "*" project list defers to its endpoints, so a narrowing
// whose endpoints are stated members is not reported as unproven.
func TestAnEdgeWithAWildcardProjectListDefersToItsEndpoints(t *testing.T) {
	scope := contextfabric.RequestedScope{
		ProjectIDs:       []string{"proj-1"},
		ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true},
	}
	member := map[string]interface{}{"authorization_repositories": []string{"acme/api"}, "authorization_projects": "*", graphrank.AuthorizationProjectIdentitiesAttr: []string{"project.v2:linear:proj-1"}}
	unstated := map[string]interface{}{"authorization_repositories": []string{"acme/api"}, "authorization_projects": "*"}
	wildcardEdge := map[string]interface{}{"authorization_repositories": []string{"acme/api"}, "authorization_projects": "*"}
	principal := storage.Principal{OrgID: "org_1"}
	nodes := []graphrank.CandidateNode{{Attributes: member}}
	if projectNarrowingRestsOnWildcard(principal, scope, nodes, []graphrank.ResolvedEdge{{Attributes: wildcardEdge}}) {
		t.Fatal("an edge between stated members is reported unproven on its own \"*\"")
	}
	if !projectNarrowingRestsOnWildcard(principal, scope, nodes, []graphrank.ResolvedEdge{{Attributes: wildcardEdge, EndpointProjectNarrowingUnproven: true}}) {
		t.Fatal("an edge whose endpoint is unproven is not reported")
	}
	if !projectNarrowingRestsOnWildcard(principal, scope, []graphrank.CandidateNode{{Attributes: unstated}}, nil) {
		t.Fatal("a node with unstated membership is not reported")
	}
}

// Review r1 P2: a tree walk narrows every issue by its stated membership --
// a repository-less issue (admitted by its link) included -- and records an
// issue it admitted only on its "*" project list.
func TestATreeWalkNarrowsEveryIssueByStatedMembership(t *testing.T) {
	scope := contextfabric.RequestedScope{
		ProjectIDs:       []string{"proj-1"},
		ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true},
	}
	issue := func(repositories []string, stated interface{}) *node {
		properties := map[string]interface{}{
			propKind: string(contextfabric.SubjectWorkItem), propCanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:ENG-1",
			propAuthzRepos: repositories, propAuthzProjects: "*",
		}
		if stated != nil {
			properties[propAuthzProjectIdentities] = stated
		}
		return &node{Properties: properties}
	}
	repositoryLessRepos, ownRepository := []string{noRepositoryScope}, []string{"acme/api"}
	native := linkTier{name: "native", grantsAuthority: true}
	for name, tc := range map[string]struct {
		n          *node
		admitted   bool
		unverified bool
	}{
		"a repository-less member":                   {issue(repositoryLessRepos, []interface{}{"project.v2:linear:proj-1"}), true, false},
		"a repository-less issue of another project": {issue(repositoryLessRepos, []interface{}{"project.v2:linear:proj-2"}), false, false},
		"a repository-less issue, unstated":          {issue(repositoryLessRepos, nil), true, true},
		"an issue of its own repository, a member":   {issue(ownRepository, []interface{}{"project.v2:linear:proj-1"}), true, false},
		"an issue of its own repository, another":    {issue(ownRepository, []interface{}{"project.v2:linear:proj-2"}), false, false},
		"an issue of its own repository, unstated":   {issue(ownRepository, nil), true, true},
	} {
		state := treeWalkState{out: &treeWalk{}, principal: storage.Principal{OrgID: "org_1"}, scope: scope}
		if got := state.admitted(treeIssue, tc.n, native); got != tc.admitted {
			t.Errorf("%s: admitted = %v, want %v", name, got, tc.admitted)
		}
		if state.out.projectNarrowingUnverified != tc.unverified {
			t.Errorf("%s: recorded unverified = %v, want %v", name, state.out.projectNarrowingUnverified, tc.unverified)
		}
	}
}
