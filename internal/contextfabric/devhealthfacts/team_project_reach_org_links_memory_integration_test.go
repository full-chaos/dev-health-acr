package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
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
	orgLinksMemoryFillerLinks  = 24_000
	orgLinksMemoryMoreLinks    = 60_000
	orgLinksMemoryCeilingLinks = 400_000
	prodShapeMemoryOrg         = "0a6f3c58-9d21-4b74-8e15-c2f0a7b94d63"
	ownedLinksMemoryOrg        = "f2a96d41-3c85-4e07-a1b9-5d70c8e3b926"
	prodLinkedIssues           = 24_000
	prodEstimatePeakBytes      = uint64(48 << 20)
	historyFloorBytes          = uint64(2 << 20)
	ownershipHistorySmall      = 100
	ownershipHistoryLarge      = 1_000
)

// The repositories a team reaches through its owned projects are read from the
// owned projects' issues only. Link rows and pull requests of other teams and
// of work items in no project must not count against the statement's memory:
// the peak of each project-reached statement, measured with the organization's
// link rows and pull requests tripled, may not grow past the slope.
func TestTeamProjectReachedStatementMemoryFollowsOwnedProjectsNotOrganizationLinksOnRealClickHouse(t *testing.T) {
	for _, merged := range []bool{false, true} {
		mode := map[bool]string{false: "unmerged", true: "merged"}[merged]
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
				maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
				options.MaxBytesToRead = &maxBytesToRead
			})
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
				measureTeamReachAxis(t, ctx, query, direct, orgLinksMemoryOrg, merged, parts, linkFiller("outside", "", "repo-filler"),
					"organization links outside the owned projects "+mode, prodLinkedIssues, 90_000, orgLinksMemoryCeilingLinks, axisGuard{smallMax: 40 << 20, largeMax: 100 << 20})
			})
			t.Run("prod shape one quarter inside the owned projects", func(t *testing.T) {
				inside, outside := linkFiller("owned", "proj-owner", "repo-reached"), linkFiller("outside", "", "repo-filler")
				measureTeamReachAxis(t, ctx, query, direct, prodShapeMemoryOrg, merged, parts, func(exec func(string, ...any), org string, from, count int) {
					inside(exec, org, from/4, count/4)
					outside(exec, org, from-from/4, count-count/4)
				}, "prod shape (6k inside, 18k outside; 4x) "+mode, prodLinkedIssues, 4*prodLinkedIssues, 0, axisGuard{smallMax: prodEstimatePeakBytes, largeMax: 120 << 20})
			})
			t.Run("links inside the owned projects", func(t *testing.T) {
				measureTeamReachAxis(t, ctx, query, direct, ownedLinksMemoryOrg, merged, parts, linkFiller("owned", "proj-owner", "repo-reached"),
					"links inside the owned projects "+mode, prodLinkedIssues, 4*prodLinkedIssues, 0, axisGuard{smallMax: 56 << 20, largeMax: 170 << 20})
			})
			if merged {
				return
			}
			t.Run("project ownership history", func(t *testing.T) {
				measureTeamReachAxis(t, ctx, query, direct, ownershipHistoryProjectOrg, false, 0, func(exec func(string, ...any), org string, from, count int) {
					n := count
					exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'linear', 'team-owner', 'proj-owner', NULL, 'native', addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ?) * 3600),
  addSeconds(toDateTime64('2021-01-01 00:00:00', 3, 'UTC'), (number + ? + 1) * 3600), toDateTime64('2026-09-29 00:00:00', 3, 'UTC') FROM numbers(?)`,
						org, uint64(from), uint64(from), uint64(n))

				}, "project ownership history versions", ownershipHistorySmall, ownershipHistoryLarge, 0, axisGuard{floor: historyFloorBytes})
			})
			t.Run("repository ownership history", func(t *testing.T) {
				measureTeamReachAxis(t, ctx, query, direct, ownershipHistoryRepoOrg, false, 0, func(exec func(string, ...any), org string, from, count int) {
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
func measureTeamReachAxis(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, direct clickhousedriver.Conn, org string, mergedParts bool, wantParts int,
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
	window := contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59))
	filters := []statementFilter{
		{Table: "team_project_ownership", QueryContains: "work_graph_issue_pr"},
		{Table: "team_project_ownership", QueryContains: "team_project_ownership"},
		{Table: "team_repo_ownership", QueryContains: "team_repo_ownership"},
	}
	read := func(label string) []statementMemory {
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
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: window, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts at the %s size of %s: %v: %s\nfailed statements:\n%s", label, axis, err, readFailureCause(err), failedStatementsSince(ctx, direct, mark))
		}
		wantInt(t, factFor(t, result, teamSubject(teamID)).Fields, "project_reached_repository_count", 1)
		var out []statementMemory
		for _, filter := range filters {
			out = append(out, statementMemorySince(t, ctx, direct, filter, mark))
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
	read("warm-up")
	small := minPeaks(read("smaller"), read("smaller again"), read("smaller third"))
	growTo(largeSize)
	large := minPeaks(read("larger"), read("larger again"), read("larger third"))
	for i := range filters {
		for hash, peak := range small[i] {
			t.Logf("SLOPE %s: statement %d (%s) peak memory %d bytes at %d, %d at %d", axis, hash, filters[i].Table, peak, smallSize, large[i][hash], largeSize)
		}
		guard.assert(t, axis+" / "+filters[i].Table+" statements", small[i], large[i])
	}
	if ceilingSize > 0 {
		growTo(ceilingSize)
		ceiling := read(fmt.Sprintf("ceiling (%d)", ceilingSize))
		for i := range filters {
			for hash, peak := range ceiling[i] {
				t.Logf("CEILING %s: statement %d (%s) peak memory %d bytes at %d", axis, hash, filters[i].Table, peak, ceilingSize)
			}
		}
	}
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
