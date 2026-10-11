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
	entityTreeOrgID    = "6e7a3b1c-9f40-4c6e-8d52-3a1f7c2b9d10"
	entityTreeRepoA    = "a1000000-0000-4000-8000-000000000001"
	entityTreeRepoB    = "b2000000-0000-4000-8000-000000000002"
	entityTreeRepoC    = "c3000000-0000-4000-8000-000000000003"
	entityTreeRepoD    = "d4000000-0000-4000-8000-000000000004"
	entityTreeSlugA    = "acme/repo-a"
	entityTreeSlugB    = "acme/repo-b"
	entityTreeSlugC    = "acme/repo-c"
	entityTreeSlugD    = "acme/repo-d"
	entityTreeProject  = "PROJ-TREE"
	entityTreeProvider = "linear"
)

type entityTreeExec interface {
	Exec(ctx context.Context, query string, args ...any) error
}

func seedEntityTree(t *testing.T, ctx context.Context, direct entityTreeExec, at time.Time) {
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
	for _, repo := range [][2]string{{entityTreeRepoA, entityTreeSlugA}, {entityTreeRepoB, entityTreeSlugB}, {entityTreeRepoC, entityTreeSlugC}, {entityTreeRepoD, entityTreeSlugD}} {
		exec("repo "+repo[1], `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo[0], entityTreeOrgID, repo[1], "github", at)
	}
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		entityTreeProject, entityTreeOrgID, entityTreeProvider, nil, "Tree", uint8(1), "active", "", at)

	// Issues of the project. Their OWN repository column names repo C for
	// every one of them: the tree must never reach C through it.
	issue := func(id string) {
		exec("issue "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, entityTreeRepoC, entityTreeOrgID, "issue "+id, "open", "", "", entityTreeProject, at)
	}
	// link writes the link row AND the pull request it names: a link of
	// record is read only with its pull request, as the canonical link
	// projection reads it.
	link := func(label, issueID, repoID string, pr uint32, provenance string) {
		exec(label, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, issueID, pr, float32(0.9), provenance, "", at, entityTreeOrgID)
		exec(label+" pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
			repoID, entityTreeOrgID, pr, "PR", "open", at, at, "feat/x", "")
	}
	issue("I-native-a")
	link("link native to A", "I-native-a", entityTreeRepoA, 1, "native")
	issue("I-heuristic-b")
	link("link heuristic to B", "I-heuristic-b", entityTreeRepoB, 2, "heuristic")
	// A second, stronger link to repo B must win B's tier.
	issue("I-text-b")
	link("link explicit_text to B", "I-text-b", entityTreeRepoB, 3, "explicit_text")
	issue("I-unlinked")

	// Teams. owner-a owns repo A and the project; owner-b owns repo B only;
	// bare owns the project and owns no repository at all.
	for _, team := range []string{"owner-a", "owner-b", "bare"} {
		exec("team "+team, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			team, team, "", at, entityTreeOrgID, entityTreeProvider, team, uint8(1))
	}
	repoOwnership := func(label, team, repoID, slug string, validTo any) {
		exec(label, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			entityTreeOrgID, "github", team, repoID, slug, "exact", "native", uint8(1), uint16(100), int32(0), at.Add(-time.Hour), validTo, at)
	}
	repoOwnership("owner-a owns A", "owner-a", entityTreeRepoA, entityTreeSlugA, nil)
	repoOwnership("owner-b owns B", "owner-b", entityTreeRepoB, entityTreeSlugB, nil)
	// An ended ownership: repo C was owned by owner-a and no longer is.
	repoOwnership("owner-a owned C until an hour ago", "owner-a", entityTreeRepoC, entityTreeSlugC, at.Add(-time.Minute))
	for _, team := range []string{"owner-a", "bare"} {
		exec("project ownership "+team, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			entityTreeOrgID, entityTreeProvider, team, entityTreeProject, nil, "native", at.Add(-time.Hour), nil, at)
	}

	// Incidents. X touches only repo A; Y touches A and B; Z touches only B.
	incident := func(id string) {
		exec("incident "+id, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES (?,?,?,?,?,?,?,?,?)`,
			entityTreeOrgID, at, id, at, at, "svc", "Incident "+id, at, uint8(0))
	}
	edge := func(id, incidentID, repoID string) {
		exec("edge "+id, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, entityTreeOrgID, "dep-"+id, incidentID, repoID, float32(0.9), "native", "", at, at)
	}
	incident("INC-X")
	incident("INC-Y")
	incident("INC-Z")
	edge("e1", "INC-X", entityTreeRepoA)
	edge("e2", "INC-Y", entityTreeRepoA)
	edge("e3", "INC-Y", entityTreeRepoB)
	edge("e4", "INC-Z", entityTreeRepoB)
}

func entityTreeProjectOrigin(t *testing.T) contextfabric.SubjectRef {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindProject, []string{entityTreeProvider, entityTreeProject}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: err=%v omitted=%v", err, omitted)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: id, Label: "Tree"}
}

func entityTreeTeamOrigin(team string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + team, Label: team}
}

func entityTreeExpand(t *testing.T, ctx context.Context, expander *devhealthfacts.ScopeExpander, principal storage.Principal, policy contextfabric.FactScopePolicy, requirement contextfabric.FactKind, origin contextfabric.SubjectRef, target contextfabric.SubjectKind) contextfabric.FactScopeExpansionResult {
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

func entityTreeLabels(targets []contextfabric.SubjectRef) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.CanonicalID)
	}
	sort.Strings(out)
	return out
}

func entityTreeAssertTargets(t *testing.T, got []contextfabric.SubjectRef, want ...string) {
	t.Helper()
	sort.Strings(want)
	have := entityTreeLabels(got)
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
func TestEntityTree_ProjectReachesRepositoriesOnlyThroughLinkedPullRequests(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)
	unrestricted := storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}}

	result := entityTreeExpand(t, ctx, expander, unrestricted,
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		entityTreeProjectOrigin(t), contextfabric.SubjectRepository)
	entityTreeAssertTargets(t, result.Targets, "repository:"+entityTreeRepoA, "repository:"+entityTreeRepoB)
	if result.Counts.MissingNextHopCount != 1 {
		t.Fatalf("MissingNextHopCount = %d, want 1 (the project's unlinked issue)", result.Counts.MissingNextHopCount)
	}
	// The strongest tier among a repository's links names its provenance.
	for _, target := range result.Targets {
		key := contextfabric.FactSubjectKey(target)
		switch target.CanonicalID {
		case "repository:" + entityTreeRepoA:
			if got := result.TargetAttributionSource[key]; got != "native" {
				t.Fatalf("repo A tier = %q, want native", got)
			}
		case "repository:" + entityTreeRepoB:
			if got := result.TargetAttributionSource[key]; got != "explicit_text" {
				t.Fatalf("repo B tier = %q, want explicit_text (outranks heuristic)", got)
			}
		}
	}

	// The same chain feeds deployments.
	deployments := entityTreeExpand(t, ctx, expander, unrestricted,
		contextfabric.FactScopePolicyProjectIssuePullRequestDeployments, contextfabric.FactDeployments,
		entityTreeProjectOrigin(t), contextfabric.SubjectRepository)
	entityTreeAssertTargets(t, deployments.Targets, "repository:"+entityTreeRepoA, "repository:"+entityTreeRepoB)

	// A repository-restricted caller loses B and the loss is counted.
	restricted := storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{entityTreeSlugA}}
	narrowed := entityTreeExpand(t, ctx, expander, restricted,
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		entityTreeProjectOrigin(t), contextfabric.SubjectRepository)
	entityTreeAssertTargets(t, narrowed.Targets, "repository:"+entityTreeRepoA)
	if narrowed.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1", narrowed.Counts.AuthorizationDroppedCount)
	}
}

// A project whose issues carry no link row reaches nothing, and says why.
func TestEntityTree_ProjectWithOnlyUnlinkedIssuesIsACountedMissingHop(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		entityTreeProjectOrigin(t), contextfabric.SubjectRepository)
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %v, want none: the issue's own repository column is not a link", entityTreeLabels(result.Targets))
	}
	if result.Counts.MissingNextHopCount == 0 {
		t.Fatal("MissingNextHopCount = 0 -- issues without a pull request link must be counted, never read as an empty project")
	}
}

// A link row whose provenance is outside ops' three tiers is counted and never
// admitted.
func TestEntityTree_UnknownLinkProvenanceIsCountedNotAdmitted(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entityTreeRepoA, "I-native-a", uint32(1), float32(0.9), "guessed", "", at, entityTreeOrgID); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		entityTreeRepoA, entityTreeOrgID, uint32(1), "PR", "open", at, at, "feat/x", ""); err != nil {
		t.Fatalf("seed pull request: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		entityTreeProjectOrigin(t), contextfabric.SubjectRepository)
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %v, want none for an unrecognised provenance", entityTreeLabels(result.Targets))
	}
	if result.Counts.UnknownAttributionSourceCount != 1 {
		t.Fatalf("UnknownAttributionSourceCount = %d, want 1", result.Counts.UnknownAttributionSourceCount)
	}
}

// A team reaches repositories through ownership rows it holds NOW. An ended
// ownership (repo C) and another team's repository (B) are not reached.
func TestEntityTree_TeamReachesOnlyCurrentlyOwnedRepositories(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)
	result := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyTeamRepositoryOwnershipContinuousIntegration, contextfabric.FactContinuousIntegration,
		entityTreeTeamOrigin("owner-a"), contextfabric.SubjectRepository)
	entityTreeAssertTargets(t, result.Targets, "repository:"+entityTreeRepoA)
}

// An incident is admitted only when EVERY repository its edges name is one the
// caller may read: Y also touches B, so a caller granted A alone never sees it.
func TestEntityTree_IncidentNamingAnUnauthorizedRepositoryIsDroppedWhole(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)

	all := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		entityTreeProjectOrigin(t), contextfabric.SubjectIncident)
	entityTreeAssertTargets(t, all.Targets, "incident:INC-X", "incident:INC-Y", "incident:INC-Z")

	onlyA := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{entityTreeSlugA}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		entityTreeProjectOrigin(t), contextfabric.SubjectIncident)
	entityTreeAssertTargets(t, onlyA.Targets, "incident:INC-X")
	if onlyA.Counts.AuthorizationDroppedCount == 0 {
		t.Fatal("AuthorizationDroppedCount = 0 -- the dropped incident and repository must be counted")
	}
}

// A project reaches its owning teams through ownership rows. A repository-
// restricted caller sees a team only when it may read a repository the team
// owns; a team owning none is visible to unrestricted callers alone.
func TestEntityTree_ProjectReachesOwningTeamsUnderTheTeamVisibilityRule(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)

	all := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies, contextfabric.FactOperationalDeficiencies,
		entityTreeProjectOrigin(t), contextfabric.SubjectTeam)
	entityTreeAssertTargets(t, all.Targets, "team:bare", "team:owner-a")

	restricted := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{entityTreeSlugA}},
		contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies, contextfabric.FactOperationalDeficiencies,
		entityTreeProjectOrigin(t), contextfabric.SubjectTeam)
	entityTreeAssertTargets(t, restricted.Targets, "team:owner-a")
	if restricted.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1 (the team that owns no readable repository)", restricted.Counts.AuthorizationDroppedCount)
	}
}

// The five policies read ownership and link rows as they stand now. A
// historical request is refused, never answered with today's rows.
func TestEntityTree_HistoricalAxisIsRefused(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedEntityTree(t, ctx, direct, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)
	asOf := time.Now().UTC().Add(-24 * time.Hour)
	_, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactContinuousIntegration,
		Origins:         []contextfabric.SubjectRef{entityTreeTeamOrigin("owner-a")},
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
func TestEntityTree_LinkToAMissingPullRequestReachesNoRepository(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entityTreeRepoD, "I-native-a", uint32(99), float32(0.9), "native", "", at, entityTreeOrgID); err != nil {
		t.Fatalf("seed stale link: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		entityTreeProjectOrigin(t), contextfabric.SubjectRepository)
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %v, want none: the link names a pull request that does not exist", entityTreeLabels(result.Targets))
	}
	if result.Counts.MissingNextHopCount == 0 {
		t.Fatal("MissingNextHopCount = 0 -- a stale link must be counted, not read as an empty project")
	}
}

// The ownership join returns one row per (team, source). The limit applies to
// DISTINCT teams, so a project owned by more teams than the limit is reported
// as truncated even when every team has several ownership sources.
func TestEntityTree_ProjectTeamFanoutIsTruncatedByDistinctTeams(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	const project = "PROJ-FAN"
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		project, entityTreeOrgID, entityTreeProvider, nil, "Fan", uint8(1), "active", "", at); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	for _, team := range []string{"fan-a", "fan-b", "fan-c"} {
		if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			team, team, "", at, entityTreeOrgID, entityTreeProvider, team, uint8(1)); err != nil {
			t.Fatalf("seed team: %v", err)
		}
		for _, source := range []string{"native", "manual"} {
			if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				entityTreeOrgID, entityTreeProvider, team, project, nil, source, at.Add(-time.Hour), nil, at); err != nil {
				t.Fatalf("seed ownership: %v", err)
			}
		}
	}
	id, omitted, err := identity.Derive(identity.KindProject, []string{entityTreeProvider, project}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: err=%v omitted=%v", err, omitted)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}}, RequirementKind: contextfabric.FactOperationalDeficiencies,
		Origins: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: id, Label: "Fan"}},
		Policy:  contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies, TargetKind: contextfabric.SubjectTeam, Limit: 2,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if !result.Counts.Truncated {
		t.Fatalf("Truncated = false with 3 owning teams and a limit of 2 (targets=%v) -- the cut was silent", entityTreeLabels(result.Targets))
	}
}

// An edge superseded by a newer row of the same key reads as the newer one:
// the incident whose edge moved from an unauthorized repository to an
// authorized one is admitted, not dropped for the stale row.
func TestEntityTree_SupersededIncidentEdgeReadsAsTheLatest(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES (?,?,?,?,?,?,?,?,?)`,
		entityTreeOrgID, at, "INC-MOVED", at, at, "svc", "Moved", at, uint8(0)); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"e-m1", entityTreeOrgID, "dep-m", "INC-MOVED", entityTreeRepoB, float32(0.9), "native", "", at, at.Add(-time.Hour)); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"e-m1", entityTreeOrgID, "dep-m", "INC-MOVED", entityTreeRepoA, float32(0.9), "native", "", at, at); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{entityTreeSlugA}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		entityTreeProjectOrigin(t), contextfabric.SubjectIncident)
	found := false
	for _, target := range result.Targets {
		if target.CanonicalID == "incident:INC-MOVED" {
			found = true
		}
	}
	if !found {
		t.Fatalf("targets = %v, want incident:INC-MOVED: its latest edge names an authorized repository", entityTreeLabels(result.Targets))
	}
}

// The missing-hop count is the number of issues, not the number of groups.
func TestEntityTree_EveryUnlinkedIssueIsCounted(t *testing.T) {
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
			id, entityTreeRepoC, entityTreeOrgID, id, "open", "", "", entityTreeProject, at); err != nil {
			t.Fatalf("seed issue: %v", err)
		}
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		entityTreeProjectOrigin(t), contextfabric.SubjectRepository)
	// 4 seeded issues + 9 extra, none linked.
	if result.Counts.MissingNextHopCount != 13 {
		t.Fatalf("MissingNextHopCount = %d, want 13 (every unlinked issue)", result.Counts.MissingNextHopCount)
	}
}

// A work item typed as a pull request is not an issue: the canonical link
// projection skips it, so its link reaches no repository here either.
func TestEntityTree_PullRequestTypedWorkItemReachesNoRepository(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `TRUNCATE TABLE work_graph_issue_pr`); err != nil {
		t.Fatalf("truncate links: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at, type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"PR-typed", entityTreeRepoC, entityTreeOrgID, "a pull request", "open", "", "", entityTreeProject, at, "pr"); err != nil {
		t.Fatalf("seed pr-typed work item: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entityTreeRepoD, "PR-typed", uint32(7), float32(0.9), "native", "", at, entityTreeOrgID); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		entityTreeRepoD, entityTreeOrgID, uint32(7), "PR", "open", at, at, "feat/x", ""); err != nil {
		t.Fatalf("seed pull request: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration, contextfabric.FactContinuousIntegration,
		entityTreeProjectOrigin(t), contextfabric.SubjectRepository)
	for _, target := range result.Targets {
		if target.CanonicalID == "repository:"+entityTreeRepoD {
			t.Fatalf("targets = %v: a pull-request-typed work item's link reached its repository", entityTreeLabels(result.Targets))
		}
	}
}

// An edge with no deployment is not a deployment-incident edge: the canonical
// projection requires one, so such a row makes no incident reachable.
func TestEntityTree_EdgeWithNoDeploymentReachesNoIncident(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	if err := direct.Exec(ctx, `INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES (?,?,?,?,?,?,?,?,?)`,
		entityTreeOrgID, at, "INC-BLANK", at, at, "svc", "Blank", at, uint8(0)); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"e-blank", entityTreeOrgID, "", "INC-BLANK", entityTreeRepoA, float32(0.9), "native", "", at, at); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	result := entityTreeExpand(t, ctx, expander, storage.Principal{OrgID: entityTreeOrgID, RepositoryScopes: []string{"*"}},
		contextfabric.FactScopePolicyProjectIssuePullRequestIncidents, contextfabric.FactIncidents,
		entityTreeProjectOrigin(t), contextfabric.SubjectIncident)
	for _, target := range result.Targets {
		if target.CanonicalID == "incident:INC-BLANK" {
			t.Fatalf("targets = %v: an edge with no deployment made an incident reachable", entityTreeLabels(result.Targets))
		}
	}
}
