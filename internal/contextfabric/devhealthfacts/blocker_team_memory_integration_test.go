package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const blockerMemoryOrg = "5c1e8f2a-94d7-4b36-8a0e-2f6b9d3c7e14"

// An organization's work items that are in no blocker edge must not count
// against the team blocker reads' statement memory budget: the reads serve a
// handful of edges, so their cost has to follow the edges, not the size of
// the organization's work item table. The query client runs under the hosted
// runtime's per-statement read budget, as acr-api does.
const (
	blockerMemoryFillerItems     = 250_000
	blockerMemoryMoreFillerItems = 375_000
)

func TestTeamBlockerFactsServeAnOrganizationWithManyWorkItemsOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const teamID, project = "team-owner", "proj-owner"
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-owned"), blockerMemoryOrg, "acme/repo-owned", "github", at)
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		teamID, teamID, at, blockerMemoryOrg, "linear", uint8(1))
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		blockerMemoryOrg, "linear", teamID, repoUUID("repo-owned"), "acme/repo-owned", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		project, blockerMemoryOrg, "linear", nil, project, uint8(1), "active", "", at)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		blockerMemoryOrg, "linear", teamID, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	for _, item := range []struct{ id, project string }{{"blocker-open", ""}, {"blocked-repo", ""}, {"blocked-project", project}} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			item.id, repoUUID("repo-owned"), blockerMemoryOrg, "title "+item.id, at, item.project)
	}
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repoUUID("repo-owned"), blockerMemoryOrg, uint32(1), "pr", "open", at, at, "feature", "")
	for _, blocked := range []string{"blocked-repo", "blocked-project"} {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID("repo-owned"), blocked, uint32(1), float32(1), "native", "", at, blockerMemoryOrg)
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
			blockerMemoryOrg, "blocker-open", blocked, "blocks", at)
	}
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('filler-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?)`,
		repoUUID("repo-owned"), blockerMemoryOrg, at, uint64(blockerMemoryFillerItems))

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	read := func() (statementMemory, contextfabric.FactProviderResult, error) {
		mark := serverNowMicros(t, ctx, direct)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: blockerMemoryOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
		})
		return statementMemorySince(t, ctx, direct, "work_item_dependencies", mark), result, err
	}
	small, smallResult, smallErr := read()
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('filler-more-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?)`,
		repoUUID("repo-owned"), blockerMemoryOrg, at, uint64(blockerMemoryMoreFillerItems))
	large, largeResult, largeErr := read()
	// The roll-up, the totals and the unlinked count run on every read.
	assertStatementMemoryFollowsEvidence(t, ctx, direct, "team blocker statements", small, large, 3)
	for _, read := range []struct {
		result contextfabric.FactProviderResult
		err    error
	}{{smallResult, smallErr}, {largeResult, largeErr}} {
		if read.err != nil {
			t.Fatalf("ReadFacts: %v: %s", read.err, readFailureCause(read.err))
		}
		fields := factFor(t, read.result, teamSubject(teamID)).Fields
		wantInt(t, fields, "blocked_work_items_current", 2)
		wantInt(t, fields, "blocker_dependencies_current", 2)
		wantInt(t, fields, "project_blocked_work_items_current", 1)
	}
}

const blockerSiblingOrg = "8d2f6a19-3c4e-4b71-9e05-7a1c5b3d9f28"

// A blocker edge counts only when both of its work items are authorized, and
// an id is authorized only when every stored row carrying it is: an endpoint
// with a second row in a repository outside the caller's scope is refused, and
// so is its edge, however the read narrows the rows it looks at.
func TestTeamBlockerFactsRefuseAnEndpointWithAnUnauthorizedSiblingRowOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const teamID = "team-owner"
	for _, repo := range []string{"repo-owned", "repo-secret"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repo), blockerSiblingOrg, "acme/"+repo, "github", at)
	}
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		blockerSiblingOrg, "github", teamID, repoUUID("repo-owned"), "acme/repo-owned", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repoUUID("repo-owned"), blockerSiblingOrg, uint32(1), "pr", "open", at, at, "feature", "")
	for _, row := range []struct{ id, repo string }{
		{"blocker-open", "repo-owned"}, {"blocked-kept", "repo-owned"},
		{"blocker-shared", "repo-owned"}, {"blocker-shared", "repo-secret"}, {"blocked-refused", "repo-owned"},
	} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'github', '', NULL)`,
			row.id, repoUUID(row.repo), blockerSiblingOrg, "title "+row.id, at)
	}
	for _, edge := range []struct{ source, target string }{{"blocker-open", "blocked-kept"}, {"blocker-shared", "blocked-refused"}} {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID("repo-owned"), edge.target, uint32(1), float32(1), "native", "", at, blockerSiblingOrg)
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
			blockerSiblingOrg, edge.source, edge.target, "blocks", at)
	}

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: blockerSiblingOrg, RepositoryScopes: []string{"acme/repo-owned"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
	}
	fields := factFor(t, result, teamSubject(teamID)).Fields
	wantInt(t, fields, "blocked_work_items_current", 1)
	wantInt(t, fields, "blocker_dependencies_current", 1)
}
