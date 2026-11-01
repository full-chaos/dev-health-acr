package falkorgraph

import (
	"context"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12361: a cohort member walk (an anchor's members over ownership and
// tree hops) counts what a requested project narrowing leaves out as scope,
// never as an authorization denial: not in the walk's denied count and not in
// its authorization filter count, which the reader discloses as
// graph_cohort_denied_by_authorization. What the grants hide stays denied.
func TestACohortWalkCountsAProjectNarrowingExclusionAsScope(t *testing.T) {
	const (
		project = "project.v2:linear:proj-p"
		inGrant = "work_item.v2:11111111-1111-1111-1111-111111111111:1"
		outside = "work_item.v2:22222222-2222-2222-2222-222222222222:2"
	)
	issue := func(id, repo string) seededNode {
		return seededNode{kind: "work_item", id: id, label: id, repos: []string{repo}, workItemType: "issue", props: map[string]interface{}{
			propAuthzProjects: "*", propAuthzTeams: "*", graphrank.AuthorizationProjectIdentitiesAttr: []string{project},
		}}
	}
	nodes := []seededNode{
		// The anchor's repository list stands in for the project reach a
		// restricted caller's reads get (project_reach.go): the anchor is
		// visible to the restricted caller, and its members are decided by
		// the caller's grants.
		{kind: "project", id: project, label: "proj-p", props: map[string]interface{}{propAuthzProjects: []string{"proj-p"}, propAuthzTeams: "*", propAuthzRepos: []string{"acme/api"}}},
		issue(inGrant, "acme/api"), issue(outside, "acme/other"),
	}
	edges := []seededEdge{
		{"BELONGS_TO_PROJECT", "work_item", inGrant, "project", project, ""},
		{"BELONGS_TO_PROJECT", "work_item", outside, "project", project, ""},
	}
	// The production edge shape: BELONGS_TO_PROJECT carries its subject's own
	// authorization (devhealthsource teams_projects_edges.go): the issue's
	// repository, and the "*" project and team lists.
	repoOf := map[string]string{inGrant: "acme/api", outside: "acme/other"}
	production := func(conn *fakeConn) *fakeConn {
		inner := conn.queryFunc
		conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			rows, err := inner(ctx, key, cypher, params, readOnly)
			if err == nil && params["rel"] == "BELONGS_TO_PROJECT" {
				for _, r := range rows {
					e, ok := r["r"].(*edge)
					if !ok {
						continue
					}
					from := ""
					if n, ok := r["b"].(*node); ok {
						from = canonicalIDOf(n)
					}
					e.Properties[propAuthzProjects], e.Properties[propAuthzTeams] = "*", "*"
					e.Properties[propAuthzRepos] = []string{repoOf[from]}
				}
			}
			return rows, err
		}
		return conn
	}
	resolved := func(id string) contextfabric.RequestedScope {
		return contextfabric.RequestedScope{ProjectIDs: []string{id}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:" + id}, Complete: true}}
	}
	open, restricted := storage.Principal{OrgID: "org-1"}, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: project, Label: "proj-p"}
	for name, tc := range map[string]struct {
		principal    storage.Principal
		scope        contextfabric.RequestedScope
		wantMembers  []string
		wantDenied   int
		wantExcluded int
		wantOutcome  TeamAnchorWalkOutcome
	}{
		"unrestricted, no narrowing":             {open, contextfabric.RequestedScope{}, []string{inGrant, outside}, 0, 0, TeamAnchorWalkMembers},
		"unrestricted, the anchor's own project": {open, resolved("proj-p"), []string{inGrant, outside}, 0, 0, TeamAnchorWalkMembers},
		"unrestricted, another project":          {open, resolved("proj-1"), []string{}, 0, 2, TeamAnchorWalkNoMembers},
		"restricted, the anchor's own project":   {restricted, resolved("proj-p"), []string{inGrant}, 1, 0, TeamAnchorWalkMembers},
		"restricted, another project":            {restricted, resolved("proj-1"), []string{}, 1, 1, TeamAnchorWalkDenied},
	} {
		t.Run(name, func(t *testing.T) {
			adapter := newFakeAdapter(t, production(seededGraphConn(nodes, edges)))
			walk, err := adapter.teamAnchorMembers(context.Background(), "key", "org-1", tc.principal, tc.scope, anchor, treeIssue, 25, temporalFilter{}, memberStatusPredicate{})
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, n := range walk.nodes {
				if s, ok := graphrank.NodeSubject(n); ok && s.Kind == contextfabric.SubjectWorkItem {
					got = append(got, s.CanonicalID)
				}
			}
			sort.Strings(got)
			sort.Strings(tc.wantMembers)
			if len(got) != len(tc.wantMembers) || (len(got) > 0 && got[0] != tc.wantMembers[0]) {
				t.Fatalf("members = %v, want %v", got, tc.wantMembers)
			}
			// Only what the grants hide is denied, in both counts.
			if walk.denied != tc.wantDenied || walk.filters.Authz != tc.wantDenied {
				t.Fatalf("denied=%d authz filtered=%d, want %d (a project-narrowing exclusion is scope)", walk.denied, walk.filters.Authz, tc.wantDenied)
			}
			// The scope exclusions are counted apart, and an anchor the
			// narrowing alone refuses is not hidden: the walk's Info outcome
			// is never denied for it.
			if len(walk.hopProjectExcluded) != tc.wantExcluded {
				t.Fatalf("hop project excluded = %d, want %d", len(walk.hopProjectExcluded), tc.wantExcluded)
			}
			var tally teamAnchorWalkTally
			tally.add(walk)
			decision := tally.decision(contextfabric.SubjectProject, contextfabric.SubjectWorkItem, 1, nil)
			if decision.Outcome != tc.wantOutcome || decision.ProjectExcluded != tc.wantExcluded {
				t.Fatalf("walk decision = %s excluded %d, want %s %d", decision.Outcome, decision.ProjectExcluded, tc.wantOutcome, tc.wantExcluded)
			}
		})
	}
}

// The same rule on an intermediate hop: a team anchor's members reached
// through the projects it owns. A project the narrowing leaves out is out of
// scope at the hop that reaches it (treeWalkState.advance), so nothing under
// it is denied.
func TestACohortWalkCountsAnIntermediateProjectOutOfTheNarrowingAsScope(t *testing.T) {
	const (
		team    = "team:t"
		project = "project.v2:linear:proj-p"
		issueID = "work_item.v2:11111111-1111-1111-1111-111111111111:1"
	)
	nodes := []seededNode{
		{kind: "team", id: team, label: "t", props: map[string]interface{}{propAuthzTeams: []string{"t"}, propAuthzProjects: "*", propAuthzRepos: "*"}},
		{kind: "project", id: project, label: "proj-p", props: map[string]interface{}{propAuthzProjects: []string{"proj-p"}, propAuthzTeams: "*", propAuthzRepos: "*"}},
		{kind: "work_item", id: issueID, label: issueID, repos: []string{"acme/api"}, workItemType: "issue", props: map[string]interface{}{
			propAuthzProjects: "*", propAuthzTeams: "*", graphrank.AuthorizationProjectIdentitiesAttr: []string{project},
		}},
	}
	edges := []seededEdge{
		{"OWNED_BY_TEAM", "project", project, "team", team, ""},
		{"BELONGS_TO_PROJECT", "work_item", issueID, "project", project, ""},
	}
	// Production edge shapes: the ownership edge carries the project and
	// the team; the membership edge its subject's own authorization.
	production := func(conn *fakeConn) *fakeConn {
		inner := conn.queryFunc
		conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			rows, err := inner(ctx, key, cypher, params, readOnly)
			if err != nil {
				return rows, err
			}
			for _, r := range rows {
				e, ok := r["r"].(*edge)
				if !ok {
					continue
				}
				switch params["rel"] {
				case "OWNED_BY_TEAM":
					e.Properties[propAuthzProjects], e.Properties[propAuthzTeams], e.Properties[propAuthzRepos] = []string{"proj-p"}, []string{"t"}, "*"
				case "BELONGS_TO_PROJECT":
					e.Properties[propAuthzProjects], e.Properties[propAuthzTeams], e.Properties[propAuthzRepos] = "*", "*", []string{"acme/api"}
				}
			}
			return rows, nil
		}
		return conn
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: team, Label: "t"}
	for name, tc := range map[string]struct {
		scope                    contextfabric.RequestedScope
		members, denied, outside int
	}{
		"no narrowing": {contextfabric.RequestedScope{}, 1, 0, 0},
		"another project": {contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"},
			ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true}}, 0, 0, 1},
	} {
		walk, err := newFakeAdapter(t, production(seededGraphConn(nodes, edges))).teamAnchorMembers(context.Background(), "key", "org-1", storage.Principal{OrgID: "org-1"}, tc.scope, anchor, treeIssue, 25, temporalFilter{}, memberStatusPredicate{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(walk.nodes) != tc.members || walk.denied != tc.denied || walk.filters.Authz != tc.denied || len(walk.hopProjectExcluded) != tc.outside {
			t.Fatalf("%s: members=%d denied=%d authz=%d excluded=%d, want %d %d %d", name, len(walk.nodes), walk.denied, walk.filters.Authz, len(walk.hopProjectExcluded), tc.members, tc.denied, tc.outside)
		}
	}
}

// Review r1 P2: a project-anchored pull request walk reads its members in the
// fused link read (project -> issue -> pull request); what the narrowing
// leaves out there is counted in that read's scope count, and the team anchor
// walk's Info count includes it.
func TestATeamAnchorWalkCountsALinkReadExclusion(t *testing.T) {
	const (
		project = "project.v2:linear:proj-p"
		issueID = "work_item.v2:11111111-1111-1111-1111-111111111111:1"
		pr      = "pull_request:github:acme/api:1"
	)
	nodes := []seededNode{
		{kind: "project", id: project, label: "proj-p", props: map[string]interface{}{propAuthzProjects: []string{"proj-p"}, propAuthzTeams: "*", propAuthzRepos: "*"}},
		{kind: "work_item", id: issueID, label: issueID, repos: []string{"acme/api"}, workItemType: "issue", props: map[string]interface{}{
			propAuthzProjects: "*", propAuthzTeams: "*", graphrank.AuthorizationProjectIdentitiesAttr: []string{project},
		}},
		{kind: "repository", id: "repository:github:acme/api", label: "acme/api", repos: []string{"acme/api"}},
		{kind: "pull_request", id: pr, label: pr, repos: []string{"acme/api"}, props: map[string]interface{}{propAuthzProjects: "*", propAuthzTeams: "*"}},
	}
	edges := []seededEdge{
		{"BELONGS_TO_PROJECT", "work_item", issueID, "project", project, ""},
		{"BELONGS_TO_REPOSITORY", "pull_request", pr, "repository", "repository:github:acme/api", ""},
		linkEdge(issueID, pr, "native"),
	}
	production := func(conn *fakeConn) *fakeConn {
		inner := conn.queryFunc
		conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			rows, err := inner(ctx, key, cypher, params, readOnly)
			if err != nil {
				return rows, err
			}
			for _, r := range rows {
				for _, k := range []string{"m", "b"} {
					if n, ok := r[k].(*node); ok {
						if _, set := n.Properties[propAuthzTeams]; !set {
							n.Properties[propAuthzTeams] = "*"
						}
						if _, set := n.Properties[propAuthzProjects]; !set {
							n.Properties[propAuthzProjects] = "*"
						}
					}
				}
				for _, k := range []string{"r", "rl", "ra"} {
					if e, ok := r[k].(*edge); ok {
						e.Properties[propAuthzProjects], e.Properties[propAuthzTeams] = "*", "*"
						if _, set := e.Properties[propAuthzRepos]; !set {
							e.Properties[propAuthzRepos] = []string{"acme/api"}
						}
					}
				}
			}
			return rows, nil
		}
		return conn
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: project, Label: "proj-p"}
	for name, tc := range map[string]struct {
		scope            contextfabric.RequestedScope
		members, outside int
	}{
		"no narrowing": {contextfabric.RequestedScope{}, 1, 0},
		"another project": {contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"},
			ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true}}, 0, 1},
	} {
		walk, err := newFakeAdapter(t, production(seededGraphConn(nodes, edges))).teamAnchorMembers(context.Background(), "key", "org-1", storage.Principal{OrgID: "org-1"}, tc.scope, anchor, treePullRequest, 25, temporalFilter{}, memberStatusPredicate{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var tally teamAnchorWalkTally
		tally.add(walk)
		decision := tally.decision(contextfabric.SubjectProject, contextfabric.SubjectPullRequest, 1, nil)
		members := len(walk.nodes)
		if members != tc.members || walk.denied != 0 || decision.ProjectExcluded != tc.outside {
			t.Fatalf("%s: members=%d denied=%d link excluded=%d hop excluded=%d info excluded=%d, want %d 0 %d", name, members, walk.denied, walk.projectExcluded, len(walk.hopProjectExcluded), decision.ProjectExcluded, tc.members, tc.outside)
		}
	}
}
