package devhealthfacts_test

import (
	"context"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const projectReachMemoryOrg = "b7a41c9e-2d63-4f85-a1e0-6c93d8f20b47"

// The repositories a team reaches through its owned projects' linked pull
// requests are read from the issues of those projects only. The cost of the
// reads follows the owned projects, not the size of the organization's work
// item table; work items of other teams' projects, and work items of no
// project, are never counted into the reach.
func TestTeamProjectReachedRepositoriesFollowOwnedProjectsNotOrganizationSizeOnRealClickHouse(t *testing.T) {
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
	const teamID, otherTeamID, project, otherProject = "team-owner", "team-other", "proj-owner", "proj-other"
	for _, label := range []string{"repo-reached", "repo-other-team", "repo-filler"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), projectReachMemoryOrg, "acme/"+label, "github", at)
	}
	for _, team := range []string{teamID, otherTeamID} {
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, projectReachMemoryOrg, "linear", uint8(1))
	}
	for _, owned := range []struct{ team, project string }{{teamID, project}, {otherTeamID, otherProject}} {
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			owned.project, projectReachMemoryOrg, "linear", nil, owned.project, uint8(1), "active", "", at)
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			projectReachMemoryOrg, "linear", owned.team, owned.project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	for _, item := range []struct{ id, project string }{{"i-reached", project}, {"i-weak", project}, {"i-other-team", otherProject}, {"i-no-project", ""}} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			item.id, repoUUID("repo-filler"), projectReachMemoryOrg, "title "+item.id, at, item.project)
	}
	for _, pr := range []struct {
		repo   string
		number uint32
	}{{"repo-reached", 1}, {"repo-reached", 2}, {"repo-other-team", 3}, {"repo-other-team", 4}} {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(pr.repo), projectReachMemoryOrg, pr.number, "pr", "open", at, at, "feature", "")
	}
	for _, link := range []struct {
		item, repo, tier string
		number           uint32
	}{
		{"i-reached", "repo-reached", "native", 1},
		{"i-weak", "repo-reached", "heuristic", 2},
		{"i-other-team", "repo-other-team", "native", 3},
		{"i-no-project", "repo-other-team", "native", 4},
	} {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(link.repo), link.item, link.number, float32(1), link.tier, "", at, projectReachMemoryOrg)
	}
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('filler-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?)`,
		repoUUID("repo-filler"), projectReachMemoryOrg, at, uint64(blockerMemoryFillerItems))

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	window := contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59))
	var lastMark int64
	read := func() (statementMemory, contextfabric.FactProviderResult, error) {
		mark := serverNowMicros(t, ctx, direct)
		lastMark = mark
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: projectReachMemoryOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: window, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
		})
		return statementMemorySince(t, ctx, direct, statementFilter{Table: "team_project_ownership", QueryContains: "work_graph_issue_pr"}, mark), result, err
	}
	if _, _, err := read(); err != nil {
		t.Fatalf("warm-up ReadFacts: %v: %s", err, readFailureCause(err))
	}
	small, smallResult, smallErr := read()
	smallBytes := projectReachedReadBytes(t, ctx, direct, lastMark)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('filler-more-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?)`,
		repoUUID("repo-filler"), projectReachMemoryOrg, at, uint64(blockerMemoryMoreFillerItems))
	large, largeResult, largeErr := read()
	largeBytes := projectReachedReadBytes(t, ctx, direct, lastMark)
	t.Logf("read_bytes of the project-reached statements: %d at the smaller size, %d at the larger", smallBytes, largeBytes)
	// The reach read and the linked pull request read run on every read.
	assertStatementMemoryFollowsEvidence(t, ctx, direct, "team project-reached repository statements", small, large, 2)
	for _, read := range []struct {
		result contextfabric.FactProviderResult
		err    error
	}{{smallResult, smallErr}, {largeResult, largeErr}} {
		if read.err != nil {
			t.Fatalf("ReadFacts: %v: %s", read.err, readFailureCause(read.err))
		}
		fields := factFor(t, read.result, teamSubject(teamID)).Fields
		wantInt(t, fields, "project_reached_repository_count", 1)
		wantInt(t, fields, "pull_requests_opened_window", 2)
		rows := wantTableRows(t, fields, "repository_breakdown")
		if len(rows) != 1 {
			t.Fatalf("repository_breakdown has %d rows, want only the owned project's reached repository", len(rows))
		}
		if _, ok := rows[repoUUID("repo-reached")]; !ok {
			t.Fatalf("repository_breakdown lacks the reached repository: %v", rows)
		}
		tier := rows[repoUUID("repo-reached")]["link_tier"]
		if tier.String == nil || *tier.String != "heuristic" {
			t.Fatalf("link_tier = %v, want the weakest tier heuristic", tier)
		}
	}
}

// projectReachedReadBytes is the peak read_bytes of the SELECTs over
// team_project_ownership and work_graph_issue_pr logged after the mark.
func projectReachedReadBytes(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, markMicros int64) uint64 {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	var peak uint64
	if err := direct.QueryRow(ctx, `SELECT max(read_bytes) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.team_project_ownership')) AND position(query, 'work_graph_issue_pr') > 0`, markMicros).Scan(&peak); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	return peak
}
