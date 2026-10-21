package devhealthfacts_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	candidateFormsOrg   = "5e2c9a17-4b83-4f60-9d21-7a8b3c6e0f94"
	candidateFormsReads = 3
)

// The candidate statement forms are measured on four shapes through the
// client the hosted API uses: every link inside the owned projects (96k, in 20
// parts and merged), the same over 100 parts, 24k owned linked issues in 10 and
// 20 unmerged versions, and 400k outside links (unmerged and merged). Each form
// must return exactly the per-version oracle's distinct ids. Peaks, durations,
// active work_items parts and the EXPLAIN PIPELINE are logged, not bounded.
func TestTeamLinkCandidateStatementFormsMeasuredAcrossShapesOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("memory measurement rows run in the unit legs; the race detector does not change a server peak")
	}
	started := time.Now()
	ctx := context.Background()
	rawQuery, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	query := statementthreads.API(rawQuery)
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	for _, statement := range devhealthschema.DDL("work_items", "work_graph_issue_pr", "projects", "team_project_ownership") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	org, at := candidateFormsOrg, ts(2026, 9, 29, 0, 0, 0)
	const teamID, project = "team-owner", "proj-owner"
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		project, org, "linear", nil, project, uint8(1), "active", "", at)
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		org, "linear", teamID, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	grown := []string{"work_items", "work_graph_issue_pr"}
	stopMerges := func() {
		for _, table := range grown {
			exec(`SYSTEM STOP MERGES ` + table)
		}
	}
	reset := func() {
		for _, table := range grown {
			exec(`TRUNCATE TABLE ` + table)
		}
		stopMerges()
	}
	merge := func() {
		for _, table := range grown {
			exec(`SYSTEM START MERGES ` + table)
			exec(`OPTIMIZE TABLE ` + table + ` FINAL`)
		}
		stopMerges()
	}
	workItems := func(prefix, projectID string, count, batches, version int) {
		for b := 0; b < batches; b++ {
			first, n := uint64(b*count/batches), uint64((b+1)*count/batches-b*count/batches)
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('`+prefix+`-', repeat('x', 96), '-', toString(number)), ?, ?, 'issue', 'open', '', ?, '', 'linear', ?, NULL, addSeconds(?, ?) FROM numbers(?, ?)`,
				repoUUID("repo-filler"), org, at, projectID, at, version, first, n)
		}
	}
	links := func(prefix string, count int) {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('`+prefix+`-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?)`,
			repoUUID("repo-reached"), at, org, uint64(count))
	}

	forms := devhealthfacts.TeamLinkCandidateFormsForTest()
	read := func(reader, statement string) []string {
		t.Helper()
		var ids []string
		if err := readers.QueryOrgScopedNamed(contextpacket.WithStatementReader(ctx, reader), query, "TeamLinkCandidateForms", statement, org, []string{teamID}, func(row readers.RowScanner) error {
			return row.Scan(&ids)
		}); err != nil {
			t.Fatalf("%s: %v: %s", reader, err, readFailureCause(err))
		}
		return ids
	}
	explain := func(statement string) string {
		t.Helper()
		explainCtx := clickhouse.Context(ctx,
			clickhouse.WithParameters(clickhouse.Parameters{"org_id": org, "ids": "['" + teamID + "']"}),
			clickhouse.WithSettings(clickhouse.Settings{"max_threads": contextpacket.StatementMaxThreads}))
		rows, err := direct.Query(explainCtx, "EXPLAIN PIPELINE "+statement)
		if err != nil {
			return "explain failed: " + err.Error()
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return "explain scan failed: " + err.Error()
			}
			lines = append(lines, strings.TrimSpace(line))
		}
		return strings.Join(lines, " | ")
	}
	measure := func(shape string, want int) {
		t.Helper()
		var active, activeRows uint64
		if err := direct.QueryRow(ctx, `SELECT count(), sum(rows) FROM system.parts WHERE database = currentDatabase() AND table = 'work_items' AND active`).Scan(&active, &activeRows); err != nil {
			t.Fatalf("count active parts: %v", err)
		}
		t.Logf("FORMS %s: active work_items parts=%d rows=%d", shape, active, activeRows)
		oracle := slices.Compact(slices.Sorted(slices.Values(read("forms-oracle", devhealthfacts.TeamLinkCandidatesPerVersionStatementForTest()))))
		if len(oracle) != want {
			t.Fatalf("%s: the per-version oracle holds %d distinct ids, want %d", shape, len(oracle), want)
		}
		for _, form := range forms {
			got := slices.Sorted(slices.Values(read("forms-"+form.Name, form.Statement)))
			if !slices.Equal(got, oracle) {
				t.Errorf("%s: form %s returned %d entries, not the oracle's %d distinct ids, each once", shape, form.Name, len(got), len(oracle))
			}
		}
		mark := serverNowMicros(t, ctx, direct)
		for range candidateFormsReads {
			for _, form := range forms {
				read("forms-"+form.Name, form.Statement)
			}
		}
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
		}
		for _, form := range forms {
			var lowest, highest, readBytes, durationMin, durationMax, rows, unbound uint64
			var threads string
			if err := direct.QueryRow(ctx, `SELECT min(memory_usage), max(memory_usage), max(read_bytes), min(query_duration_ms), max(query_duration_ms), count(), `+statementthreads.UnboundThreadsCount+`, any(Settings['max_threads']) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select' AND log_comment = ?`, mark, "forms-"+form.Name).Scan(&lowest, &highest, &readBytes, &durationMin, &durationMax, &rows, &unbound, &threads); err != nil {
				t.Fatalf("read query_log: %v", err)
			}
			if rows != candidateFormsReads {
				t.Fatalf("%s: form %s logged %d finished statements, want %d (the measurement did not happen)", shape, form.Name, rows, candidateFormsReads)
			}
			statementthreads.RequireThreadBound(t, ctx, direct, "team link candidate form "+form.Name, unbound)
			t.Logf("FORMS %s form=%s peak_min=%d peak_max=%d (%.1f%% of the API cap) read_bytes=%d duration_ms=%d..%d max_threads=%q parts=%d entries=%d",
				shape, form.Name, lowest, highest, 100*float64(lowest)/float64(contextpacket.APIStatementMaxMemoryUsage), readBytes, durationMin, durationMax, threads, active, want)
			t.Logf("FORMS-PLAN %s form=%s: %s", shape, form.Name, explain(form.Statement))
		}
	}

	stopMerges()
	workItems("owned", project, 96_000, 20, 0)
	links("owned", 96_000)
	measure("all-inside 96k unmerged", 96_000)
	merge()
	measure("all-inside 96k merged", 96_000)

	reset()
	workItems("owned", project, 96_000, 100, 0)
	links("owned", 96_000)
	measure("all-inside 96k over 100 parts", 96_000)

	reset()
	links("owned", 24_000)
	for v := 0; v < 10; v++ {
		workItems("owned", project, 24_000, 1, v)
	}
	measure("versions 24k x 10", 24_000)
	for v := 10; v < 20; v++ {
		workItems("owned", project, 24_000, 1, v)
	}
	measure("versions 24k x 20", 24_000)

	reset()
	workItems("outside", "", 400_000, 20, 0)
	links("outside", 400_000)
	workItems("owned", project, 1, 1, 0)
	links("owned", 1)
	measure("outside 400k unmerged", 1)
	merge()
	measure("outside 400k merged", 1)
	t.Logf("FORMS total %s", fmt.Sprint(time.Since(started).Round(time.Millisecond)))
}
