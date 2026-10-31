package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12319 review r1, over the fake graph: (P2-a) the link-scoped census
// of a requested repository scope applies the same team narrowing as the
// repository walk; (P2-b) the walk is floored only for an issue the rest of
// the request admits, never for one it already refuses.

// productionTeamLists gives the link read's nodes the "*" team list every
// projected repository, pull request and work item carries
// (projection.go authorizationValue).
func productionTeamLists(rows []row) {
	for _, r := range rows {
		for _, key := range []string{"m", "b"} {
			if n, ok := r[key].(*node); ok {
				n.Properties[propAuthzTeams] = "*"
			}
		}
	}
}

func teamNode(id, requestedID string) seededNode {
	return seededNode{kind: "team", id: id, label: id, props: map[string]interface{}{propAuthzTeams: []string{requestedID}}}
}

func TestTheLinkScopedCensusAppliesTheTeamNarrowing(t *testing.T) {
	const inside = "work_item.v2:11111111-1111-1111-1111-111111111111:42"
	const outside = "work_item.v2:22222222-2222-2222-2222-222222222222:7"
	const unowned = "work_item.v2:33333333-3333-3333-3333-333333333333:9"
	s := newMemberSeed()
	s.issue(inside, []string{"acme/inside"})
	s.issue(outside, []string{"acme/outside"})
	s.issue(unowned, []string{"acme/unowned"})
	s.pullRequest("pull_request:github:acme/svc:1", memberAnchorSlug)
	s.link(inside, "pull_request:github:acme/svc:1", "native")
	s.link(outside, "pull_request:github:acme/svc:1", "native")
	s.link(unowned, "pull_request:github:acme/svc:1", "native")
	s.nodes = append(s.nodes, teamNode("team:anchor-owner", "anchor-owner"), teamNode("team:requested", "requested"), teamNode("team:other", "other"),
		seededNode{kind: "project", id: "project.v2:linear:inside", label: "inside"}, seededNode{kind: "project", id: "project.v2:linear:outside", label: "outside"},
		seededNode{kind: "project", id: "project.v2:linear:unowned", label: "unowned"})
	s.edges = append(s.edges,
		seededEdge{"OWNED_BY_TEAM", "repository", memberAnchorID, "team", "team:anchor-owner", ""},
		seededEdge{"BELONGS_TO_PROJECT", "work_item", inside, "project", "project.v2:linear:inside", ""},
		seededEdge{"OWNED_BY_TEAM", "project", "project.v2:linear:inside", "team", "team:requested", ""},
		seededEdge{"BELONGS_TO_PROJECT", "work_item", outside, "project", "project.v2:linear:outside", ""},
		seededEdge{"OWNED_BY_TEAM", "project", "project.v2:linear:outside", "team", "team:other", ""},
		// A project no team owns: missing data, so a census that leaves its
		// issue out is not the whole population.
		seededEdge{"BELONGS_TO_PROJECT", "work_item", unowned, "project", "project.v2:linear:unowned", ""},
	)
	conn := seededGraphConn(s.nodes, s.edges)
	inner := conn.queryFunc
	conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "$kinds") {
			r := fakeSubjectNodeRow("repository", memberAnchorID, memberAnchorSlug)
			r["n"].(*node).Properties[propAuthzRepos] = []string{memberAnchorSlug}
			r["n"].(*node).Properties[propAuthzTeams] = "*"
			return []row{r}, nil
		}
		rows, err := inner(ctx, key, cypher, params, readOnly)
		if err == nil && params["linkRel"] != nil {
			productionTeamLists(rows)
		}
		return rows, err
	}
	adapter := newFakeAdapter(t, conn)
	for name, tc := range map[string]struct {
		teams    []string
		want     []string
		complete bool
	}{
		"no narrowing":                 {nil, []string{inside, outside, unowned}, true},
		"a team that owns one project": {[]string{"requested"}, []string{inside}, false},
		"the anchor's owner":           {[]string{"anchor-owner"}, []string{inside, outside, unowned}, true},
	} {
		population, complete, err := adapter.linkScopedIssues(context.Background(), "key", open(), contextfabric.RequestedScope{RepositorySlugs: []string{memberAnchorSlug}, TeamIDs: tc.teams}, 25)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(population) != len(tc.want) || complete != tc.complete {
			t.Fatalf("%s: population = %v complete=%v, want %v complete=%v", name, population, complete, tc.want, tc.complete)
		}
		for _, id := range tc.want {
			if population[id] != "native" {
				t.Fatalf("%s: population = %v, want %v", name, population, tc.want)
			}
		}
	}
}

func TestATeamNarrowingFloorsOnlyForAnIssueTheRestOfTheRequestAdmits(t *testing.T) {
	const issue = "work_item.v2:11111111-1111-1111-1111-111111111111:42"
	s := newMemberSeed()
	s.issue(issue, []string{"acme/other"})
	s.pullRequest("pull_request:github:acme/svc:1", memberAnchorSlug)
	s.link(issue, "pull_request:github:acme/svc:1", "native")
	// The anchor is owned by another team; the issue's project has no owner.
	s.nodes = append(s.nodes, teamNode("team:anchor-owner", "anchor-owner"), seededNode{kind: "project", id: "project.v2:linear:actual", label: "actual"})
	s.edges = append(s.edges,
		seededEdge{"OWNED_BY_TEAM", "repository", memberAnchorID, "team", "team:anchor-owner", ""},
		seededEdge{"BELONGS_TO_PROJECT", "work_item", issue, "project", "project.v2:linear:actual", ""},
	)
	conn := seededGraphConn(s.nodes, s.edges)
	inner := conn.queryFunc
	conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		rows, err := inner(ctx, key, cypher, params, readOnly)
		if err == nil && params["linkRel"] != nil {
			productionTeamLists(rows)
			for _, r := range rows {
				if n, ok := r["b"].(*node); ok && propStringValue(n.Properties[propKind]) == "work_item" {
					n.Properties[propAuthzProjects] = []string{"actual"}
					n.Properties[graphrank.AuthorizationProjectIdentitiesAttr] = []string{"project.v2:linear:actual"}
				}
			}
		}
		return rows, err
	}
	adapter := newFakeAdapter(t, conn)
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	refused := contextfabric.RequestedScope{ProjectIDs: []string{"requested"}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:requested"}, Complete: true}}
	for name, tc := range map[string]struct {
		scope         contextfabric.RequestedScope
		wantTruncated bool
	}{
		// The project narrowing already refuses the issue: its project's
		// missing owner is not needed to decide this request.
		"the rest of the request refuses the issue": {refused, false},
		// The issue is admitted otherwise and left to (ii), whose owner is missing.
		"the rest of the request admits the issue": {contextfabric.RequestedScope{}, true},
	} {
		tc.scope.TeamIDs = []string{"requested"}
		walk, err := adapter.TreeWorkItemMembers(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.ResolvedGraphBinding{}, tc.scope, anchor, 25)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(walk.Members) != 0 || walk.Truncated != tc.wantTruncated || !walk.AnchorOutsideTeamNarrowing {
			t.Fatalf("%s: members=%d truncated=%v outside=%v, want 0, truncated=%v, outside", name, len(walk.Members), walk.Truncated, walk.AnchorOutsideTeamNarrowing, tc.wantTruncated)
		}
	}
}
