package devhealthfacts_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

func TestDiagnosticLinkRowComponentMemory(t *testing.T) {
	ctx := context.Background()
	_, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, nil)
	for _, statement := range devhealthschema.DDL("repos", "teams", "work_items", "work_graph_issue_pr", "git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	const org = "9b1d4e72-8c35-4a60-b2f1-3d7e0a95c468"
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, statement)
		}
	}
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, "proj-owner", org, "linear", nil, "p", uint8(1), "active", "", at)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, org, "linear", "team-owner", "proj-owner", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES ('i-reached', ?, ?, 't', 'open', '', ?, '', 'linear', 'proj-owner', NULL)`, repoUUID("repo-f"), org, at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,1,'pr','open',?,?,NULL,NULL,'f','')`, repoUUID("repo-r"), org, at, at)
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, 'i-reached', 1, 1, 'native', '', ?, ?)`, repoUUID("repo-r"), at, org)
	var from uint64
	grow := func(n int) {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('filler-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?, ?)`, repoUUID("repo-f"), org, at, from, uint64(n))
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1000), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?, ?)`, repoUUID("repo-f"), org, at, at, from, uint64(n))
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('filler-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`, repoUUID("repo-f"), at, org, from, uint64(n))
		from += uint64(n)
	}
	var lines []string
	report := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	setIn := `work_item_id IN (SELECT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String})`
	components := map[string]string{
		"v01_current_set_final_project":      `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND ` + setIn + ` AND project_id = 'proj-owner'`,
		"v02_distinct_set":                   `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN (SELECT DISTINCT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String}) AND project_id = 'proj-owner'`,
		"v03_two_pass_nonfinal_then_final":   `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN (SELECT work_item_id FROM work_items WHERE org_id = {org_id:String} AND ` + setIn + ` AND project_id = 'proj-owner') AND project_id = 'proj-owner'`,
		"v04_nonfinal_set_project_only":      `SELECT count() FROM work_items WHERE org_id = {org_id:String} AND ` + setIn + ` AND project_id = 'proj-owner'`,
		"v05_hashed_set_no_pk_prune":         `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND cityHash64(work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String}) AND project_id = 'proj-owner'`,
		"v06_set_without_index_use":          `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND ` + setIn + ` AND project_id = 'proj-owner' SETTINGS use_index_for_in_with_subqueries = 0`,
		"v07_project_prewhere_nonfinal_only": `SELECT count() FROM work_items PREWHERE project_id = 'proj-owner' WHERE org_id = {org_id:String}`,
		"v08_links_final_distinct_ids":       `SELECT count() FROM (SELECT DISTINCT work_item_id FROM work_graph_issue_pr FINAL WHERE org_id = {org_id:String})`,
		"v09_semi_join_set_side_right":       `SELECT count() FROM work_items FINAL LEFT SEMI JOIN (SELECT DISTINCT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String}) AS k ON k.work_item_id = work_items.work_item_id WHERE work_items.org_id = {org_id:String} AND work_items.project_id = 'proj-owner'`,
		"v10_new_statement":                  `SELECT count() FROM ` + devhealthfacts.TeamProjectLinkRowsSQLForTest(),
		"v11_old_statement":                  `SELECT count() FROM ` + devhealthfacts.TeamProjectLinkRowsPreStreamingSQLForTest(),
	}
	measure := func(size int) {
		for name, q := range components {
			mark := serverNowMicros(t, ctx, direct)
			rows, err := direct.Query(clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"log_comment": name})), q, clickhouse.Named("org_id", org), clickhouse.Named("ids", []string{"team-owner"}))
			if err != nil {
				report("DIAG %s at %d: error %v", name, size, err)
				continue
			}
			for rows.Next() {
			}
			rows.Close()
			exec(`SYSTEM FLUSH LOGS`)
			var mem, read uint64
			if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_bytes) FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND log_comment = ?`, mark, name).Scan(&mem, &read); err != nil {
				report("DIAG %s at %d: log %v", name, size, err)
				continue
			}
			report("DIAG %s at %d links: memory=%d read_bytes=%d", name, size, mem, read)
		}
	}
	grow(30_000)
	measure(30_000)
	grow(60_000)
	measure(90_000)
	for _, table := range []string{"work_items", "work_graph_issue_pr", "git_pull_requests"} {
		exec(`OPTIMIZE TABLE ` + table + ` FINAL`)
	}
	measure(-90_000)
	sort.Strings(lines)
	t.Errorf("diagnostic table (failing on purpose so the log shows it):\n%s", strings.Join(lines, "\n"))
}
