package devhealthfacts_test

import (
	"context"
	"testing"
)

// Diagnostic only: peak memory of one narrowed work_items read over 250k
// filler rows, per read shape. It logs and never fails on a value.
func TestNarrowedWorkItemReadMemoryShapesOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := newCHAOS3780IntegrationClient(t, ctx)
	s := newProjectRollupSeed(t, ctx, direct)
	s.workItem("i-linked", "repo-a", "issue", "open", "proj-p")
	s.workItem("b-open", "repo-a", "issue", "open", "")
	s.blocks("b-open", "i-linked")
	s.exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('filler-', repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'issue', 'open', '', ?, ?, '', 'linear', 'proj-p', NULL, ? FROM numbers(?)`,
		repoUUID("repo-a"), projectRollupOrg, s.at, s.at, s.at, uint64(blockerMemoryFillerItems))
	endpoints := `(SELECT arrayJoin([be.source_work_item_id, be.target_work_item_id]) FROM work_item_dependencies AS be FINAL WHERE be.org_id = '` + projectRollupOrg + `' AND lower(ifNull(be.relationship_type, '')) = 'blocks')`
	shapes := map[string]string{
		"final":                   `SELECT count() FROM (SELECT org_id, work_item_id, status, type FROM work_items FINAL WHERE org_id = '` + projectRollupOrg + `' AND work_item_id IN ` + endpoints + `)`,
		"final_prewhere":          `SELECT count() FROM (SELECT org_id, work_item_id, status, type FROM work_items FINAL WHERE org_id = '` + projectRollupOrg + `' AND work_item_id IN ` + endpoints + `) SETTINGS optimize_move_to_prewhere_if_final = 1`,
		"final_block8k":           `SELECT count() FROM (SELECT org_id, work_item_id, status, type FROM work_items FINAL WHERE org_id = '` + projectRollupOrg + `' AND work_item_id IN ` + endpoints + `) SETTINGS max_block_size = 8192`,
		"final_explicit_prewhere": `SELECT count() FROM (SELECT org_id, work_item_id, status, type FROM work_items FINAL PREWHERE work_item_id IN ` + endpoints + ` WHERE org_id = '` + projectRollupOrg + `')`,
		"nofinal_argmax":          `SELECT count() FROM (SELECT repo_id, work_item_id, argMax(status, last_synced), argMax(type, last_synced) FROM work_items WHERE org_id = '` + projectRollupOrg + `' AND work_item_id IN ` + endpoints + ` GROUP BY repo_id, work_item_id)`,
		"nofinal_argmax_block8k":  `SELECT count() FROM (SELECT repo_id, work_item_id, argMax(status, last_synced), argMax(type, last_synced) FROM work_items WHERE org_id = '` + projectRollupOrg + `' AND work_item_id IN ` + endpoints + ` GROUP BY repo_id, work_item_id) SETTINGS max_block_size = 8192`,
		"auth_groupby_final":      `SELECT count() FROM (SELECT w.work_item_id FROM work_items AS w FINAL WHERE w.org_id = '` + projectRollupOrg + `' AND w.work_item_id IN ` + endpoints + ` GROUP BY w.work_item_id)`,
		"full_scan_final":         `SELECT count() FROM (SELECT org_id, work_item_id, status, type FROM work_items FINAL WHERE org_id = '` + projectRollupOrg + `')`,
	}
	for name, statement := range shapes {
		tagged := "/* narrowed-diag:" + name + " */ " + statement
		var n uint64
		if err := direct.QueryRow(ctx, tagged).Scan(&n); err != nil {
			t.Logf("DIAG shape=%s error=%v", name, err)
		}
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT extract(query, 'narrowed-diag:([a-z0-9_]+)'), memory_usage, read_rows, read_bytes FROM system.query_log
WHERE type IN ('QueryFinish', 'ExceptionWhileProcessing') AND position(query, 'narrowed-diag:') > 0 AND position(query, 'system.query_log') = 0 ORDER BY 1`)
	if err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name string
		var memory, readRows, readBytes uint64
		if err := rows.Scan(&name, &memory, &readRows, &readBytes); err != nil {
			t.Fatal(err)
		}
		seen++
		t.Logf("DIAG shape=%s peak_bytes=%d read_rows=%d read_bytes=%d", name, memory, readRows, readBytes)
	}
	if seen == 0 {
		t.Fatal("no diagnostic statement in system.query_log (measurement did not happen)")
	}
}
