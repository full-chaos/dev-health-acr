package falkorgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
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
			if !slices.Equal(got, tc.wantMembers) {
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

// Review r2 P2: the deployment route (anchorDeploymentMembers) decides its
// anchor as the team anchor route does. A project anchor the requested
// narrowing alone refuses is out of scope: the walk reads nothing, serves no
// deployment and denies none; its own project, or no narrowing, walks.
func TestTheDeploymentRouteKeepsAnAnchorOutOfTheNarrowingOutOfScope(t *testing.T) {
	const project = "project.v2:linear:payments"
	nodes := []seededNode{{kind: "project", id: project, label: "payments", props: map[string]interface{}{propAuthzProjects: []string{"payments"}, propAuthzTeams: "*", propAuthzRepos: "*"}}}
	narrowed := func(id string) contextfabric.RequestedScope {
		return contextfabric.RequestedScope{ProjectIDs: []string{id}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:" + id}, Complete: true}}
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: project, Label: "payments"}
	open := func() storage.Principal { return storage.Principal{OrgID: "org-1"} }
	for name, tc := range map[string]struct {
		scope      contextfabric.RequestedScope
		outOfScope bool
		principal  func() storage.Principal
	}{
		"no narrowing":             {contextfabric.RequestedScope{}, false, open},
		"the anchor's own project": {narrowed("payments"), false, open},
		"another project":          {narrowed("requested"), true, open},
		// The caller's grants refuse the anchor even without the narrowing
		// (no project reach on this read): that is not a scope decision, and
		// the walk runs and decides every node as before.
		"another project, an anchor the grants refuse": {narrowed("requested"), false, func() storage.Principal {
			return storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
		}},
	} {
		conn := seededGraphConn(nodes, nil)
		inner, walked := conn.queryFunc, 0
		conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if params["fromKind"] != nil || params["linkRel"] != nil {
				walked++
			}
			return inner(ctx, key, cypher, params, readOnly)
		}
		walk, err := newFakeAdapter(t, conn).anchorDeploymentMembers(context.Background(), "key", "org-1", tc.principal(), tc.scope, anchor, 50, newTemporalFilter(contextfabric.TimeContext{}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if walk.anchorOutOfScope != tc.outOfScope || (walked == 0) != tc.outOfScope || len(walk.nodes) != 0 || walk.denied != 0 {
			t.Fatalf("%s: out of scope=%v walked=%d members=%d denied=%d, want out of scope=%v", name, walk.anchorOutOfScope, walked, len(walk.nodes), walk.denied, tc.outOfScope)
		}
		if got := projectDeploymentWalkOutcome(walk, false, nil); tc.outOfScope && got == ProjectDeploymentWalkDenied {
			t.Fatalf("%s: outcome %s for an anchor out of scope", name, got)
		}
	}
}

// The same through DiscoverContext's deployment route (review r2's shape):
// the reader serves no deployment under an anchor out of scope, and the
// walk's Info decision carries anchor_out_of_scope.
func TestTheDeploymentRouteDecisionCarriesAnAnchorOutOfScope(t *testing.T) {
	const (
		projectID = "project:payments"
		issueID   = "work_item:linear:ENG-1"
		prID      = "pull_request:github:acme/api:1"
		repoID    = "repository:github:acme/api"
		deployID  = "deployment:acme/api:1"
	)
	s := projectSeed{nodes: []seededNode{
		{kind: "project", id: projectID, label: "payments", props: map[string]interface{}{propAuthzProjects: []string{"payments"}}},
		{kind: "work_item", id: issueID, label: "ENG-1", repos: []string{noRepositoryScope}, workItemType: "issue", props: map[string]interface{}{propAuthzProjects: "*"}},
		{kind: "pull_request", id: prID, label: "PR 1", repos: []string{"acme/api"}},
		{kind: "repository", id: repoID, label: "acme/api", repos: []string{"acme/api"}},
		{kind: "deployment", id: deployID, label: "deploy 1", repos: []string{"acme/api"}},
	}, edges: []seededEdge{
		{"BELONGS_TO_PROJECT", "work_item", issueID, "project", projectID, ""},
		linkEdge(issueID, prID, "native"),
		{"BELONGS_TO_REPOSITORY", "pull_request", prID, "repository", repoID, ""},
		{"BELONGS_TO_REPOSITORY", "deployment", deployID, "repository", repoID, ""},
	}}
	for name, tc := range map[string]struct {
		scope      contextfabric.RequestedScope
		outOfScope bool
	}{
		"no narrowing": {contextfabric.RequestedScope{}, false},
		"another project": {contextfabric.RequestedScope{ProjectIDs: []string{"requested"},
			ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:requested"}, Complete: true}}, true},
	} {
		request := projectDeploymentsRequest()
		request.Request.RequestedScope = tc.scope
		recording := &recordingTelemetry{}
		result, err := newFakeAdapterWithTelemetry(t, seededGraphConn(s.nodes, s.edges), recording).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		served := 0
		if result.Cohort != nil {
			served = len(result.Cohort.Members)
		}
		if len(recording.projectDeploymentWalks) != 1 || recording.projectDeploymentWalks[0].AnchorOutOfScope != tc.outOfScope || (served == 0) != tc.outOfScope {
			t.Fatalf("%s: served=%d decisions=%+v, want anchor out of scope=%v", name, served, recording.projectDeploymentWalks, tc.outOfScope)
		}
	}
}

// Review r3 P3: the two Info lines carry the new fields' values, not only
// their keys.
func TestTheWalkLinesCarryTheScopeValues(t *testing.T) {
	var buf bytes.Buffer
	telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	telemetry.RecordTeamAnchorWalk(context.Background(), "org-1", TeamAnchorWalkDecision{Outcome: TeamAnchorWalkNoMembers, AnchorKind: contextfabric.SubjectProject, MemberKind: contextfabric.SubjectWorkItem, Committed: 1, ProjectExcluded: 3})
	telemetry.RecordProjectDeploymentWalk(context.Background(), "org-1", ProjectDeploymentWalkDecision{Outcome: ProjectDeploymentWalkNoDeployments, AnchorKind: contextfabric.SubjectProject, Committed: 1, AnchorOutOfScope: true})
	var lines []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if len(lines) != 2 || lines[0]["project_excluded"] != float64(3) || lines[1]["anchor_out_of_scope"] != true {
		t.Fatalf("lines = %v, want project_excluded=3 and anchor_out_of_scope=true", lines)
	}
}
