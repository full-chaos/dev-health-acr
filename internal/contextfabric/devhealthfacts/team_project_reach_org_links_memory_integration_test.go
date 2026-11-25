package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const (
	orgLinksMemoryOrg          = "c4d82e17-6b09-4f3a-9a51-0e7d3b8c2f64"
	ownershipHistoryProjectOrg = "d1f6a3b8-7e24-4c90-8b35-4a2e9c6d1f07"
	ownershipHistoryRepoOrg    = "e8b25c70-1a93-4d6f-b042-7c3f8a9e5d16"
	orgLinksMemoryFillerLinks  = 24_000
	orgLinksMemoryMoreLinks    = 60_000
	orgLinksMemoryCeilingLinks = 400_000
	prodShapeMemoryOrg         = "0a6f3c58-9d21-4b74-8e15-c2f0a7b94d63"
	ownedLinksMemoryOrg        = "f2a96d41-3c85-4e07-a1b9-5d70c8e3b926"
	prodLinkedIssues           = 24_000
	prodEstimatePeakBytes      = uint64(48 << 20)
	historyFloorBytes          = uint64(2 << 20)
	// linksInsideLargeMax bounds each statement where every link of 96k is
	// inside the owned projects. A link-row statement may not hold more links
	// than one 24k read does (58.1 MB measured), whatever the link count: 64 MiB
	// is that peak plus 15%, a quarter of the 256 MiB API statement cap.
	linksInsideLargeMax = uint64(64 << 20)
	// linkedReadsInsideLargeMax bounds each linked pull request read at 96k
	// all-inside links: its first measure (33,601,449 B, CI run 45556469550)
	// plus 15%.
	linkedReadsInsideLargeMax = uint64(38_641_667)
	// The work bounds at 96k all-inside links (96,001 candidates, 5 chunks) and
	// at 24,001 candidates (2 chunks): the first measure (CI run 45562209733,
	// the larger of the unmerged and merged reads) plus 15%, rounded up.
	linksInsideSetReadRowsMax         = (1_635_367*115 + 99) / 100   // merged
	linksInsideSetReadBytesMax        = (237_131_284*115 + 99) / 100 // unmerged
	linksInsideLinkedReadRowsMax      = (685_461*115 + 99) / 100     // merged
	linksInsideLinkedReadBytesMax     = (47_598_017*115 + 99) / 100  // merged
	linksInsideTwoChunkSetReadRowsMax = (268_748*115 + 99) / 100     // merged
	// linksInsideBaseStatements is what the read ran at main at both sizes (CI
	// run 45551732249); linksInsideLargeStatements is what it runs at 96k: the
	// link set once per 24k-candidate chunk.
	linksInsideBaseStatements  = 10
	linksInsideLargeStatements = 11
	// linkedPullRequestStatements is the number of team statements that read
	// the project-reached pull requests (reached repositories, counts, states).
	linkedPullRequestStatements = 3
	ownershipHistorySmall       = 100
	ownershipHistoryLarge       = 1_000
)

// The repositories a team reaches through its owned projects are read from the
// owned projects' issues only. Link rows and pull requests of other teams and
// of work items in no project must not count against the statement's memory:
// the peak of each project-reached statement, measured with the organization's
// link rows and pull requests tripled, may not grow past the slope.
func TestTeamProjectReachedStatementMemoryFollowsOwnedProjectsNotOrganizationLinksOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		// The peaks are the ClickHouse server's, which the Go race detector does not touch;
		// it only multiplies the Go-side seeding of about a million rows past the race job's
		// budget. The ordinary unit legs run this row and fail on any bound.
		t.Skip("memory bound rows run in the unit legs; the race detector does not change a server peak")
	}
	for _, merged := range []bool{false, true} {
		mode := map[bool]string{false: "unmerged", true: "merged"}[merged]
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			rawQuery, direct := newDedicatedClickHouseIntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
				maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
				options.MaxBytesToRead = &maxBytesToRead
			})
			query := statementthreads.API(rawQuery)
			for _, statement := range devhealthschema.DDL(
				// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
				"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
				"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
			) {
				if err := direct.Exec(ctx, statement); err != nil {
					t.Fatalf("create table: %v\n%s", err, statement)
				}
			}
			if !merged {
				// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
				for _, table := range []string{"work_items", "work_graph_issue_pr", "git_pull_requests", "team_project_ownership", "team_repo_ownership"} {
					if err := direct.Exec(ctx, `SYSTEM STOP MERGES `+table); err != nil {
						t.Fatalf("stop merges on %s: %v", table, err)
					}
				}
			}
			parts := 1
			if !merged {
				parts = 20
			}
			linkFiller := func(prefix, project, prRepo string) func(exec func(string, ...any), org string, from, count int) {
				return func(exec func(string, ...any), org string, from, count int) {
					at := ts(2026, 9, 29, 0, 0, 0)
					const batches = 20
					for b := 0; b < batches; b++ {
						first, n := uint64(from+b*count/batches), uint64((b+1)*count/batches-b*count/batches)
						exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('`+prefix+`-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', ?, NULL FROM numbers(?, ?)`,
							repoUUID("repo-filler"), org, at, project, first, n)
						exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1000), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?, ?)`,
							repoUUID(prRepo), org, at, at, first, n)
						exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('`+prefix+`-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`,
							repoUUID(prRepo), at, org, first, n)
					}
				}
			}
			t.Run("organization links outside the owned projects", func(t *testing.T) {
				measureTeamReachAxis(t, ctx, query, statementthreads.Unbounded(rawQuery), direct, orgLinksMemoryOrg, merged, parts, linkFiller("outside", "", "repo-filler"),
					"organization links outside the owned projects "+mode, prodLinkedIssues, 90_000, orgLinksMemoryCeilingLinks, axisGuard{smallMax: 40 << 20, largeMax: 100 << 20})
			})
			t.Run("prod shape one quarter inside the owned projects", func(t *testing.T) {
				inside, outside := linkFiller("owned", "proj-owner", "repo-reached"), linkFiller("outside", "", "repo-filler")
				measureTeamReachAxis(t, ctx, query, statementthreads.Unbounded(rawQuery), direct, prodShapeMemoryOrg, merged, parts, func(exec func(string, ...any), org string, from, count int) {
					inside(exec, org, from/4, count/4)
					outside(exec, org, from-from/4, count-count/4)
				}, "prod shape (6k inside, 18k outside; 4x) "+mode, prodLinkedIssues, 4*prodLinkedIssues, 0, axisGuard{smallMax: prodEstimatePeakBytes, largeMax: 120 << 20})
			})
			t.Run("links inside the owned projects", func(t *testing.T) {
				measureTeamReachAxis(t, ctx, query, statementthreads.Unbounded(rawQuery), direct, ownedLinksMemoryOrg, merged, parts, linkFiller("owned", "proj-owner", "repo-reached"),
					"links inside the owned projects "+mode, prodLinkedIssues-1, 4*prodLinkedIssues, 0, axisGuard{smallMax: 56 << 20, largeMax: linksInsideLargeMax, linkedLargeMax: linkedReadsInsideLargeMax, work: &readWorkGuard{smallStatementsMax: linksInsideBaseStatements, largeStatements: linksInsideLargeStatements}})
			})
			if merged {
				return
			}
			t.Run("project ownership history", func(t *testing.T) {
				measureTeamReachAxis(t, ctx, query, statementthreads.Unbounded(rawQuery), direct, ownershipHistoryProjectOrg, false, 0, func(exec func(string, ...any), org string, from, count int) {
					n := count
					exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'linear', 'team-owner', 'proj-owner', NULL, 'native', addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ?) * 3600),
  addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ? + 1) * 3600), toDateTime64('2026-09-29 00:00:00', 3, 'UTC') FROM numbers(?)`,
						org, uint64(from), uint64(from), uint64(n))

				}, "project ownership history versions", ownershipHistorySmall, ownershipHistoryLarge, 0, axisGuard{floor: historyFloorBytes})
			})
			t.Run("repository ownership history", func(t *testing.T) {
				measureTeamReachAxis(t, ctx, query, statementthreads.Unbounded(rawQuery), direct, ownershipHistoryRepoOrg, false, 0, func(exec func(string, ...any), org string, from, count int) {
					n := count
					// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
					exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'linear', 'team-owner', ?, 'acme/repo-filler', 'exact', 'native', 1, 100, 0, addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ?) * 3600),
  addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ? + 1) * 3600), toDateTime64('2026-09-29 00:00:00', 3, 'UTC') FROM numbers(?)`,
						org, repoUUID("repo-filler"), uint64(from), uint64(from), uint64(n))

				}, "repository ownership history versions", ownershipHistorySmall, ownershipHistoryLarge, 0, axisGuard{floor: historyFloorBytes})
			})
		})
	}
}

// measureTeamReachAxis seeds one team that reaches one repository through one
// owned project, grows the axis grow names from the smaller to the larger
// size, and applies the slope guard to every statement the team pull request
// read runs over team_project_ownership and team_repo_ownership.
func measureTeamReachAxis(t *testing.T, ctx context.Context, query, ceilingQuery contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, org string, mergedParts bool, wantParts int,
	grow func(exec func(string, ...any), org string, from, count int), axis string, smallSize, largeSize, ceilingSize int, guard axisGuard) {
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
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
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
	ceilingProvider := findProvider(t, devhealthfacts.NewProviders(ceilingQuery), contextfabric.FactPullRequests)
	window := contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59))
	filters := []statementFilter{
		{Table: "team_project_ownership", QueryContains: "work_graph_issue_pr"},
		{Table: "team_project_ownership", QueryContains: "team_project_ownership"},
		{Table: "team_repo_ownership", QueryContains: "team_repo_ownership"},
		// The linked pull request reads, whatever tables they join: a reader
		// that stops reading team_project_ownership still counts here.
		{Table: "git_pull_requests", QueryContains: "lr.pr_repo_id"},
	}
	const linkedPullRequestFilter = 3
	var works []labeledWork
	read := func(label string, provider contextfabric.FactProvider) []statementMemory {
		var active uint64
		if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE database = currentDatabase() AND table = 'work_items' AND active`).Scan(&active); err != nil {
			t.Fatalf("count active parts: %v", err)
		}
		switch {
		case mergedParts && active != 1:
			t.Fatalf("%s: %d active work_items parts at the %s size, want 1 (the merged state was not reached)", axis, active, label)
		case !mergedParts && wantParts > 0 && int(active) < wantParts:
			t.Fatalf("%s: %d active work_items parts at the %s size, want at least %d (the unmerged state was not reached)", axis, active, label, wantParts)
		}
		mark := serverNowMicros(t, ctx, direct)
		started := time.Now()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: window, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
		})
		wall := time.Since(started)
		if err != nil {
			t.Fatalf("ReadFacts at the %s size of %s: %v: %s\nfailed statements:\n%s", label, axis, err, readFailureCause(err), failedStatementsSince(ctx, direct, mark))
		}
		wantInt(t, factFor(t, result, teamSubject(teamID)).Fields, "project_reached_repository_count", 1)
		var out []statementMemory
		for _, filter := range filters {
			out = append(out, statementMemorySince(t, ctx, direct, filter, mark))
		}
		if n := len(out[linkedPullRequestFilter]); n != linkedPullRequestStatements {
			t.Fatalf("%s at the %s size: %d linked pull request statements measured, want %d (a linked pull request read left the measure)", axis, label, n, linkedPullRequestStatements)
		}
		work := readWorkSince(t, ctx, direct, mark)
		t.Logf("COST %s: the %s read ran %d statements in %d ms", axis, label, work.statements, wall.Milliseconds())
		t.Logf("WORK %s: the %s read: link set %d statement(s) read_rows %d read_bytes %d; linked pull request reads read_rows %d read_bytes %d", axis, label, work.setStatements, work.setReadRows, work.setReadBytes, work.linkedReadRows, work.linkedReadBytes)
		works = append(works, labeledWork{label: label, work: work})
		if size := longestStatementSince(t, ctx, direct, filters[linkedPullRequestFilter], mark); size >= maxQuerySizeBytes {
			t.Fatalf("%s at the %s size: a linked pull request statement is %d bytes, want under max_query_size %d (the set must ride a sent table, never the text)", axis, label, size, maxQuerySizeBytes)
		}
		return out
	}
	merged := func() {
		if !mergedParts {
			return
		}
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
		for _, table := range []string{"work_items", "work_graph_issue_pr", "git_pull_requests", "team_project_ownership", "team_repo_ownership"} {
			exec(`OPTIMIZE TABLE ` + table + ` FINAL`)
		}
	}
	grown := 0
	growTo := func(size int) {
		grow(exec, org, grown, size-grown)
		grown = size
		merged()
	}
	growTo(smallSize)
	read("warm-up", provider)
	small := minPeaks(read("smaller", provider), read("smaller again", provider), read("smaller third", provider))
	if guard.work != nil {
		mark := serverNowMicros(t, ctx, direct)
		if _, err := devhealthfacts.TeamLinkSetUnchunkedForTest(ctx, query, org, []string{teamID}, contextfabric.FactQuery{Time: window, Kind: contextfabric.FactPullRequests}); err != nil {
			t.Fatalf("%s: unchunked link set: %v", axis, err)
		}
		unchunked := readWorkSince(t, ctx, direct, mark)
		for _, w := range works {
			if w.work.setStatements != 1 || w.work.setReadRows != unchunked.setReadRows {
				t.Errorf("%s at the %s size: the link set ran %d statement(s) reading %d rows, want 1 reading the %d rows of one unchunked statement", axis, w.label, w.work.setStatements, w.work.setReadRows, unchunked.setReadRows)
			}
			if w.work.statements > guard.work.smallStatementsMax {
				t.Errorf("%s at the %s size: %d statements, want at most %d (main)", axis, w.label, w.work.statements, guard.work.smallStatementsMax)
			}
		}
		works = nil
		// One candidate more is two chunks: the second pays its own floor.
		growTo(smallSize + 1)
		read("control one candidate over the chunk", provider)
		for _, w := range works {
			if w.work.setStatements != 2 || w.work.setReadRows > linksInsideTwoChunkSetReadRowsMax {
				t.Errorf("%s at the %s size: the link set ran %d statement(s) reading %d rows, want 2 reading at most %d", axis, w.label, w.work.setStatements, w.work.setReadRows, linksInsideTwoChunkSetReadRowsMax)
			}
		}
		works = nil
	}
	growTo(largeSize)
	large := minPeaks(read("larger", provider), read("larger again", provider), read("larger third", provider))
	if guard.work != nil {
		for _, w := range works {
			if w.work.statements != guard.work.largeStatements {
				t.Errorf("%s at the %s size: %d statements, want %d", axis, w.label, w.work.statements, guard.work.largeStatements)
			}
			for _, c := range []struct {
				what     string
				got, max uint64
			}{
				{"link set read_rows", w.work.setReadRows, linksInsideSetReadRowsMax},
				{"link set read_bytes", w.work.setReadBytes, linksInsideSetReadBytesMax},
				{"linked pull request reads read_rows", w.work.linkedReadRows, linksInsideLinkedReadRowsMax},
				{"linked pull request reads read_bytes", w.work.linkedReadBytes, linksInsideLinkedReadBytesMax},
			} {
				if c.got > c.max {
					t.Errorf("%s at the %s size: %s %d, want at most %d", axis, w.label, c.what, c.got, c.max)
				}
			}
		}
	}
	for i := range filters {
		for hash, peak := range small[i] {
			t.Logf("SLOPE %s: statement %d (%s) peak memory %d bytes at %d, %d at %d", axis, hash, filters[i].Table, peak, smallSize, large[i][hash], largeSize)
		}
		filterGuard := guard
		if i == linkedPullRequestFilter && guard.linkedLargeMax > 0 {
			filterGuard.largeMax = guard.linkedLargeMax
		}
		filterGuard.assert(t, axis+" / "+filters[i].Table+" statements", small[i], large[i])
	}
	if ceilingSize > 0 {
		growTo(ceilingSize)
		ceiling := read(fmt.Sprintf("ceiling (%d)", ceilingSize), ceilingProvider)
		for i := range filters {
			for hash, peak := range ceiling[i] {
				over := ""
				if peak >= contextpacket.APIStatementMaxMemoryUsage {
					over = " (OVER the API statement cap: the hosted API refuses this read at this size)"
				}
				t.Logf("CEILING %s: statement %d (%s) peak memory %d bytes at %d%s", axis, hash, filters[i].Table, peak, ceilingSize, over)
			}
		}
	}
}

// readWork is what one read sent, from query_log: the SELECTs over the test
// database that ended after the mark (finished or failed), and the rows and
// bytes the link set statements and the linked pull request reads read.
type readWork struct {
	statements, setStatements       uint64
	setReadRows, setReadBytes       uint64
	linkedReadRows, linkedReadBytes uint64
}

type labeledWork struct {
	label string
	work  readWork
}

func readWorkSince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, markMicros int64) readWork {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS query_log`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
	}
	var w readWork
	if err := direct.QueryRow(ctx, `SELECT count(), countIf(is_set), sumIf(read_rows, is_set), sumIf(read_bytes, is_set), sumIf(read_rows, is_linked), sumIf(read_bytes, is_linked)
FROM (
	SELECT read_rows, read_bytes,
		position(query, 'groupArray(r), groupArray(n)') > 0 AS is_set,
		has(tables, concat(currentDatabase(), '.git_pull_requests')) AND position(query, 'lr.pr_repo_id') > 0 AS is_linked
	FROM system.query_log
	WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
	  AND has(databases, currentDatabase())
)`, markMicros).Scan(&w.statements, &w.setStatements, &w.setReadRows, &w.setReadBytes, &w.linkedReadRows, &w.linkedReadBytes); err != nil {
		t.Fatalf("read the read's work: %v", err)
	}
	return w
}

// maxQuerySizeBytes is ClickHouse's default max_query_size, the most query text
// the server parses.
const maxQuerySizeBytes = 256 << 10

// longestStatementSince is the longest query text among the statements filter
// selects after the mark.
func longestStatementSince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, filter statementFilter, markMicros int64) uint64 {
	t.Helper()
	var n uint64
	if err := direct.QueryRow(ctx, `SELECT max(length(query)) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.', ?)) AND position(query, ?) > 0`, markMicros, filter.Table, filter.QueryContains).Scan(&n); err != nil {
		t.Fatalf("read statement size: %v", err)
	}
	return n
}

// failedStatementsSince names, after the mark, every SELECT the server ended
// with an exception: hash, code, read_bytes, memory and the query head.
func failedStatementsSince(ctx context.Context, direct clickhousedriver.Conn, markMicros int64) string {
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		return "flush logs: " + err.Error()
	}
	rows, err := direct.Query(ctx, `SELECT concat('hash=', toString(normalized_query_hash), ' code=', toString(exception_code), ' read_bytes=', toString(read_bytes), ' memory=', toString(memory_usage), ' tables=', arrayStringConcat(tables, ','), ' || ', substring(replaceRegexpAll(query, '\\s+', ' '), 1, 300))
FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select' ORDER BY event_time_microseconds`, markMicros)
	if err != nil {
		return "query_log unreadable: " + err.Error()
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return out.String() + "scan: " + err.Error()
		}
		out.WriteString(line + "\n")
	}
	return out.String()
}

// axisGuard is the rule of one axis. A flat axis (floor set) allows the slope
// plus a floor of memory. A growing axis (links) instead holds each statement
// under smallMax at the smaller size and under largeMax at the larger one: the
// bounds sit between the form that streams linked ids through owned projects
// and the form that held every organization link in one set.
type axisGuard struct {
	floor    uint64
	smallMax uint64
	largeMax uint64
	// linkedLargeMax, when set, is largeMax for the linked pull request reads.
	linkedLargeMax uint64
	// work, when set, pins the read's work on the all-inside axis.
	work *readWorkGuard
}

// readWorkGuard pins the work of a read on the all-inside axis, never its wall
// time: at the smaller size (one candidate chunk) the link set runs once and
// reads what one unchunked statement reads, in no more statements than main;
// at the larger size the read runs a fixed number of statements.
type readWorkGuard struct {
	smallStatementsMax uint64
	largeStatements    uint64
}

func (g axisGuard) assert(t *testing.T, label string, small, large statementMemory) {
	t.Helper()
	if len(small) < 1 || len(large) != len(small) {
		t.Fatalf("%s: measurement did not happen: %d and %d distinct statements logged", label, len(small), len(large))
	}
	for hash, smallPeak := range small {
		largePeak, ok := large[hash]
		if !ok {
			t.Fatalf("%s: statement %d ran at the smaller size only", label, hash)
		}
		if g.smallMax == 0 {
			if float64(largePeak) > statementMemorySlope*float64(smallPeak)+float64(g.floor) {
				t.Errorf("%s: statement %d grew: peak %d bytes at the larger size, %d at the smaller, over the %.2fx slope plus %d", label, hash, largePeak, smallPeak, statementMemorySlope, g.floor)
			}
			for _, peak := range []uint64{smallPeak, largePeak} {
				if peak >= statementMemoryCeiling {
					t.Errorf("%s: statement %d peaked at %d bytes, want under %d", label, hash, peak, statementMemoryCeiling)
				}
			}
			continue
		}
		if smallPeak > g.smallMax {
			t.Errorf("%s: statement %d peaked at %d bytes at the smaller size, want at most %d", label, hash, smallPeak, g.smallMax)
		}
		if largePeak > g.largeMax {
			t.Errorf("%s: statement %d peaked at %d bytes at the larger size, want at most %d", label, hash, largePeak, g.largeMax)
		}
	}
}

// minPeaks is, per statement, the smallest peak of the warm reads: one slow
// read on a shared runner is noise, a peak every read shows is the statement.
func minPeaks(runs ...[]statementMemory) []statementMemory {
	out := make([]statementMemory, len(runs[0]))
	for i := range out {
		out[i] = statementMemory{}
		for _, run := range runs {
			for hash, peak := range run[i] {
				if best, ok := out[i][hash]; !ok || peak < best {
					out[i][hash] = peak
				}
			}
		}
	}
	return out
}
