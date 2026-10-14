package devhealthfacts_test

// The team reads that page under the ClickHouse client's max_result_rows
// (1000, result_overflow_mode throw), executed against a real ClickHouse for
// the two arms the ownership-bound test does not reach: repositories reached
// through an owned project's linked pull requests (and the linked pull request
// counts read per reached repository), and the scope expander's owned
// repository slugs. Each seeds more than one page and more than the client cap.
// The container is mandatory: a missing one fails the test, never skips it.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	pagedReachOrg   = "0b8c41d7-52e6-4f19-a7c3-91d2e6f40a58"
	pagedSlugOrg    = "c47e19a0-83b2-4d65-9f01-2a5b7d8e6c13"
	pagedRepoCount  = 1100
	pagedUUIDPrefix = "00000000-0000-4000-8000-"
)

func pagedUUID(column string) string {
	return `toUUID(concat('` + pagedUUIDPrefix + `', leftPad(toString(` + column + `), 12, '0')))`
}

func TestTeamProjectReachedRepositoriesPageUnderTheClientRowCapOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_relations_read", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership",
		"work_item_team_attributions", "deploy_metrics_daily",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 29, 0, 0, 0)
	const when = `toDateTime64('2026-09-29 00:00:00', 3, 'UTC')`
	n := strconv.Itoa(pagedRepoCount)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v\n%s", label, err, statement)
		}
	}
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-wide", pagedReachOrg, "linear", nil, "wide", uint8(1), "active", "", at)
	exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-wide", "team-wide", at, pagedReachOrg, "linear", uint8(1))
	exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		pagedReachOrg, "linear", "team-wide", "proj-wide", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	exec("repos", `INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT `+pagedUUID("number")+`, '`+pagedReachOrg+`', concat('acme/wide-', toString(number)), 'github', `+when+` FROM numbers(`+n+`)`)
	exec("issues", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) SELECT concat('wi-', toString(number)), `+pagedUUID("0")+`, '`+pagedReachOrg+`', 'issue', 'open', '', `+when+`, '', 'linear', 'proj-wide', NULL FROM numbers(`+n+`)`)
	exec("links", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) SELECT `+pagedUUID("number")+`, concat('wi-', toString(number)), toUInt32(1), toFloat32(1), 'native', '', `+when+`, '`+pagedReachOrg+`' FROM numbers(`+n+`)`)
	exec("pull requests", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) SELECT `+pagedUUID("number")+`, '`+pagedReachOrg+`', toUInt32(1), 'pr', 'open', `+when+`, `+when+`, NULL, NULL, 'feature', '' FROM numbers(`+n+`)`)

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: pagedReachOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59)), Kind: contextfabric.FactPullRequests,
		Subjects: []contextfabric.SubjectRef{teamSubject("team-wide")},
	})
	if err != nil {
		t.Fatalf("a team reaching %d repositories through one project must be served: %v", pagedRepoCount, err)
	}
	fields := factFor(t, result, teamSubject("team-wide")).Fields
	wantInt(t, fields, "project_reached_repository_count", pagedRepoCount)
	wantInt(t, fields, "pull_requests_opened_window", pagedRepoCount)
}

func TestScopeExpanderTeamSlugsPageUnderTheClientRowCapOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	for _, statement := range devhealthschema.DDL("teams", "repos", "projects", "team_project_ownership", "team_repo_ownership") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := time.Now().UTC()
	const when = `toDateTime64('2026-01-01 00:00:00', 3, 'UTC')`
	n := strconv.Itoa(pagedRepoCount)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v\n%s", label, err, statement)
		}
	}
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"PROJ-SLUGS", pagedSlugOrg, "linear", nil, "slugs", uint8(1), "active", "", at)
	exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"team-slugs", "team-slugs", "", at, pagedSlugOrg, "linear", "team-slugs", uint8(1))
	exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		pagedSlugOrg, "linear", "team-slugs", "PROJ-SLUGS", nil, "native", at.Add(-time.Hour), nil, at)
	exec("repos", `INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT `+pagedUUID("number")+`, '`+pagedSlugOrg+`', concat('acme/slug-', toString(number)), 'github', now64(3) FROM numbers(`+n+`)`)
	exec("repository ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) SELECT '`+pagedSlugOrg+`', 'github', 'team-slugs', `+pagedUUID("number")+`, concat('acme/slug-', toString(number)), 'exact', 'native', 1, 100, 0, `+when+`, NULL, now64(3) FROM numbers(`+n+`)`)

	id, omitted, err := identity.Derive(identity.KindProject, []string{"linear", "PROJ-SLUGS"}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: err=%v omitted=%v", err, omitted)
	}
	// The team is admitted to a repository-restricted caller only when the
	// caller may read a repository the team owns. "acme/slug-999" sorts last
	// of the 1100 slugs, so it is on the third page: the team is admitted only
	// if every page's slugs reach the authorization rule.
	expand := func(scopes []string) contextfabric.FactScopeExpansionResult {
		t.Helper()
		result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal: storage.Principal{OrgID: pagedSlugOrg, RepositoryScopes: scopes}, RequirementKind: contextfabric.FactOperationalDeficiencies,
			Origins: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: id, Label: "Slugs"}},
			Policy:  contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies, TargetKind: contextfabric.SubjectTeam, Limit: 10,
		})
		if err != nil {
			t.Fatalf("a team owning %d repositories must expand: %v", pagedRepoCount, err)
		}
		return result
	}
	if got := expand([]string{"acme/slug-999"}).Targets; len(got) != 1 {
		t.Fatalf("a caller who reads only the last page's repository: targets = %d (%v), want the owning team", len(got), got)
	}
	if got := expand([]string{"acme/not-owned"}).Targets; len(got) != 0 {
		t.Fatalf("a caller who reads none of the team's repositories: targets = %d (%v), want none", len(got), got)
	}
	if got := expand([]string{"*"}).Targets; len(got) != 1 {
		t.Fatalf("an unrestricted caller: targets = %d (%v), want the owning team", len(got), got)
	}
}
