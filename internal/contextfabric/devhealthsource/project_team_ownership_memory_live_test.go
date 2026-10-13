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

// Prod shape of the largest org: team_project_ownership carries about 770
// distinct valid_from rows per (project, team) pair.
const (
	memoryShapeProjects    = 40
	memoryShapeTeamsPerOrg = 6
	memoryShapeVersions    = 770
	memoryShapeUnmerged    = 12
	memoryShapeRuns        = 3
)

func seedProjectTeamOwnershipShape(t *testing.T, f *ingestColumnsFixture, unmergedParts int) {
	t.Helper()
	synced := f.old
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced)
SELECT concat('P-', toString(number)), ?, 'linear', concat('K', toString(number)), concat('project ', toString(number)), 1, 'started', '', ?, ? FROM numbers(?)`,
		f.h.orgID, f.old, synced, uint64(memoryShapeProjects))
	for team := 0; team < memoryShapeTeamsPerOrg; team++ {
		f.team(fmt.Sprintf("T-%d", team), f.old, synced)
	}
	slices := unmergedParts
	if slices < 1 {
		slices = 1
	}
	per := (memoryShapeVersions + slices - 1) / slices
	for s := 0; s < slices; s++ {
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced)
SELECT ?, 'linear', concat('T-', toString(t.number)), concat('P-', toString(p.number)), concat('K', toString(p.number)), 'native',
  toDateTime64(?, 3, 'UTC') + toIntervalDay(v.number), if(v.number = ? - 1, NULL, toDateTime64(?, 3, 'UTC') + toIntervalDay(v.number + 1)), ?, ?
FROM numbers(?) AS p, numbers(?) AS t, (SELECT number FROM numbers(?) WHERE number >= ? AND number < ?) AS v
WHERE (p.number % ?) = t.number`,
			f.h.orgID, f.old.Add(-800*24*time.Hour), uint64(memoryShapeVersions), f.old.Add(-800*24*time.Hour), f.old.Add(time.Duration(s)*time.Second), synced,
			uint64(memoryShapeProjects), uint64(memoryShapeTeamsPerOrg), uint64(memoryShapeVersions), uint64(s*per), uint64((s+1)*per), uint64(memoryShapeTeamsPerOrg))
	}
}

type fragmentPeak struct {
	name     string
	mib      float64
	readRows uint64
	rows     int
}

func measureProjectTeamsFragments(t *testing.T, f *ingestColumnsFixture, query contextpacket.ClickHouseQueryClient, stage string) []fragmentPeak {
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
			rows, err := query.Query(f.ctx, frag.SQL+"\nSETTINGS log_comment = '"+tag+"'", bindings)
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
	fmt.Fprintf(&b, "project-team ownership statement, stage=%s (min of %d warm runs; %d projects x %d versions)\n", stage, memoryShapeRuns, memoryShapeProjects, memoryShapeVersions)
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
	unmerged := measureProjectTeamsFragments(t, f, query, "unmerged")
	logFragmentTable(t, "unmerged", unmerged)
	mustExec(t, f.ctx, direct, "SYSTEM START MERGES team_project_ownership")
	for _, table := range []string{"team_project_ownership", "projects", "teams"} {
		mustExec(t, f.ctx, direct, "OPTIMIZE TABLE "+table+" FINAL")
	}
	merged := measureProjectTeamsFragments(t, f, query, "optimized")
	logFragmentTable(t, "optimized", merged)
}
