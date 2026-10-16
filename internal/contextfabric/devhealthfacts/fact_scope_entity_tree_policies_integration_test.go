package devhealthfacts_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The entity-tree policies, run against a real ClickHouse: the statements use
// window functions, FINAL, argMax and joins a fake client cannot execute.
//
// The tree is Repository <> Pull request <> Issue <> Project. The fixture is
// built so that following the issue's own repository column, or a computed
// team attribution, reaches a DIFFERENT answer than the tree does: every
// assertion below is therefore a statement about the tree.
const (
	policyTreeOrgID    = "6e7a3b1c-9f40-4c6e-8d52-3a1f7c2b9d10"
	policyTreeRepoA    = "a1000000-0000-4000-8000-000000000001"
	policyTreeRepoB    = "b2000000-0000-4000-8000-000000000002"
	policyTreeRepoC    = "c3000000-0000-4000-8000-000000000003"
	policyTreeRepoD    = "d4000000-0000-4000-8000-000000000004"
	policyTreeSlugA    = "acme/repo-a"
	policyTreeSlugB    = "acme/repo-b"
	policyTreeSlugC    = "acme/repo-c"
	policyTreeSlugD    = "acme/repo-d"
	policyTreeProject  = "PROJ-TREE"
	policyTreeProvider = "linear"
)

type policyTreeExec interface {
	Exec(ctx context.Context, query string, args ...any) error
}

func seedEntityTree(t *testing.T, ctx context.Context, direct policyTreeExec, at time.Time) {
	t.Helper()
	for _, statement := range devhealthschema.DDL("teams", "work_items", "repos", "projects", "team_project_ownership", "team_repo_ownership", "work_graph_issue_pr", "work_graph_deployment_incident_edges", "operational_incidents", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	for _, repo := range [][2]string{{policyTreeRepoA, policyTreeSlugA}, {policyTreeRepoB, policyTreeSlugB}, {policyTreeRepoC, policyTreeSlugC}, {policyTreeRepoD, policyTreeSlugD}} {
		exec("repo "+repo[1], `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo[0], policyTreeOrgID, repo[1], "github", at)
	}
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		policyTreeProject, policyTreeOrgID, policyTreeProvider, nil, "Tree", uint8(1), "active", "", at)

	// Issues of the project. Their OWN repository column names repo C for
	// every one of them: the tree must never reach C through it.
	issue := func(id string) {
		exec("issue "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, policyTreeRepoC, policyTreeOrgID, "issue "+id, "open", "", "", policyTreeProject, at)
	}
	// link writes the link row AND the pull request it names: a link of
	// record is read only with its pull request, as the canonical link
	// projection reads it.
	link := func(label, issueID, repoID string, pr uint32, provenance string) {
		exec(label, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, issueID, pr, float32(0.9), provenance, "", at, policyTreeOrgID)
		exec(label+" pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
			repoID, policyTreeOrgID, pr, "PR", "open", at, at, "feat/x", "")
	}
	issue("I-native-a")
	link("link native to A", "I-native-a", policyTreeRepoA, 1, "native")
	issue("I-heuristic-b")
	link("link heuristic to B", "I-heuristic-b", policyTreeRepoB, 2, "heuristic")
	// A second, stronger link to repo B must win B's tier.
	issue("I-text-b")
	link("link explicit_text to B", "I-text-b", policyTreeRepoB, 3, "explicit_text")
	issue("I-unlinked")

	// Teams. owner-a owns repo A and the project; owner-b owns repo B only;
	// bare owns the project and owns no repository at all.
	for _, team := range []string{"owner-a", "owner-b", "bare"} {
		exec("team "+team, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			team, team, "", at, policyTreeOrgID, policyTreeProvider, team, uint8(1))
	}
	repoOwnership := func(label, team, repoID, slug string, validTo any) {
		exec(label, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			policyTreeOrgID, "github", team, repoID, slug, "exact", "native", uint8(1), uint16(100), int32(0), at.Add(-time.Hour), validTo, at)
	}
	repoOwnership("owner-a owns A", "owner-a", policyTreeRepoA, policyTreeSlugA, nil)
	repoOwnership("owner-b owns B", "owner-b", policyTreeRepoB, policyTreeSlugB, nil)
	// An ended ownership: repo C was owned by owner-a and no longer is.
	repoOwnership("owner-a owned C until an hour ago", "owner-a", policyTreeRepoC, policyTreeSlugC, at.Add(-time.Minute))
	for _, team := range []string{"owner-a", "bare"} {
		exec("project ownership "+team, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			policyTreeOrgID, policyTreeProvider, team, policyTreeProject, nil, "native", at.Add(-time.Hour), nil, at)
	}

	// Incidents. X touches only repo A; Y touches A and B; Z touches only B.
	incident := func(id string) {
		exec("incident "+id, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES (?,?,?,?,?,?,?,?,?)`,
			policyTreeOrgID, at, id, at, at, "svc", "Incident "+id, at, uint8(0))
	}
	edge := func(id, incidentID, repoID string) {
		exec("edge "+id, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, policyTreeOrgID, "dep-"+id, incidentID, repoID, float32(0.9), "native", "", at, at)
	}
	incident("INC-X")
	incident("INC-Y")
	incident("INC-Z")
	edge("e1", "INC-X", policyTreeRepoA)
	edge("e2", "INC-Y", policyTreeRepoA)
	edge("e3", "INC-Y", policyTreeRepoB)
	edge("e4", "INC-Z", policyTreeRepoB)
}

func policyTreeProjectOrigin(t *testing.T) contextfabric.SubjectRef {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindProject, []string{policyTreeProvider, policyTreeProject}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: err=%v omitted=%v", err, omitted)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: id, Label: "Tree"}
}

func policyTreeTeamOrigin(team string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + team, Label: team}
}

func policyTreeExpand(t *testing.T, ctx context.Context, expander *devhealthfacts.ScopeExpander, principal storage.Principal, policy contextfabric.FactScopePolicy, requirement contextfabric.FactKind, origin contextfabric.SubjectRef, target contextfabric.SubjectKind) contextfabric.FactScopeExpansionResult {
	t.Helper()
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: principal, RequirementKind: requirement,
		Origins: []contextfabric.SubjectRef{origin}, Policy: policy, TargetKind: target, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope(%s): %v", policy, err)
	}
	return result
}

func policyTreeLabels(targets []contextfabric.SubjectRef) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.CanonicalID)
	}
	sort.Strings(out)
	return out
}

func policyTreeAssertTargets(t *testing.T, got []contextfabric.SubjectRef, want ...string) {
	t.Helper()
	sort.Strings(want)
	have := policyTreeLabels(got)
	if len(have) != len(want) {
		t.Fatalf("targets = %v, want %v", have, want)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Fatalf("targets = %v, want %v", have, want)
		}
	}
}

// A project reaches repositories through its issues' LINKED pull request
// rows. Every issue's own repository column names repo C; reaching C would be
// the column, not the tree.
func TestPolicyTree_ProjectReachesRepositoriesOnlyThroughLinkedPullRequests(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)
	unrestricted := storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}}

	result := policyTreeExpand(t, ctx, expander, unrestricted,
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		policyTreeProjectOrigin(t), contextfabric.SubjectRepository)
	policyTreeAssertTargets(t, result.Targets, "repository:"+policyTreeRepoA, "repository:"+policyTreeRepoB)
	if result.Counts.MissingNextHopCount != 1 {
		t.Fatalf("MissingNextHopCount = %d, want 1 (the project's unlinked issue)", result.Counts.MissingNextHopCount)
	}
	// The strongest tier among a repository's links names its provenance.
	for _, target := range result.Targets {
		key := contextfabric.FactSubjectKey(target)
		switch target.CanonicalID {
		case "repository:" + policyTreeRepoA:
			if got := result.TargetAttributionSource[key]; got != "native" {
				t.Fatalf("repo A tier = %q, want native", got)
			}
		case "repository:" + policyTreeRepoB:
			if got := result.TargetAttributionSource[key]; got != "explicit_text" {
				t.Fatalf("repo B tier = %q, want explicit_text (outranks heuristic)", got)
			}
		}
	}

	// The same chain feeds deployments.
	deployments := policyTreeExpand(t, ctx, expander, unrestricted,
		contextfabric.FactScopePolicyProjectIssuePullRequestDeployments, contextfabric.FactDeployments,
		policyTreeProjectOrigin(t), contextfabric.SubjectRepository)
	policyTreeAssertTargets(t, deployments.Targets, "repository:"+policyTreeRepoA, "repository:"+policyTreeRepoB)

	// A repository-restricted caller loses B and the loss is counted.
	restricted := storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{policyTreeSlugA}}
	narrowed := policyTreeExpand(t, ctx, expander, restricted,
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		policyTreeProjectOrigin(t), contextfabric.SubjectRepository)
	policyTreeAssertTargets(t, narrowed.Targets, "repository:"+policyTreeRepoA)
	if narrowed.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1", narrowed.Counts.AuthorizationDroppedCount)
	}
}

// A project whose issues carry no link row reaches nothing, and says why.
func TestPolicyTree_ProjectWithOnlyUnlinkedIssuesIsACountedMissingHop(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		policyTreeProjectOrigin(t), contextfabric.SubjectRepository)
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %v, want none: the issue's own repository column is not a link", policyTreeLabels(result.Targets))
	}
	if result.Counts.MissingNextHopCount == 0 {
		t.Fatal("MissingNextHopCount = 0 -- issues without a pull request link must be counted, never read as an empty project")
	}
}

// A link row whose provenance is outside ops' three tiers is counted and never
// admitted.
func TestPolicyTree_UnknownLinkProvenanceIsCountedNotAdmitted(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		policyTreeRepoA, "I-native-a", uint32(1), float32(0.9), "guessed", "", at, policyTreeOrgID); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		policyTreeRepoA, policyTreeOrgID, uint32(1), "PR", "open", at, at, "feat/x", ""); err != nil {
		t.Fatalf("seed pull request: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		policyTreeProjectOrigin(t), contextfabric.SubjectRepository)
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %v, want none for an unrecognised provenance", policyTreeLabels(result.Targets))
	}
	if result.Counts.UnknownAttributionSourceCount != 1 {
		t.Fatalf("UnknownAttributionSourceCount = %d, want 1", result.Counts.UnknownAttributionSourceCount)
	}
}

// A team reaches repositories through ownership rows it holds NOW. An ended
// ownership (repo C) and another team's repository (B) are not reached.
func TestPolicyTree_TeamReachesOnlyCurrentlyOwnedRepositories(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyTeamRepositoryOwnershipContinuousIntegration, contextfabric.FactContinuousIntegration,
		policyTreeTeamOrigin("owner-a"), contextfabric.SubjectRepository)
	policyTreeAssertTargets(t, result.Targets, "repository:"+policyTreeRepoA)
}

// An incident is admitted only when EVERY repository its edges name is one the
// caller may read: Y also touches B, so a caller granted A alone never sees it.
func TestPolicyTree_IncidentNamingAnUnauthorizedRepositoryIsDroppedWhole(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)

	all := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		policyTreeProjectOrigin(t), contextfabric.SubjectIncident)
	policyTreeAssertTargets(t, all.Targets, "incident:INC-X", "incident:INC-Y", "incident:INC-Z")

	onlyA := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{policyTreeSlugA}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		policyTreeProjectOrigin(t), contextfabric.SubjectIncident)
	policyTreeAssertTargets(t, onlyA.Targets, "incident:INC-X")
	if onlyA.Counts.AuthorizationDroppedCount == 0 {
		t.Fatal("AuthorizationDroppedCount = 0 -- the dropped incident and repository must be counted")
	}
}

// A project reaches its owning teams through ownership rows. A repository-
// restricted caller sees a team only when it may read a repository the team
// owns; a team owning none is visible to unrestricted callers alone.
func TestPolicyTree_ProjectReachesOwningTeamsUnderTheTeamVisibilityRule(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)

	all := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies, contextfabric.FactOperationalDeficiencies,
		policyTreeProjectOrigin(t), contextfabric.SubjectTeam)
	policyTreeAssertTargets(t, all.Targets, "team:bare", "team:owner-a")

	restricted := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{policyTreeSlugA}},
		contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies, contextfabric.FactOperationalDeficiencies,
		policyTreeProjectOrigin(t), contextfabric.SubjectTeam)
	policyTreeAssertTargets(t, restricted.Targets, "team:owner-a")
	if restricted.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1 (the team that owns no readable repository)", restricted.Counts.AuthorizationDroppedCount)
	}
}

// The five policies read ownership and link rows as they stand now. A
// historical request is refused, never answered with today's rows.
func TestPolicyTree_HistoricalAxisIsRefused(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)
	asOf := time.Now().UTC().Add(-24 * time.Hour)
	_, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactContinuousIntegration,
		Origins:         []contextfabric.SubjectRef{policyTreeTeamOrigin("owner-a")},
		Policy:          contextfabric.FactScopePolicyTeamRepositoryOwnershipContinuousIntegration,
		TargetKind:      contextfabric.SubjectRepository, Limit: 20,
		TimeContext: contextfabric.TimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &asOf},
	})
	if !errors.Is(err, contextfabric.ErrFactScopeAxisUnsupported) {
		t.Fatalf("err = %v, want ErrFactScopeAxisUnsupported", err)
	}
}

// A link row names a pull request by (repository, number). A link whose pull
// request does not exist is a stale link: it reaches no repository and is a
// counted missing hop, exactly as the canonical link projection skips it.
func TestPolicyTree_LinkToAMissingPullRequestReachesNoRepository(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		policyTreeRepoD, "I-native-a", uint32(99), float32(0.9), "native", "", at, policyTreeOrgID); err != nil {
		t.Fatalf("seed stale link: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		policyTreeProjectOrigin(t), contextfabric.SubjectRepository)
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %v, want none: the link names a pull request that does not exist", policyTreeLabels(result.Targets))
	}
	if result.Counts.MissingNextHopCount == 0 {
		t.Fatal("MissingNextHopCount = 0 -- a stale link must be counted, not read as an empty project")
	}
}

// The ownership join returns one row per (team, source). The limit applies to
// DISTINCT teams, so a project owned by more teams than the limit is reported
// as truncated even when every team has several ownership sources.
func TestPolicyTree_ProjectTeamFanoutIsTruncatedByDistinctTeams(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	const project = "PROJ-FAN"
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		project, policyTreeOrgID, policyTreeProvider, nil, "Fan", uint8(1), "active", "", at); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	for _, team := range []string{"fan-a", "fan-b", "fan-c"} {
		if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			team, team, "", at, policyTreeOrgID, policyTreeProvider, team, uint8(1)); err != nil {
			t.Fatalf("seed team: %v", err)
		}
		for _, source := range []string{"native", "manual"} {
			if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				policyTreeOrgID, policyTreeProvider, team, project, nil, source, at.Add(-time.Hour), nil, at); err != nil {
				t.Fatalf("seed ownership: %v", err)
			}
		}
	}
	id, omitted, err := identity.Derive(identity.KindProject, []string{policyTreeProvider, project}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: err=%v omitted=%v", err, omitted)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}}, RequirementKind: contextfabric.FactOperationalDeficiencies,
		Origins: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: id, Label: "Fan"}},
		Policy:  contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies, TargetKind: contextfabric.SubjectTeam, Limit: 2,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if !result.Counts.Truncated {
		t.Fatalf("Truncated = false with 3 owning teams and a limit of 2 (targets=%v) -- the cut was silent", policyTreeLabels(result.Targets))
	}
}

// The edge table is keyed by repository (one row for each repository,
// deployment and incident): a newer row of repository A does not supersede
// the row of repository B for the same deployment id. The incident keeps both
// edges, and an incident that names a repository the caller was never granted
// is dropped whole -- admitting it would expose that repository.
func TestPolicyTree_IncidentEdgeOfAnotherRepositoryDoesNotSupersede(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES (?,?,?,?,?,?,?,?,?)`,
		policyTreeOrgID, at, "INC-MOVED", at, at, "svc", "Moved", at, uint8(0)); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"e-m1", policyTreeOrgID, "dep-m", "INC-MOVED", policyTreeRepoB, float32(0.9), "native", "", at, at.Add(-time.Hour)); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"e-m1", policyTreeOrgID, "dep-m", "INC-MOVED", policyTreeRepoA, float32(0.9), "native", "", at, at); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{policyTreeSlugA}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		policyTreeProjectOrigin(t), contextfabric.SubjectIncident)
	for _, target := range result.Targets {
		if target.CanonicalID == "incident:INC-MOVED" {
			t.Fatalf("targets = %v, want no incident:INC-MOVED: it also has an edge in a repository the caller was not granted", policyTreeLabels(result.Targets))
		}
	}
}

// An incident deleted in its latest revision is not a target, though an
// earlier revision of it is a separate, undeleted row.
func TestPolicyTree_IncidentDeletedInItsLatestRevisionIsNotATarget(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	for _, revision := range []struct {
		version time.Time
		deleted uint8
	}{{at.Add(-time.Hour), 0}, {at, 1}} {
		if err := direct.Exec(ctx, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES (?,?,?,?,?,?,?,?,?)`,
			policyTreeOrgID, revision.version, "INC-GONE", at, revision.version, "svc", "Gone", at, revision.deleted); err != nil {
			t.Fatalf("seed incident revision: %v", err)
		}
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"e-gone", policyTreeOrgID, "dep-gone", "INC-GONE", policyTreeRepoA, float32(0.9), "native", "", at, at); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		policyTreeProjectOrigin(t), contextfabric.SubjectIncident)
	policyTreeAssertTargets(t, result.Targets, "incident:INC-X", "incident:INC-Y", "incident:INC-Z")
}

// The missing-hop count is the number of issues, not the number of groups.
func TestPolicyTree_EveryUnlinkedIssueIsCounted(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	for i := 0; i < 9; i++ {
		id := "I-extra-" + string(rune('a'+i))
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, policyTreeRepoC, policyTreeOrgID, id, "open", "", "", policyTreeProject, at); err != nil {
			t.Fatalf("seed issue: %v", err)
		}
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		policyTreeProjectOrigin(t), contextfabric.SubjectRepository)
	// 4 seeded issues + 9 extra, none linked.
	if result.Counts.MissingNextHopCount != 13 {
		t.Fatalf("MissingNextHopCount = %d, want 13 (every unlinked issue)", result.Counts.MissingNextHopCount)
	}
}

// A work item typed as a pull request is not an issue: the canonical link
// projection skips it, so its link reaches no repository here either.
func TestPolicyTree_PullRequestTypedWorkItemReachesNoRepository(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at, type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"PR-typed", policyTreeRepoC, policyTreeOrgID, "a pull request", "open", "", "", policyTreeProject, at, "pr"); err != nil {
		t.Fatalf("seed pr-typed work item: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		policyTreeRepoD, "PR-typed", uint32(7), float32(0.9), "native", "", at, policyTreeOrgID); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		policyTreeRepoD, policyTreeOrgID, uint32(7), "PR", "open", at, at, "feat/x", ""); err != nil {
		t.Fatalf("seed pull request: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		policyTreeProjectOrigin(t), contextfabric.SubjectRepository)
	for _, target := range result.Targets {
		if target.CanonicalID == "repository:"+policyTreeRepoD {
			t.Fatalf("targets = %v: a pull-request-typed work item's link reached its repository", policyTreeLabels(result.Targets))
		}
	}
}

// An edge with no deployment is not a deployment-incident edge: the canonical
// projection requires one, so such a row makes no incident reachable.
func TestPolicyTree_EdgeWithNoDeploymentReachesNoIncident(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES (?,?,?,?,?,?,?,?,?)`,
		policyTreeOrgID, at, "INC-BLANK", at, at, "svc", "Blank", at, uint8(0)); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"e-blank", policyTreeOrgID, "", "INC-BLANK", policyTreeRepoA, float32(0.9), "native", "", at, at); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := policyTreeExpand(t, ctx, expander, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		policyTreeProjectOrigin(t), contextfabric.SubjectIncident)
	for _, target := range result.Targets {
		if target.CanonicalID == "incident:INC-BLANK" {
			t.Fatalf("targets = %v: an edge with no deployment made an incident reachable", policyTreeLabels(result.Targets))
		}
	}
}
