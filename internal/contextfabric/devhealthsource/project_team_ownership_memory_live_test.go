package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// Prod shape of the largest org (read-only counts 2026-10-13): 67,486
// team_project_ownership rows over 88 (team, project) pairs, 36 teams, 48
// project ids; 47 project rows.
const (
	memoryShapeRows     = 67486
	memoryShapePairs    = 88
	memoryShapeTeams    = 36
	memoryShapeProjects = 48
	memoryShapeOpen     = 60
	memoryShapeUnmerged = 12
	memoryShapeRuns     = 3

	// The projector's statement settings in prod: max_memory_usage 192 MiB from
	// cmd/acr-projector/runtime.go projectorStatementMaxMemoryUsage, max_bytes_to_read
	// 256 MiB (config.DefaultClickHouseMaxBytesToRead), driver-default max_result_rows.
	prodMaxMemoryUsage = 192 << 20
	prodMaxBytesToRead = 256 << 20
)

func seedProjectTeamOwnershipShape(t *testing.T, f *ingestColumnsFixture, unmergedParts int) {
	t.Helper()
	synced := f.old
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced)
SELECT concat('P-', toString(number)), ?, 'linear', concat('K', toString(number)), concat('project ', toString(number)), 1, 'started', '', ?, ? FROM numbers(?)`,
		f.h.orgID, f.old, synced, uint64(memoryShapeProjects-1))
	for team := 0; team < memoryShapeTeams; team++ {
		f.team(fmt.Sprintf("T-%d", team), f.old, synced)
	}
	slices := unmergedParts
	if slices < 1 {
		slices = 1
	}
	per := (memoryShapeRows + slices - 1) / slices
	base := f.old.Add(-800 * 24 * time.Hour)
	lastVersion := (memoryShapeRows - 1) / memoryShapePairs
	for s := 0; s < slices; s++ {
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced)
SELECT ?, 'linear', concat('T-', toString((n % ?) % ?)), concat('P-', toString((n % ?) % ?)), concat('K', toString((n % ?) % ?)), 'native',
  toDateTime64(?, 3, 'UTC') + toIntervalHour(intDiv(n, ?)),
  if((n % ?) < ? AND intDiv(n, ?) >= ?, NULL, toDateTime64(?, 3, 'UTC') + toIntervalHour(intDiv(n, ?)) + toIntervalMinute(30)),
  ?, ?
FROM (SELECT number AS n FROM numbers(?) WHERE number >= ? AND number < ?)`,
			f.h.orgID,
			uint64(memoryShapePairs), uint64(memoryShapeTeams), uint64(memoryShapePairs), uint64(memoryShapeProjects), uint64(memoryShapePairs), uint64(memoryShapeProjects),
			base, uint64(memoryShapePairs),
			uint64(memoryShapePairs), uint64(memoryShapeOpen), uint64(memoryShapePairs), uint64(lastVersion), base, uint64(memoryShapePairs),
			f.old.Add(time.Duration(s)*time.Second), synced,
			uint64(memoryShapeRows), uint64(s*per), uint64((s+1)*per))
	}
}

type fragmentPeak struct {
	name     string
	mib      float64
	readRows uint64
	rows     int
}

func measureProjectTeamsFragments(t *testing.T, f *ingestColumnsFixture, query, projectorCapped contextpacket.ClickHouseQueryClient, stage string) []fragmentPeak {
	t.Helper()
	var out []fragmentPeak
	for _, frag := range devhealthsource.ProjectTeamsFragmentsForTest(true) {
		best := fragmentPeak{name: frag.Name, mib: -1}
		for run := 0; run < memoryShapeRuns; run++ {
			tag := fmt.Sprintf("pto-%s-%s-%d-%d", stage, frag.Name, run, time.Now().UnixNano())
			bindings := []contextpacket.ClickHouseBinding{{Name: "org_id", Value: f.h.orgID}}
			if frag.Paged {
				bindings = devhealthsource.ProjectTeamsPagedBindingsForTest(f.h.orgID, 500)
			}
			client, settings := query, "max_result_rows = 0, max_memory_usage = 0"
			if frag.Paged {
				client, settings = projectorCapped, fmt.Sprintf("max_bytes_to_read = %d", prodMaxBytesToRead)
			}
			rows, err := client.Query(f.ctx, frag.SQL+"\nSETTINGS "+settings+", log_comment = '"+tag+"'", bindings)
			if err != nil {
				t.Fatalf("%s %s: %v", stage, frag.Name, err)
			}
			n := 0
			for rows.Next() {
				n++
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("%s %s drain: %v", stage, frag.Name, err)
			}
			_ = rows.Close()
			mustExec(t, f.ctx, f.h.direct, "SYSTEM FLUSH LOGS")
			r, err := f.h.direct.Query(f.ctx, `SELECT memory_usage, read_rows FROM system.query_log WHERE type = 'QueryFinish' AND log_comment = ? ORDER BY event_time DESC LIMIT 1`, tag)
			if err != nil {
				t.Fatalf("query_log: %v", err)
			}
			var mem, readRows uint64
			if !r.Next() {
				_ = r.Close()
				t.Fatalf("%s %s: no query_log row for %s", stage, frag.Name, tag)
			}
			if err := r.Scan(&mem, &readRows); err != nil {
				t.Fatal(err)
			}
			_ = r.Close()
			mib := float64(mem) / (1 << 20)
			if best.mib < 0 || mib < best.mib {
				best = fragmentPeak{name: frag.Name, mib: mib, readRows: readRows, rows: n}
			}
		}
		out = append(out, best)
	}
	return out
}

func logFragmentTable(t *testing.T, stage string, peaks []fragmentPeak) {
	var b strings.Builder
	b.WriteString("settings: paged_statement = prod projector settings (max_memory_usage 192 MiB, max_bytes_to_read 256 MiB, driver max_result_rows); other fragments = diagnostic, max_result_rows=0 and max_memory_usage=0\n")
	fmt.Fprintf(&b, "project-team ownership statement, stage=%s (min of %d warm runs; %d rows, %d pairs)\n", stage, memoryShapeRuns, memoryShapeRows, memoryShapePairs)
	for _, p := range peaks {
		fmt.Fprintf(&b, "  %-24s peak=%7.2f MiB read_rows=%9d rows_out=%d\n", p.name, p.mib, p.readRows, p.rows)
	}
	t.Log(b.String())
}

func TestProjectTeamOwnershipFragmentMemoryTable(t *testing.T) {
	query, direct := orgIsolationClickHouseFixture(t)
	f := newIngestColumnsFixture(t, "95460000-0000-4000-8000-000000000001", nil, nil)
	seedProjectTeamOwnershipShape(t, f, memoryShapeUnmerged)
	t.Cleanup(func() {
		_ = direct.Exec(context.Background(), "SYSTEM START MERGES team_project_ownership")
	})
	mustExec(t, f.ctx, direct, "SYSTEM STOP MERGES team_project_ownership")
	capped := contextpacket.NewMemoryCappedQueryClient(query, prodMaxMemoryUsage)
	unmerged := measureProjectTeamsFragments(t, f, query, capped, "unmerged")
	logFragmentTable(t, "unmerged", unmerged)
	mustExec(t, f.ctx, direct, "SYSTEM START MERGES team_project_ownership")
	for _, table := range []string{"team_project_ownership", "projects", "teams"} {
		mustExec(t, f.ctx, direct, "OPTIMIZE TABLE "+table+" FINAL")
	}
	merged := measureProjectTeamsFragments(t, f, query, capped, "optimized")
	logFragmentTable(t, "optimized", merged)
}
