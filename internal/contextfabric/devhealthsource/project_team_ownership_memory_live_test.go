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

	// Worst case (unmerged parts) must stay at or under this; main measured
	// 70.5 MiB unmerged and 72.8 MiB merged at this shape.
	memoryBoundMiB = 48.0
)

func seedProjectTeamOwnershipShape(t *testing.T, f *ingestColumnsFixture, rows, unmergedParts int) {
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
	per := (rows + slices - 1) / slices
	base := f.old.Add(-800 * 24 * time.Hour)
	lastVersion := (rows - 1) / memoryShapePairs
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
			uint64(rows), uint64(s*per), uint64((s+1)*per))
	}
}

type fragmentPeak struct {
	name     string
	mib      float64
	readRows uint64
	rows     int
}

func measureProjectTeamsFragments(t *testing.T, f *ingestColumnsFixture, query, projectorCapped contextpacket.ClickHouseQueryClient, stage string, only map[string]bool) []fragmentPeak {
	t.Helper()
	var out []fragmentPeak
	for _, frag := range devhealthsource.ProjectTeamsFragmentsForTest(true) {
		if only != nil && !only[frag.Name] {
			continue
		}
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

func peakOf(t *testing.T, peaks []fragmentPeak, name string) fragmentPeak {
	t.Helper()
	for _, p := range peaks {
		if p.name == name {
			return p
		}
	}
	t.Fatalf("no measurement named %s", name)
	return fragmentPeak{}
}

func fragmentTable(stage string, peaks []fragmentPeak) string {
	var b strings.Builder
	b.WriteString("settings: paged statements = prod projector settings (max_memory_usage 192 MiB, max_bytes_to_read 256 MiB, driver max_result_rows); other fragments = diagnostic, max_result_rows=0 and max_memory_usage=0\n")
	fmt.Fprintf(&b, "project-team ownership statement, stage=%s (min of %d warm runs; %d pairs)\n", stage, memoryShapeRuns, memoryShapePairs)
	for _, p := range peaks {
		fmt.Fprintf(&b, "  %-24s peak=%7.2f MiB read_rows=%9d rows_out=%d\n", p.name, p.mib, p.readRows, p.rows)
	}
	return b.String()
}

// The statement the projector sends for the project-team ownership edge, on
// real ClickHouse at the largest prod org's ownership volume, under the
// projector's own statement settings. The worst case is merges stopped with
// several unmerged parts; the other stage is after OPTIMIZE FINAL.
func TestProjectTeamOwnershipStatementMemoryOnTheProdShape(t *testing.T) {
	query, direct := orgIsolationClickHouseFixture(t)
	f := newIngestColumnsFixture(t, "95460000-0000-4000-8000-000000000001", nil, nil)
	seedProjectTeamOwnershipShape(t, f, memoryShapeRows, memoryShapeUnmerged)
	capped := contextpacket.NewMemoryCappedQueryClient(query, prodMaxMemoryUsage)
	t.Cleanup(func() {
		// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
		_ = direct.Exec(context.Background(), "SYSTEM START MERGES team_project_ownership")
	})
	// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
	mustExec(t, f.ctx, direct, "SYSTEM STOP MERGES team_project_ownership")
	unmerged := measureProjectTeamsFragments(t, f, query, capped, "unmerged", nil)
	// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
	mustExec(t, f.ctx, direct, "SYSTEM START MERGES team_project_ownership")
	// devhealthschema:not-a-production-replica these statements maintain tables the production DDL created; no column, type or engine is declared here.
	for _, table := range []string{"team_project_ownership", "projects", "teams"} {
		mustExec(t, f.ctx, direct, "OPTIMIZE TABLE "+table+" FINAL")
	}
	merged := measureProjectTeamsFragments(t, f, query, capped, "optimized", nil)
	t.Log(fragmentTable("unmerged", unmerged) + fragmentTable("optimized", merged))

	for stage, peaks := range map[string][]fragmentPeak{"unmerged": unmerged, "optimized": merged} {
		paged := peakOf(t, peaks, "paged_statement")
		raw := peakOf(t, peaks, "ownership_raw_final_x1")
		if paged.mib > memoryBoundMiB {
			t.Errorf("%s: the paged statement peaked at %.2f MiB, bound %.0f MiB", stage, paged.mib, memoryBoundMiB)
		}
		// The ownership table is read once: the statement's whole read is the
		// raw FINAL read plus the small projects and teams tables.
		if limit := raw.readRows + raw.readRows/10 + 2000; paged.readRows > limit {
			t.Errorf("%s: the paged statement read %d rows, want at most %d (one read of the %d ownership rows)", stage, paged.readRows, limit, raw.readRows)
		}
	}
}

// The statement's memory must not follow the length of the ownership history:
// the same 88 pairs with 100 versus 1,000 versions per pair.
func TestProjectTeamOwnershipStatementMemoryDoesNotGrowWithVersions(t *testing.T) {
	query, direct := orgIsolationClickHouseFixture(t)
	capped := contextpacket.NewMemoryCappedQueryClient(query, prodMaxMemoryUsage)
	only := map[string]bool{"paged_statement": true}
	var small, large float64
	for i, versions := range []int{100, 1000} {
		f := newIngestColumnsFixture(t, fmt.Sprintf("95460000-0000-4000-8000-00000000010%d", i), nil, nil)
		seedProjectTeamOwnershipShape(t, f, memoryShapePairs*versions, memoryShapeUnmerged)
		// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
		mustExec(t, f.ctx, direct, "OPTIMIZE TABLE team_project_ownership FINAL")
		peak := peakOf(t, measureProjectTeamsFragments(t, f, query, capped, fmt.Sprintf("slope%d", versions), only), "paged_statement")
		t.Logf("slope: %d versions per pair (%d rows): paged statement peak %.2f MiB read_rows=%d", versions, memoryShapePairs*versions, peak.mib, peak.readRows)
		if i == 0 {
			small = peak.mib
		} else {
			large = peak.mib
		}
	}
	if large > 1.15*small && large > small+2 {
		t.Errorf("memory grows with the ownership history: %.2f MiB at 100 versions per pair, %.2f MiB at 1,000 (fail when large > 1.15x small and large > small + 2 MiB)", small, large)
	}
}
