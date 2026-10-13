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

const (
	orgLinksMemoryOrg          = "c4d82e17-6b09-4f3a-9a51-0e7d3b8c2f64"
	ownershipHistoryProjectOrg = "d1f6a3b8-7e24-4c90-8b35-4a2e9c6d1f07"
	ownershipHistoryRepoOrg    = "e8b25c70-1a93-4d6f-b042-7c3f8a9e5d16"
	orgLinksMemoryFillerLinks  = 30_000
	orgLinksMemoryMoreLinks    = 60_000
	ownershipHistorySmall      = 100
	ownershipHistoryLarge      = 1_000
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
	t.Run("organization links", func(t *testing.T) {
		var from uint64
		measureTeamReachAxis(t, ctx, query, direct, orgLinksMemoryOrg, func(exec func(string, ...any), org string, count int, first bool) {
			n := orgLinksMemoryFillerLinks
			if !first {
				n = orgLinksMemoryMoreLinks
			}
			at := ts(2026, 9, 29, 0, 0, 0)
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('filler-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?, ?)`,
				repoUUID("repo-filler"), org, at, from, uint64(n))
			exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1000), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?, ?)`,
				repoUUID("repo-filler"), org, at, at, from, uint64(n))
			exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('filler-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`,
				repoUUID("repo-filler"), at, org, from, uint64(n))
			from += uint64(n)
		}, "organization links", orgLinksMemoryFillerLinks, orgLinksMemoryFillerLinks+orgLinksMemoryMoreLinks)
	})
	t.Run("project ownership history", func(t *testing.T) {
		var versions uint64
		measureTeamReachAxis(t, ctx, query, direct, ownershipHistoryProjectOrg, func(exec func(string, ...any), org string, count int, first bool) {
			n := ownershipHistorySmall
			if !first {
				n = ownershipHistoryLarge - ownershipHistorySmall
			}
			exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'linear', 'team-owner', 'proj-owner', NULL, 'native', addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ?) * 3600),
  addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ? + 1) * 3600), toDateTime64('2026-09-29 00:00:00', 3, 'UTC') FROM numbers(?)`,
				org, versions, versions, uint64(n))
			versions += uint64(n)
		}, "project ownership history versions", ownershipHistorySmall, ownershipHistoryLarge)
	})
	t.Run("repository ownership history", func(t *testing.T) {
		var versions uint64
		measureTeamReachAxis(t, ctx, query, direct, ownershipHistoryRepoOrg, func(exec func(string, ...any), org string, count int, first bool) {
			n := ownershipHistorySmall
			if !first {
				n = ownershipHistoryLarge - ownershipHistorySmall
			}
			exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'linear', 'team-owner', ?, 'acme/repo-filler', 'exact', 'native', 1, 100, 0, addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ?) * 3600),
  addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ? + 1) * 3600), toDateTime64('2026-09-29 00:00:00', 3, 'UTC') FROM numbers(?)`,
				org, repoUUID("repo-filler"), versions, versions, uint64(n))
			versions += uint64(n)
		}, "repository ownership history versions", ownershipHistorySmall, ownershipHistoryLarge)
	})
}

// measureTeamReachAxis seeds one team that reaches one repository through one
// owned project, grows the axis grow names from the smaller to the larger
// size, and applies the slope guard to every statement the team pull request
// read runs over team_project_ownership and team_repo_ownership.
func measureTeamReachAxis(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, direct clickhousedriver.Conn, org string,
	grow func(exec func(string, ...any), org string, count int, first bool), axis string, smallSize, largeSize int) {
	t.Helper()
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

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	window := contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59))
	filters := []statementFilter{
		{Table: "team_project_ownership", QueryContains: "work_graph_issue_pr"},
		{Table: "team_project_ownership", QueryContains: "team_project_ownership"},
		{Table: "team_repo_ownership", QueryContains: "team_repo_ownership"},
	}
	read := func(label string) []statementMemory {
		mark := serverNowMicros(t, ctx, direct)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: window, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts at the %s size of %s: %v: %s", label, axis, err, readFailureCause(err))
		}
		wantInt(t, factFor(t, result, teamSubject(teamID)).Fields, "project_reached_repository_count", 1)
		var out []statementMemory
		for _, filter := range filters {
			out = append(out, statementMemorySince(t, ctx, direct, filter, mark))
		}
		return out
	}
	grow(exec, org, smallSize, true)
	read("warm-up")
	small := read("smaller")
	grow(exec, org, largeSize, false)
	large := read("larger")
	for i := range filters {
		for hash, peak := range small[i] {
			t.Logf("SLOPE %s: statement %d (%s) peak memory %d bytes at %d, %d at %d", axis, hash, filters[i].Table, peak, smallSize, large[i][hash], largeSize)
		}
		assertStatementMemoryFollowsEvidence(t, ctx, direct, axis+" / "+filters[i].Table+" statements", small[i], large[i], 1)
	}
}
