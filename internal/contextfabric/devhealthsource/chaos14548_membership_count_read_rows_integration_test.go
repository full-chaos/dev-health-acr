package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// DRAFT-ONLY diagnostic cell (CHAOS-14548), never merged: it measures the
// read rows of the BELONGS_TO_PROJECT edge count statement before and after
// repos rows are rewritten, and fails on purpose so CI prints its log.

const (
	c14548OrgA = "14548000-0000-4000-8000-00000000000a"
	c14548OrgB = "14548000-0000-4000-8000-00000000000b"
	c14548OrgC = "14548000-0000-4000-8000-00000000000c"
	c14548Base = "toDateTime64('2026-09-01 00:00:00', 3, 'UTC')"
)

func c14548RepoExpr(org, n string) string {
	return fmt.Sprintf("toUUID(concat('14548%s-0000-4000-8000-', leftPad(toString(%s), 12, '0')))", org[len(org)-3:], n)
}

func c14548ProjectExpr(n string) string {
	return fmt.Sprintf("concat('14548200-0000-4000-8000-', leftPad(toString(%s), 12, '0'))", n)
}

type c14548Run struct {
	label                                      string
	readRows, readBytes, parts, marks, selRows uint64
	memory                                     int64
}

func TestCHAOS14548MembershipCountReadRowsByRepositoryRewrite(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	exec := func(label, statement string) {
		t.Helper()
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	soft := func(label, statement string) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Logf("SOFT %s: %v", label, err)
		}
	}
	tables := []string{"repos", "work_items", "project_membership_transitions", "projects"}
	for _, table := range tables {
		soft("stop merges "+table, "SYSTEM STOP MERGES "+table)
	}

	var version string
	if err := direct.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("server version %s", version)

	// repos: 18 rows in 3 parts (org A 11, org B 4, org C 3).
	repoInsert := func(org string, from, count int, slug, stamp string) string {
		return fmt.Sprintf(`INSERT INTO repos (id, org_id, repo, provider, last_synced)
SELECT %s, '%s', concat('acme/%s-', toString(number)), 'linear', %s FROM numbers(%d, %d)`, c14548RepoExpr(org, "number"), org, slug, stamp, from, count)
	}
	exec("repos A", repoInsert(c14548OrgA, 0, 11, "repo", c14548Base))
	exec("repos B", repoInsert(c14548OrgB, 0, 4, "repo", c14548Base))
	exec("repos C", repoInsert(c14548OrgC, 0, 3, "repo", c14548Base))

	exec("projects", fmt.Sprintf(`INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at)
SELECT %s, if(number < 40, '%s', '%s'), concat('P', toString(number)), '', 'linear', 'started', '', 1, %s FROM numbers(45)`, c14548ProjectExpr("number"), c14548OrgA, c14548OrgB, c14548Base))

	// work_items: 4 parts, 40,000 rows of org A and 2,000 of org B; 3 of 5 carry a project.
	const workItemColumns = "INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, ingested_at) "
	workItemRows := func(org string, from, count int) string {
		return fmt.Sprintf(`SELECT concat('linear:W-', toString(number)), %s, '%s', 'w', 'open', '', '', 'linear', if(number %% 5 < 3, %s, ''), %s + toIntervalSecond(number), %s + toIntervalSecond(number + 3) FROM numbers(%d, %d)`,
			c14548RepoExpr(org, "number % 11"), org, c14548ProjectExpr("number % 40"), c14548Base, c14548Base, from, count)
	}
	for part := 0; part < 3; part++ {
		exec("work items A", workItemColumns+workItemRows(c14548OrgA, part*10000, 10000))
	}
	exec("work items A+B", workItemColumns+workItemRows(c14548OrgA, 30000, 10000)+" UNION ALL "+workItemRows(c14548OrgB, 0, 2000))

	// project_membership_transitions: 3 parts (add, move, move back) of 4,666
	// org A subjects, about 14,000 rows; the third part also holds 600 org B rows.
	transitions := func(org string, subjects, step int, from, to string) string {
		return fmt.Sprintf(`SELECT '%s', NULL, %s, 'work_item', concat('linear:W-', toString(number)), 'linear', %s, %s, '', '', '', %s + toIntervalSecond(number * 7 + %d), %s + toIntervalSecond(number * 7 + %d), concat('ev-', toString(number), '-%d'), %s + toIntervalSecond(number * 7 + %d + 11) FROM numbers(%d)`,
			org, c14548RepoExpr(org, "number % 11"), from, to, c14548Base, step, c14548Base, step, step, c14548Base, step, subjects)
	}
	steps := [][2]string{{"''", c14548ProjectExpr("number % 40")}, {c14548ProjectExpr("number % 40"), c14548ProjectExpr("(number + 1) % 40")}, {c14548ProjectExpr("(number + 1) % 40"), c14548ProjectExpr("number % 40")}}
	const transitionColumns = "INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) "
	for step, pair := range steps {
		statement := transitionColumns + transitions(c14548OrgA, 4666, step, pair[0], pair[1])
		if step == 2 {
			statement += " UNION ALL " + transitions(c14548OrgB, 600, step, pair[0], pair[1])
		}
		exec("transitions", statement)
	}

	statement, _ := devhealthsource.MembershipEdgeCountSQLForTest(true)
	bindings := []contextpacket.ClickHouseBinding{{Name: "org_id", Value: c14548OrgA}}
	token := fmt.Sprintf("c14548-%d", time.Now().UnixNano())
	var runs []c14548Run

	measure := func(phase string) {
		t.Helper()
		c14548LogParts(t, ctx, direct, phase, tables)
		for r := 1; r <= 2; r++ {
			comment := fmt.Sprintf("%s|%s|r%d", token, phase, r)
			rows, err := query.Query(ctx, statement+" SETTINGS log_comment = '"+comment+"', log_processors_profiles = 1", bindings)
			if err != nil {
				t.Fatalf("%s run %d: %v", phase, r, err)
			}
			var counts []string
			for rows.Next() {
				var source string
				var total uint64
				if err := rows.Scan(&source, &total); err != nil {
					t.Fatal(err)
				}
				counts = append(counts, fmt.Sprintf("%s=%d", source, total))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if r == 1 {
				t.Logf("%s edge counts: %s", phase, strings.Join(counts, " "))
			}
		}
		exec("flush logs", "SYSTEM FLUSH LOGS")
		logRows, err := direct.Query(ctx, `SELECT log_comment, query_id, read_rows, read_bytes, toInt64(memory_usage), ProfileEvents['SelectedParts'], ProfileEvents['SelectedMarks'], ProfileEvents['SelectedRows']
FROM system.query_log WHERE type = 'QueryFinish' AND startsWith(log_comment, ?) ORDER BY event_time_microseconds`, token+"|"+phase+"|")
		if err != nil {
			t.Fatal(err)
		}
		var lastID string
		for logRows.Next() {
			var run c14548Run
			var id string
			if err := logRows.Scan(&run.label, &id, &run.readRows, &run.readBytes, &run.memory, &run.parts, &run.marks, &run.selRows); err != nil {
				t.Fatal(err)
			}
			run.label = strings.TrimPrefix(run.label, token+"|")
			runs = append(runs, run)
			lastID = id
			t.Logf("RUN %s read_rows=%d read_bytes=%d memory=%d selected_parts=%d selected_marks=%d selected_rows=%d", run.label, run.readRows, run.readBytes, run.memory, run.parts, run.marks, run.selRows)
		}
		logRows.Close()
		if lastID == "" {
			t.Fatalf("%s: no query_log row", phase)
		}
		c14548LogLines(t, ctx, direct, phase+" READSTEP", `SELECT concat(plan_step_description, ' step=', toString(plan_step), ' out_rows=', toString(sum(output_rows)), ' out_bytes=', toString(sum(output_bytes)))
FROM system.processors_profile_log WHERE query_id = ? AND startsWith(name, 'MergeTreeSelect')
GROUP BY plan_step_description, plan_step ORDER BY plan_step_description, plan_step`, lastID)
		c14548LogLines(t, ctx, direct, phase+" READTABLE", `SELECT concat(plan_step_description, ' reads=', toString(count()), ' out_rows=', toString(sum(rows))) FROM (
SELECT plan_step_description, plan_step, sum(output_rows) AS rows FROM system.processors_profile_log WHERE query_id = ? AND startsWith(name, 'MergeTreeSelect') GROUP BY plan_step_description, plan_step)
GROUP BY plan_step_description ORDER BY plan_step_description`, lastID)
		c14548LogQueryLines(t, ctx, query, phase+" ESTIMATE", "SELECT toString(tuple(*)) FROM (EXPLAIN ESTIMATE "+statement+")", bindings, "")
		c14548LogQueryLines(t, ctx, query, phase+" PLAN", "SELECT explain FROM (EXPLAIN PLAN indexes = 1 "+statement+")", bindings, "ReadFromMergeTree|Join|Granules:|Parts:|Keys:|FINAL|Filter column")
	}

	measure("P0-baseline")

	// P1: the 11 org A rows rewritten, one insert (one part) per row, same key and
	// values, newer version: an ordinary ReplacingMergeTree rewrite.
	for i := 0; i < 11; i++ {
		exec("rewrite", repoInsert(c14548OrgA, i, 1, "repo", c14548Base+" + toIntervalHour(9)"))
	}
	measure("P1-rewrite-11-one-row-parts")

	soft("start merges repos", "SYSTEM START MERGES repos")
	exec("optimize repos", "OPTIMIZE TABLE repos FINAL")
	soft("stop merges repos", "SYSTEM STOP MERGES repos")
	measure("P2-repos-merged-one-part")

	// P3: 11 NEW repos keys in org A (29 rows after the merge).
	for i := 0; i < 11; i++ {
		exec("new keys", repoInsert(c14548OrgA, 100+i, 1, "new", c14548Base+" + toIntervalHour(10)"))
	}
	measure("P3-11-new-keys-unmerged")
	soft("start merges repos", "SYSTEM START MERGES repos")
	exec("optimize repos", "OPTIMIZE TABLE repos FINAL")
	soft("stop merges repos", "SYSTEM STOP MERGES repos")
	measure("P4-new-keys-merged-29-rows")

	// P5: the 11 org A rows rewritten with a changed slug (the only repos column
	// the statement reads besides the join keys).
	for i := 0; i < 11; i++ {
		exec("slug change", repoInsert(c14548OrgA, i, 1, "renamed", c14548Base+" + toIntervalHour(11)"))
	}
	measure("P5-slug-changed")

	// P6: control without repos: the other small inserts of the same hour
	// (work_items 107 rows in 5 parts, transitions 15 rows in 1 part).
	for part := 0; part < 5; part++ {
		exec("work items small", workItemColumns+workItemRows(c14548OrgA, 50000+part*30, 21))
	}
	exec("transitions small", transitionColumns+strings.Replace(transitions(c14548OrgA, 15, 3, "''", c14548ProjectExpr("number % 40")), "concat('linear:W-', toString(number))", "concat('linear:W-', toString(50000 + number))", 1))
	measure("P6-control-work-items-and-transitions")

	var summary []string
	base := runs[1].readRows
	for _, run := range runs {
		summary = append(summary, fmt.Sprintf("%s read_rows=%d (%+d vs P0 r2) marks=%d parts=%d memory=%d", run.label, run.readRows, int64(run.readRows)-int64(base), run.marks, run.parts, run.memory))
	}
	t.Fatalf("CHAOS-14548 DIAG CELL (draft only, fails on purpose to print the log), server %s:\n%s", version, strings.Join(summary, "\n"))
}

func c14548LogParts(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, phase string, tables []string) {
	t.Helper()
	c14548LogLines(t, ctx, direct, phase+" PARTS", `SELECT concat(table, ' parts=', toString(count()), ' rows=', toString(sum(rows)), ' marks=', toString(sum(marks)))
FROM system.parts WHERE database = currentDatabase() AND active AND has(?, table) GROUP BY table ORDER BY table`, tables)
}

func c14548LogLines(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label, statement string, args ...any) {
	t.Helper()
	rows, err := direct.Query(ctx, statement, args...)
	if err != nil {
		t.Logf("SOFT %s: %v", label, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Logf("SOFT %s scan: %v", label, err)
			return
		}
		t.Logf("%s %s", label, line)
	}
	if err := rows.Err(); err != nil {
		t.Logf("SOFT %s: %v", label, err)
	}
}

func c14548LogQueryLines(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, label, statement string, bindings []contextpacket.ClickHouseBinding, keep string) {
	t.Helper()
	rows, err := query.Query(ctx, statement, bindings)
	if err != nil {
		t.Logf("SOFT %s: %v", label, err)
		return
	}
	defer rows.Close()
	marks := strings.Split(keep, "|")
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Logf("SOFT %s scan: %v", label, err)
			return
		}
		if keep != "" {
			hit := false
			for _, mark := range marks {
				if strings.Contains(line, mark) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		t.Logf("%s %s", label, line)
	}
	if err := rows.Err(); err != nil {
		t.Logf("SOFT %s: %v", label, err)
	}
}
