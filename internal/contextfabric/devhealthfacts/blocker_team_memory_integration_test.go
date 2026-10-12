package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const blockerMemoryOrg = "5c1e8f2a-94d7-4b36-8a0e-2f6b9d3c7e14"

// An organization's work items that are in no blocker edge must not count
// against the team blocker reads' statement memory budget: the reads serve a
// handful of edges, so their cost has to follow the edges, not the size of
// the organization's work item table.
const blockerMemoryFillerItems = 250_000

func TestTeamBlockerFactsServeAnOrganizationWithManyWorkItemsOnRealClickHouse(t *testing.T) {
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
SELECT concat('filler-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', ?, NULL FROM numbers(?)`,
		repoUUID("repo-owned"), blockerMemoryOrg, at, project, uint64(blockerMemoryFillerItems))

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: blockerMemoryOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
	})
	if err != nil {
		cause := readFailureCause(err)
		if strings.Contains(cause, "code: 241") {
			t.Fatalf("team blocker read exceeded its statement memory budget over %d unrelated work items: %s", blockerMemoryFillerItems, cause)
		}
		t.Fatalf("ReadFacts: %v: %s", err, cause)
	}
	fields := factFor(t, result, teamSubject(teamID)).Fields
	wantInt(t, fields, "blocked_work_items_current", 2)
	wantInt(t, fields, "blocker_dependencies_current", 2)
	wantInt(t, fields, "project_blocked_work_items_current", 1)
}
