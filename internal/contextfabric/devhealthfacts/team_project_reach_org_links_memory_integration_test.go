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

const (
	orgLinksMemoryOrg         = "c4d82e17-6b09-4f3a-9a51-0e7d3b8c2f64"
	orgLinksMemoryFillerLinks = 30_000
	orgLinksMemoryMoreLinks   = 60_000
)

// The repositories a team reaches through its owned projects are read from the
// owned projects' issues only. Link rows and pull requests of other teams and
// of work items in no project must not count against the statement's memory:
// the peak of each project-reached statement, measured with the organization's
// link rows and pull requests tripled, may not grow past the slope.
func TestTeamProjectReachedStatementMemoryFollowsOwnedProjectsNotOrganizationLinksOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	org := orgLinksMemoryOrg
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const teamID, project, reachedItem = "team-owner", "proj-owner", "i-reached"
	for _, label := range []string{"repo-reached", "repo-filler"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), org, "acme/"+label, "github", at)
	}
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		teamID, teamID, at, org, "linear", uint8(1))
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		project, org, "linear", nil, project, uint8(1), "active", "", at)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		org, "linear", teamID, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
		reachedItem, repoUUID("repo-filler"), org, "title", at, project)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repoUUID("repo-reached"), org, uint32(1), "pr", "open", at, at, "feature", "")
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoUUID("repo-reached"), reachedItem, uint32(1), float32(1), "native", "", at, org)

	var from uint64
	addLinks := func(count int) {
		t.Helper()
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('filler-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?, ?)`,
			repoUUID("repo-filler"), org, at, from, uint64(count))
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1000), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?, ?)`,
			repoUUID("repo-filler"), org, at, at, from, uint64(count))
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('filler-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`,
			repoUUID("repo-filler"), at, org, from, uint64(count))
		from += uint64(count)
	}
	addLinks(orgLinksMemoryFillerLinks)

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	window := contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59))
	read := func(label string) statementMemory {
		mark := serverNowMicros(t, ctx, direct)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: window, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts at the %s size: %v: %s", label, err, readFailureCause(err))
		}
		wantInt(t, factFor(t, result, teamSubject(teamID)).Fields, "project_reached_repository_count", 1)
		return statementMemorySince(t, ctx, direct, statementFilter{Table: "team_project_ownership", QueryContains: "work_graph_issue_pr"}, mark)
	}
	read("warm-up")
	small := read("smaller")
	addLinks(orgLinksMemoryMoreLinks)
	large := read("larger")
	for hash, peak := range small {
		t.Logf("statement %d peak memory: %d bytes at %d filler links, %d at %d", hash, peak, orgLinksMemoryFillerLinks, large[hash], orgLinksMemoryFillerLinks+orgLinksMemoryMoreLinks)
	}
	assertStatementMemoryFollowsEvidence(t, ctx, direct, "team project-reached statements over organization links", small, large, 2)
}
