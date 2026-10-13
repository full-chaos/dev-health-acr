package devhealthfacts_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	projectReachMemoryOrg            = "b7a41c9e-2d63-4f85-a1e0-6c93d8f20b47"
	projectReachInterleavedMemoryOrg = "3e9d17b2-5a48-4c06-9f1d-8b27c4e6a053"
	projectReachReadBytesSlope       = 1.15
)

var projectReachFillerPrefix = "filler-" + strings.Repeat("x", 96) + "-"

// The repositories a team reaches through its owned projects' linked pull
// requests are read from the issues of those projects only. The cost of the
// reads follows the owned projects, not the size of the organization's work
// item table; work items of other teams' projects, and work items of no
// project, are never counted into the reach. The interleaved shape puts the
// linked issues in the filler's repository with ids inside the filler's id
// range and merges the table to one part, so linked and unlinked work items
// share granules.
func TestTeamProjectReachedRepositoriesFollowOwnedProjectsNotOrganizationSizeOnRealClickHouse(t *testing.T) {
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
	t.Run("clustered", func(t *testing.T) {
		measureProjectReachedRead(t, ctx, query, direct, projectReachMemoryOrg, false)
	})
	t.Run("interleaved", func(t *testing.T) {
		measureProjectReachedRead(t, ctx, query, direct, projectReachInterleavedMemoryOrg, true)
	})
}

func measureProjectReachedRead(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, direct clickhousedriver.Conn, org string, interleaved bool) {
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	itemID := func(label, interleavedNumber string) string {
		if interleaved {
			return projectReachFillerPrefix + interleavedNumber + "-" + label
		}
		return label
	}
	reachedItem, weakItem := itemID("i-reached", "31000"), itemID("i-weak", "92000")
	otherTeamItem, noProjectItem := itemID("i-other-team", "157000"), itemID("i-no-project", "218000")
	const teamID, otherTeamID, project, otherProject = "team-owner", "team-other", "proj-owner", "proj-other"
	for _, label := range []string{"repo-reached", "repo-other-team", "repo-filler"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), org, "acme/"+label, "github", at)
	}
	for _, team := range []string{teamID, otherTeamID} {
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, org, "linear", uint8(1))
	}
	for _, owned := range []struct{ team, project string }{{teamID, project}, {otherTeamID, otherProject}} {
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			owned.project, org, "linear", nil, owned.project, uint8(1), "active", "", at)
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			org, "linear", owned.team, owned.project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	for _, item := range []struct{ id, project string }{{reachedItem, project}, {weakItem, project}, {otherTeamItem, otherProject}, {noProjectItem, ""}} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			item.id, repoUUID("repo-filler"), org, "title "+item.id, at, item.project)
	}
	for _, pr := range []struct {
		repo   string
		number uint32
	}{{"repo-reached", 1}, {"repo-reached", 2}, {"repo-other-team", 3}, {"repo-other-team", 4}} {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(pr.repo), org, pr.number, "pr", "open", at, at, "feature", "")
	}
	for _, link := range []struct {
		item, repo, tier string
		number           uint32
	}{
		{reachedItem, "repo-reached", "native", 1},
		{weakItem, "repo-reached", "heuristic", 2},
		{otherTeamItem, "repo-other-team", "native", 3},
		{noProjectItem, "repo-other-team", "native", 4},
	} {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(link.repo), link.item, link.number, float32(1), link.tier, "", at, org)
	}
	addFiller := func(suffix string, items int) {
		t.Helper()
		id := `concat('filler-` + suffix + `', repeat('x', 96), '-', toString(number))`
		if interleaved {
			id = `concat('` + projectReachFillerPrefix + `', toString(number), '` + suffix + `')`
		}
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT `+id+`, ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?)`,
			repoUUID("repo-filler"), org, at, uint64(items))
		if interleaved {
			exec(`OPTIMIZE TABLE work_items FINAL`)
		}
	}
	addFiller("", blockerMemoryFillerItems)

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	window := contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59))
	var lastMark int64
	read := func() (statementMemory, contextfabric.FactProviderResult, error) {
		mark := serverNowMicros(t, ctx, direct)
		lastMark = mark
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: window, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
		})
		return statementMemorySince(t, ctx, direct, statementFilter{Table: "team_project_ownership", QueryContains: "work_graph_issue_pr"}, mark), result, err
	}
	if _, _, err := read(); err != nil {
		t.Fatalf("warm-up ReadFacts: %v: %s", err, readFailureCause(err))
	}
	small, smallResult, smallErr := read()
	smallBytes := projectReachedReadBytes(t, ctx, direct, lastMark)
	addFiller("more-", blockerMemoryMoreFillerItems)
	large, largeResult, largeErr := read()
	largeBytes := projectReachedReadBytes(t, ctx, direct, lastMark)
	t.Logf("read_bytes of the project-reached statements: %d at %d filler work items, %d at %d", smallBytes, blockerMemoryFillerItems, largeBytes, blockerMemoryFillerItems+blockerMemoryMoreFillerItems)
	// The reach read and the linked pull request read run on every read.
	assertStatementMemoryFollowsEvidence(t, ctx, direct, "team project-reached repository statements", small, large, 2)
	if smallBytes == 0 {
		t.Fatalf("read_bytes measurement did not happen: no project-reached statement logged at the smaller size")
	}
	if float64(largeBytes) > projectReachReadBytesSlope*float64(smallBytes) {
		t.Errorf("project-reached statements' read_bytes grew with the organization's work items: %d at the larger size, %d at the smaller, over the %.2fx slope", largeBytes, smallBytes, projectReachReadBytesSlope)
	}
	assertLinkRowsNarrowedWithIdenticalRows(t, ctx, query, direct, org, teamID)
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

// teamProjectLinkRowsSQL resolves the owned projects once and prunes the work
// item read from the named teams' ownership rows. The rows are the rows of the
// statement it replaced, and the statement's peak memory is lower.
func assertLinkRowsNarrowedWithIdenticalRows(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, direct clickhousedriver.Conn, org, teamID string) {
	t.Helper()
	run := func(marker, rows string) ([]string, uint64) {
		t.Helper()
		statement := "SELECT /* " + marker + " */ team_id, work_item_id, toString(pr_repo_id), toInt64(pr_number), toInt64(link_rank) FROM " + rows + " AS lr ORDER BY team_id, work_item_id, pr_number"
		mark := serverNowMicros(t, ctx, direct)
		var out []string
		err := readers.QueryOrgScopedNamed(ctx, query, "TeamProjectLinkRows", statement, org, []string{teamID}, func(row contextpacket.ClickHouseRowScanner) error {
			var team, item, repo string
			var number int64
			var rank int64
			if err := row.Scan(&team, &item, &repo, &number, &rank); err != nil {
				return err
			}
			out = append(out, fmt.Sprint(team, "|", item, "|", repo, "|", number, "|", rank))
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", marker, err)
		}
		peak := statementMemorySince(t, ctx, direct, statementFilter{Table: "team_project_ownership", QueryContains: "work_graph_issue_pr"}, mark)
		var worst uint64
		for _, bytes := range peak {
			if bytes > worst {
				worst = bytes
			}
		}
		if worst == 0 {
			t.Fatalf("%s: memory measurement did not happen", marker)
		}
		return out, worst
	}
	run("link-rows-before", devhealthfacts.LegacyTeamProjectLinkRowsSQLForTest("ids"))
	beforeRows, before := run("link-rows-before", devhealthfacts.LegacyTeamProjectLinkRowsSQLForTest("ids"))
	run("link-rows-after", devhealthfacts.TeamProjectLinkRowsSQLForTest("ids"))
	afterRows, after := run("link-rows-after", devhealthfacts.TeamProjectLinkRowsSQLForTest("ids"))
	t.Logf("MEMORY team project link rows: before %d bytes, after %d bytes (%d rows)", before, after, len(afterRows))
	if len(beforeRows) == 0 || !slices.Equal(beforeRows, afterRows) {
		t.Fatalf("link rows differ: before %v, after %v", beforeRows, afterRows)
	}
	if after >= before {
		t.Errorf("narrowed statement peaked at %d bytes, want under the %d bytes of the statement it replaced", after, before)
	}
}
