package devhealthfacts_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const (
	orgLinksCeilingOrg = "7b3e91c4-5a28-4f06-b8d3-2c61e0f94a75"
	// orgLinksCeilingBound is half the API statement cap: a link-row statement
	// at the ceiling size keeps that much room under the cap.
	orgLinksCeilingBound = contextpacket.APIStatementMaxMemoryUsage / 2
)

// orgLinksCeilingSizes are the organization link counts outside the owned
// projects the read is measured at; the last is the ceiling.
var orgLinksCeilingSizes = []int{24_000, 90_000, 400_000}

// linkRowStatement is one link-row statement in query_log: its peak memory,
// its read_bytes, the max_threads it ran with, and whether it is the team link
// candidate statement (which still reads the organization's linked ids).
type linkRowStatement struct {
	peak      uint64
	readBytes uint64
	threads   string
	candidate bool
}

// The team pull-request read is served for an organization with 400,000 link
// rows outside the team's owned projects, through the client the hosted API
// uses (the API statement memory cap at the production max_threads). Each
// statement that reads the team's project link rows, and the one that reads
// the team's link candidates once per read, stays under half the cap at that
// size: no link-row join side holds the organization's other links.
func TestTeamPullRequestReadServesAnOrganizationWithManyOutsideLinksUnderTheAPICapOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("memory bound rows run in the unit legs; the race detector does not change a server peak")
	}
	for _, merged := range []bool{false, true} {
		mode := map[bool]string{false: "unmerged", true: "merged"}[merged]
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			rawQuery, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
				maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
				options.MaxBytesToRead = &maxBytesToRead
			})
			query := contextpacket.NewMemoryCappedQueryClient(rawQuery, contextpacket.APIStatementMaxMemoryUsage, contextpacket.WithMaxThreads(contextpacket.StatementMaxThreads))
			for _, statement := range devhealthschema.DDL(
				// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
				"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
				"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
			) {
				if err := direct.Exec(ctx, statement); err != nil {
					t.Fatalf("create table: %v\n%s", err, statement)
				}
			}
			// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
			tables := []string{"work_items", "work_graph_issue_pr", "git_pull_requests", "team_project_ownership", "team_repo_ownership"}
			if !merged {
				for _, table := range tables {
					if err := direct.Exec(ctx, `SYSTEM STOP MERGES `+table); err != nil {
						t.Fatalf("stop merges on %s: %v", table, err)
					}
				}
			}
			org, at := orgLinksCeilingOrg, ts(2026, 9, 29, 0, 0, 0)
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

			grown := 0
			growTo := func(size int) {
				const batches = 20
				count := size - grown
				for b := 0; b < batches; b++ {
					first, n := uint64(grown+b*count/batches), uint64((b+1)*count/batches-b*count/batches)
					exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('outside-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?, ?)`,
						repoUUID("repo-filler"), org, at, first, n)
					exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1000), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?, ?)`,
						repoUUID("repo-filler"), org, at, at, first, n)
					exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('outside-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`,
						repoUUID("repo-filler"), at, org, first, n)
				}
				grown = size
				if merged {
					for _, table := range tables {
						exec(`OPTIMIZE TABLE ` + table + ` FINAL`)
					}
				}
			}

			var serverThreads string
			if err := direct.QueryRow(ctx, `SELECT value FROM system.settings WHERE name = 'max_threads'`).Scan(&serverThreads); err != nil {
				t.Fatalf("read the server max_threads: %v", err)
			}
			wantThreads := fmt.Sprint(contextpacket.StatementMaxThreads)
			// A setting equal to the server default is not logged as changed.
			ranAtStatementThreads := func(logged string) bool {
				return logged == wantThreads || (logged == "" && (serverThreads == wantThreads || serverThreads == "auto("+wantThreads+")"))
			}
			provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
			window := contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59))
			read := func(label string) map[uint64]linkRowStatement {
				t.Helper()
				var active uint64
				if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE database = currentDatabase() AND table = 'work_items' AND active`).Scan(&active); err != nil {
					t.Fatalf("count active parts: %v", err)
				}
				if merged && active != 1 {
					t.Fatalf("%s: %d active work_items parts, want 1 (the merged state was not reached)", label, active)
				}
				if !merged && active < 20 {
					t.Fatalf("%s: %d active work_items parts, want at least 20 (the unmerged state was not reached)", label, active)
				}
				mark := serverNowMicros(t, ctx, direct)
				result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
					Time: window, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
				})
				if err != nil {
					t.Fatalf("%s %s: ReadFacts through the API-capped client: %v: %s\nfailed statements:\n%s", mode, label, err, readFailureCause(err), failedStatementsSince(ctx, direct, mark))
				}
				wantInt(t, factFor(t, result, teamSubject(teamID)).Fields, "project_reached_repository_count", 1)
				return linkRowStatementsSince(t, ctx, direct, mark)
			}

			for _, size := range orgLinksCeilingSizes {
				growTo(size)
				label := fmt.Sprintf("%d outside links", size)
				read(label + " warm-up")
				runs := []map[uint64]linkRowStatement{read(label), read(label + " again"), read(label + " third")}
				if len(runs[0]) < 4 {
					t.Fatalf("%s %s: %d link-row statements logged, want at least 4: the candidates and three link-row reads (the measurement did not happen)", mode, label, len(runs[0]))
				}
				candidates := 0
				for _, statement := range runs[0] {
					if statement.candidate {
						candidates++
					}
				}
				if candidates != 1 {
					t.Fatalf("%s %s: %d team link candidate statements logged, want 1", mode, label, candidates)
				}
				hashes := make([]uint64, 0, len(runs[0]))
				for hash := range runs[0] {
					hashes = append(hashes, hash)
				}
				sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
				for _, hash := range hashes {
					lowest, highest := runs[0][hash].peak, runs[0][hash].peak
					var readBytes uint64
					for _, run := range runs {
						statement, ok := run[hash]
						if !ok {
							t.Fatalf("%s %s: statement %d did not run in every read", mode, label, hash)
						}
						if !ranAtStatementThreads(statement.threads) {
							t.Fatalf("%s %s: statement %d ran with max_threads %q (server default %q), want %s (the peak would measure the runner)", mode, label, hash, statement.threads, serverThreads, wantThreads)
						}
						lowest, highest = min(lowest, statement.peak), max(highest, statement.peak)
						readBytes = max(readBytes, statement.readBytes)
					}
					kind := "link rows"
					if runs[0][hash].candidate {
						kind = "candidates"
					}
					t.Logf("SLOPE %s: %s statement %d peak memory lowest %d, highest %d bytes, read_bytes highest %d at %d outside links (bound %d)", mode, kind, hash, lowest, highest, readBytes, size, orgLinksCeilingBound)
					if highest >= orgLinksCeilingBound {
						t.Errorf("%s %s: statement %d peaked at %d bytes, want under %d (half the API statement cap)", mode, label, hash, highest, orgLinksCeilingBound)
					}
				}
			}
		})
	}
}

// linkRowStatementsSince is every finished or failed SELECT after the mark
// that reads both the team project ownership and the link rows: per
// normalized query hash, its peak memory and the max_threads it ran with.
func linkRowStatementsSince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, markMicros int64) map[uint64]linkRowStatement {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT normalized_query_hash, max(memory_usage), max(read_bytes), any(Settings['max_threads']), max(position(query, 'SELECT groupUniqArray(cp.work_item_id) FROM work_items AS cp') > 0) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.team_project_ownership')) AND has(tables, concat(currentDatabase(), '.work_graph_issue_pr'))
GROUP BY normalized_query_hash`, markMicros)
	if err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	defer rows.Close()
	out := map[uint64]linkRowStatement{}
	for rows.Next() {
		var hash, peak, readBytes uint64
		var threads string
		var candidate uint8
		if err := rows.Scan(&hash, &peak, &readBytes, &threads, &candidate); err != nil {
			t.Fatalf("scan query_log: %v", err)
		}
		out[hash] = linkRowStatement{peak: peak, readBytes: readBytes, threads: threads, candidate: candidate == 1}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read query_log rows (measurement did not happen): %v", err)
	}
	return out
}
