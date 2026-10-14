package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const projectReachTypeOrg = "e41a7c05-8b2d-4936-b0f1-5d7c2a9e8613"

// A work item reaches repositories as the issue it is in the owned project:
// the type of its row in that project decides, whatever a row of the same id
// in another project says.
func TestTeamProjectReachUsesTheOwnedProjectRowTypeOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_relations_read", "work_item_dependency_first_seen", "work_graph_issue_pr",
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
	const teamID, owned, other = "team-owner", "proj-owned", "proj-other"
	for _, label := range []string{"repo-issue-here", "repo-pr-here", "repo-x"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), projectReachTypeOrg, "acme/"+label, "github", at)
	}
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		teamID, teamID, at, projectReachTypeOrg, "linear", uint8(1))
	for _, project := range []string{owned, other} {
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, projectReachTypeOrg, "linear", nil, project, uint8(1), "active", "", at)
	}
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		projectReachTypeOrg, "linear", teamID, owned, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	// Two rows per id (different repo_id keeps both): one in the owned project
	// and one in another project, of opposite types.
	row := func(id, repoLabel, project, itemType string) {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, type) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL, ?)`,
			id, repoUUID(repoLabel), projectReachTypeOrg, "title "+id, at, project, itemType)
	}
	row("i-issue-here", "repo-x", owned, "issue")
	row("i-issue-here", "repo-pr-here", other, "pr")
	row("i-pr-here", "repo-x", owned, "pr")
	row("i-pr-here", "repo-issue-here", other, "issue")
	for _, link := range []struct {
		item, repo string
		number     uint32
	}{{"i-issue-here", "repo-issue-here", 1}, {"i-pr-here", "repo-pr-here", 2}} {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(link.repo), projectReachTypeOrg, link.number, "pr", "open", at, at, "feature", "")
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(link.repo), link.item, link.number, float32(1), "native", "", at, projectReachTypeOrg)
	}

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: projectReachTypeOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59)), Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
	}
	fields := factFor(t, result, teamSubject(teamID)).Fields
	wantInt(t, fields, "project_reached_repository_count", 1)
	rows := wantTableRows(t, fields, "repository_breakdown")
	if _, ok := rows[repoUUID("repo-issue-here")]; !ok || len(rows) != 1 {
		t.Fatalf("repository_breakdown = %v, want only the repository of the item that is an issue in the owned project", rows)
	}
}
