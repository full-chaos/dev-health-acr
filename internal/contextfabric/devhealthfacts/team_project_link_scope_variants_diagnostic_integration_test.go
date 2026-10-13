package devhealthfacts_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

func TestDiagnosticLinkScopeVariants(t *testing.T) {
	ctx := context.Background()
	_, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, nil)
	for _, statement := range devhealthschema.DDL("repos", "teams", "work_items", "work_graph_issue_pr", "git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	const org = "7d2a95c1-4e68-4b03-9f17-a8c5e0d36b92"
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, statement)
		}
	}
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES ('i-reached', ?, ?, 't', 'open', '', ?, '', 'linear', 'proj-owner', NULL)`, repoUUID("repo-f"), org, at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,1,'pr','open',?,?,NULL,NULL,'f','')`, repoUUID("repo-r"), org, at, at)
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, 'i-reached', 1, 1, 'native', '', ?, ?)`, repoUUID("repo-r"), at, org)
	var unlinked, linked uint64
	const batches = 20
	growUnlinked := func(n int) {
		for b := 0; b < batches; b++ {
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('unlinked-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?, ?)`, repoUUID("repo-f"), org, at, unlinked, uint64(n/batches))
			unlinked += uint64(n / batches)
		}
	}
	growLinked := func(n int) {
		for b := 0; b < batches; b++ {
			c := uint64(n / batches)
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('linked-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'open', '', ?, '', 'linear', '', NULL FROM numbers(?, ?)`, repoUUID("repo-f"), org, at, linked, c)
			exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1000), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?, ?)`, repoUUID("repo-f"), org, at, at, linked, c)
			exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('linked-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`, repoUUID("repo-f"), at, org, linked, c)
			linked += c
		}
	}
	setIn := `work_item_id IN (SELECT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String})`
	variants := map[string]string{
		"v01_in_set":       `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND ` + setIn + ` AND project_id = 'proj-owner'`,
		"v03_two_pass":     `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN (SELECT work_item_id FROM work_items WHERE org_id = {org_id:String} AND ` + setIn + ` AND project_id = 'proj-owner') AND project_id = 'proj-owner'`,
		"v05_hashed_set":   `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND cityHash64(work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String}) AND project_id = 'proj-owner'`,
		"v06_no_index_use": `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND ` + setIn + ` AND project_id = 'proj-owner' SETTINGS use_index_for_in_with_subqueries = 0`,
		"v09_semi_join":    `SELECT count() FROM work_items FINAL LEFT SEMI JOIN (SELECT DISTINCT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String}) AS k ON k.work_item_id = work_items.work_item_id WHERE work_items.org_id = {org_id:String} AND work_items.project_id = 'proj-owner'`,
		"v10_project_only": `SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND project_id = 'proj-owner'`,
	}
	var lines []string
	measure := func(stage string) {
		var parts uint64
		_ = direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE active AND database = currentDatabase() AND table = 'work_items'`).Scan(&parts)
		for name, q := range variants {
			bestMem, bestRead := ^uint64(0), uint64(0)
			for run := 0; run < 3; run++ {
				mark := serverNowMicros(t, ctx, direct)
				rows, err := direct.Query(clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"log_comment": name})), q, clickhouse.Named("org_id", org))
				if err != nil {
					lines = append(lines, fmt.Sprintf("DIAG2 %s %s: error %v", stage, name, err))
					break
				}
				for rows.Next() {
				}
				rows.Close()
				exec(`SYSTEM FLUSH LOGS`)
				var mem, read uint64
				if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_bytes) FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND log_comment = ?`, mark, name).Scan(&mem, &read); err != nil {
					lines = append(lines, fmt.Sprintf("DIAG2 %s %s: log %v", stage, name, err))
					break
				}
				if mem < bestMem {
					bestMem, bestRead = mem, read
				}
			}
			lines = append(lines, fmt.Sprintf("DIAG2 %s work_items_parts=%d unlinked=%d linked=%d %s: memory=%d read_bytes=%d", stage, parts, unlinked, linked, name, bestMem, bestRead))
		}
	}
	merge := func() {
		for _, table := range []string{"work_items", "work_graph_issue_pr", "git_pull_requests"} {
			exec(`OPTIMIZE TABLE ` + table + ` FINAL`)
		}
	}
	growUnlinked(250_000)
	measure("U1-unmerged")
	growUnlinked(375_000)
	measure("U2-unmerged")
	growLinked(30_000)
	measure("L1-unmerged")
	growLinked(60_000)
	measure("L2-unmerged")
	merge()
	measure("L2-merged")
	sort.Strings(lines)
	t.Errorf("diagnostic table (failing on purpose so the log shows it):\n%s", strings.Join(lines, "\n"))
}
