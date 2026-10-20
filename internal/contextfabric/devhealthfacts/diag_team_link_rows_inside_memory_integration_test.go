package devhealthfacts_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

const diagInsideOrg = "9d2c6e41-7b3a-4f58-a0e9-3c81b5d7f264"

// Diagnostic only (do not merge): per-clause memory of the three team link-row
// reader statements where every link is inside the owned projects. It fails
// on purpose so the job log carries its lines.
func TestDiagTeamLinkRowsInsideOwnedProjectsMemoryOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("diagnostic runs in the unit legs")
	}
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
	org, at := diagInsideOrg, ts(2026, 9, 29, 0, 0, 0)
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
SELECT concat('owned-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', ?, NULL FROM numbers(?, ?)`,
				repoUUID("repo-filler"), org, at, project, first, n)
			exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1000), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?, ?)`,
				repoUUID("repo-reached"), org, at, at, first, n)
			exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('owned-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`,
				repoUUID("repo-reached"), at, org, first, n)
		}
		grown = size
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
		for _, table := range []string{"work_items", "work_graph_issue_pr", "git_pull_requests", "team_project_ownership", "team_repo_ownership"} {
			exec(`OPTIMIZE TABLE ` + table + ` FINAL`)
		}
	}
	type measured struct {
		lowest, highest, readBytes uint64
		events                     string
	}
	measure := func(runCtx context.Context, marker, statement string, bindings []readers.Binding) measured {
		t.Helper()
		tagged := strings.Replace(statement, "SELECT ", "SELECT /* "+marker+" */ ", 1)
		run := func() {
			if err := readers.QueryOrgScopedNamed(runCtx, query, "DiagLinkRows", tagged, org, []string{teamID}, func(readers.RowScanner) error { return nil }, bindings...); err != nil {
				t.Errorf("%s: %v: %s", marker, err, readFailureCause(err))
			}
		}
		run()
		mark := serverNowMicros(t, ctx, direct)
		for range 3 {
			run()
		}
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs: %v", err)
		}
		var m measured
		if err := direct.QueryRow(ctx, `SELECT min(memory_usage), max(memory_usage), max(read_bytes),
  toString(argMax(mapFilter((k, v) -> match(k, 'Set|Join|External|Hash|Memory|Arena'), ProfileEvents), event_time_microseconds))
FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND position(query, ?) > 0 AND position(query, 'system.query_log') = 0`, mark, "/* "+marker+" */").Scan(&m.lowest, &m.highest, &m.readBytes, &m.events); err != nil {
			t.Fatalf("%s: read query_log: %v", marker, err)
		}
		return m
	}
	named := func(bindings []readers.Binding) []any {
		out := []any{clickhouse.Named("org_id", org), clickhouse.Named("ids", []string{teamID})}
		for _, b := range bindings {
			out = append(out, clickhouse.Named(b.Name, b.Value))
		}
		return out
	}
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59)
	reach := []string{repoUUID("repo-reached")}
	for _, size := range []int{24_000, 96_000} {
		growTo(size)
		candidates, err := devhealthfacts.DiagTeamLinkCandidatesForTest(ctx, query, org, []string{teamID})
		if err != nil {
			t.Fatalf("candidates: %v", err)
		}
		table, err := devhealthfacts.TeamLinkCandidatesTableForTest(candidates)
		if err != nil {
			t.Fatal(err)
		}
		sent := clickhouse.Context(ctx, clickhouse.WithExternalTable(table))
		cand := measure(ctx, fmt.Sprintf("diag-%d-candidates", size), devhealthfacts.TeamLinkCandidatesStatementForTest(), nil)
		t.Logf("DIAG %d candidates statement: %d ids, lowest %d highest %d bytes, read_bytes %d, events %s", size, len(candidates), cand.lowest, cand.highest, cand.readBytes, cand.events)
		for _, variant := range devhealthfacts.DiagLinkRowsVariants {
			runCtx := ctx
			if devhealthfacts.DiagLinkRowsUsesTable(variant) {
				runCtx = sent
			}
			var sum uint64
			var rows []string
			for _, s := range devhealthfacts.DiagReaderStatementsForTest(variant, start, end, reach, candidates) {
				m := measure(runCtx, fmt.Sprintf("diag-%d-%s-%s", size, variant, s.Reader), s.SQL, s.Bindings)
				sum += m.lowest
				rows = append(rows, fmt.Sprintf("%s %d", s.Reader, m.lowest))
				t.Logf("DIAG %d %s %s: lowest %d highest %d bytes, read_bytes %d, events %s", size, variant, s.Reader, m.lowest, m.highest, m.readBytes, m.events)
			}
			withCandidates := sum
			if devhealthfacts.DiagLinkRowsUsesTable(variant) {
				withCandidates += 3 * cand.lowest
			}
			t.Logf("DIAG %d %s SUM of the three readers (lowest): %d bytes; with the candidate statement once per reader: %d bytes [%s]", size, variant, sum, withCandidates, strings.Join(rows, ", "))
		}
		if size == 96_000 {
			for _, variant := range []string{"V0", "VM"} {
				runCtx := ctx
				if devhealthfacts.DiagLinkRowsUsesTable(variant) {
					runCtx = sent
				}
				s := devhealthfacts.DiagReaderStatementsForTest(variant, start, end, reach, candidates)[0]
				rows, err := direct.Query(runCtx, "EXPLAIN PIPELINE "+s.SQL, named(s.Bindings)...)
				if err != nil {
					t.Errorf("EXPLAIN PIPELINE %s: %v", variant, err)
					continue
				}
				var lines []string
				for rows.Next() {
					var line string
					if err := rows.Scan(&line); err != nil {
						t.Fatalf("scan explain: %v", err)
					}
					lines = append(lines, line)
				}
				rows.Close()
				t.Logf("DIAG PIPELINE %s reach:\n%s", variant, strings.Join(slices.Clip(lines), "\n"))
			}
		}
	}
	t.Errorf("diagnostic only: this test fails on purpose so its DIAG lines print; never merge")
}
