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
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
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

	// The 12-part layout is bounded by a line in versions per pair. Measured
	// paged-statement peaks (CI, prod settings): 17.2-17.4 MiB at 100, 18.7-21.4
	// at 1,000, 31.5-35.3 at 2,000. FINAL over unmerged overlapping parts is
	// what grows (the raw FINAL read of the table alone goes 7.5 -> 17.7 MiB
	// from 1,000 to 2,000), so the line has margin over every measured point:
	// 20 + 0.0125 x versions = 21.3 / 32.5 / 45.0 MiB. Main's statement
	// measures 70+ MiB at 767 versions and ~85 at 1,000 in this layout.
	partsBoundBaseMiB    = 20.0
	partsBoundPerVersion = 0.0125
)

func seedProjectTeamOwnershipShape(t *testing.T, f *ingestColumnsFixture, rows, unmergedParts int) {
	t.Helper()
	synced := f.old
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced)
SELECT concat('P-', toString(number)), ?, 'linear', concat('K', toString(number)), concat('project ', toString(number)), 1, 'started', '', ?, ? FROM numbers(?)`,
		f.h.orgID, f.old, synced, uint64(memoryShapeProjects-1))
	// One insert, one part: teams are joined with FINAL in both arms, and a
	// part per team would put a fixture artifact into every layout's peak.
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active)
SELECT concat('T-', toString(number)), concat('team T-', toString(number)), '', ?, ?, ?, 'linear', concat('T-', toString(number)), [], 1 FROM numbers(?)`,
		f.old, synced, f.h.orgID, uint64(memoryShapeTeams))
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
	maxMiB   float64
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
		highest := 0.0
		for run := 0; run < memoryShapeRuns; run++ {
			tag := fmt.Sprintf("pto-%s-%s-%d-%d", stage, frag.Name, run, time.Now().UnixNano())
			bindings := []contextpacket.ClickHouseBinding{{Name: "org_id", Value: f.h.orgID}}
			if frag.Paged {
				bindings = devhealthsource.ProjectTeamsPagedBindingsForTest(f.h.orgID, 500)
			}
			client, settings := query, "max_result_rows = 0, max_memory_usage = 0"
			if frag.Settings != "" {
				settings += ", " + frag.Settings
			}
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
			if mib > highest {
				highest = mib
			}
			if best.mib < 0 || mib < best.mib {
				best = fragmentPeak{name: frag.Name, mib: mib, readRows: readRows, rows: n}
			}
		}
		best.maxMiB = highest
		out = append(out, best)
	}
	return out
}

func report(t *testing.T, text string) {
	t.Helper()
	t.Log(text)
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
	fmt.Fprintf(&b, "project-team ownership statement, stage=%s (lowest of %d warm runs, highest in brackets; %d pairs)\n", stage, memoryShapeRuns, memoryShapePairs)
	for _, p := range peaks {
		fmt.Fprintf(&b, "  %-24s peak=%7.2f MiB (highest %7.2f) read_rows=%9d rows_out=%d\n", p.name, p.mib, p.maxMiB, p.readRows, p.rows)
	}
	return b.String()
}

// The statement the projector sends for the project-team ownership edge, on
// real ClickHouse at the largest prod org's ownership volume, under the
// projector's own statement settings. The worst case is merges stopped with
// several unmerged parts; the other stage is after OPTIMIZE FINAL.
func TestProjectTeamOwnershipStatementMemoryOnTheProdShape(t *testing.T) {
	// 767 versions per pair over 88 pairs is the 67,496 rows of the largest org.
	for i, layout := range []string{"parts", "merged"} {
		t.Run(layout, func(t *testing.T) {
			m := freshDatabaseFixture(t, fmt.Sprintf("95460000-0000-4000-8000-00000000030%d", i), layout, 767)
			capped := contextpacket.NewMemoryCappedQueryClient(m.query, prodMaxMemoryUsage)
			only := map[string]bool{"ownership_raw_final_x1": true, "asserting_arm": true, "retraction_arm": true, "group_statement": true, "paged_statement": true, "legacy_paged_statement": true}
			peaks := measureProjectTeamsFragments(t, m.f, m.query, capped, "prod-"+layout, only)
			report(t, fmt.Sprintf("%d active parts\n%s", m.parts, fragmentTable(layout, peaks)))
			paged := peakOf(t, peaks, "paged_statement")
			raw := peakOf(t, peaks, "ownership_raw_final_x1")
			// An absolute bound is a cap margin: the worst warm read decides.
			if paged.maxMiB > memoryBoundMiB {
				t.Errorf("%s: the paged statement peaked at %.2f MiB (lowest read %.2f), bound %.0f MiB", layout, paged.maxMiB, paged.mib, memoryBoundMiB)
			}
			// The ownership table is read once: the statement's whole read is the
			// raw FINAL read plus the small projects and teams tables.
			if limit := raw.readRows + raw.readRows/10 + 2000; paged.readRows > limit {
				t.Errorf("%s: the paged statement read %d rows, want at most %d (one read of the %d ownership rows)", layout, paged.readRows, limit, raw.readRows)
			}
		})
	}
}

// layoutMeasurement is a fresh database holding the seeded ownership history in
// a fixed physical layout: "merged" is one part after OPTIMIZE FINAL; "parts"
// is memoryShapeUnmerged inserts with merges stopped for the table, which is
// what a prod table looks like between merges.
type layoutMeasurement struct {
	f      *ingestColumnsFixture
	query  contextpacket.ClickHouseQueryClient
	parts  uint64
	layout string
}

func freshDatabaseFixture(t *testing.T, orgID, layout string, versions int) layoutMeasurement {
	t.Helper()
	ctx := context.Background()
	addr, admin := sharedDevHealthClickHouse(t)
	database := fmt.Sprintf("t%d", perTestDatabaseSeq.Add(1))
	if err := admin.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create per-test database %s: %v", database, err)
	}
	t.Cleanup(func() { _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" SYNC") })
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: database, Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	f := &ingestColumnsFixture{t: t, ctx: ctx, now: time.Now().UTC().Truncate(time.Second)}
	f.hourAgo, f.old = f.now.Add(-time.Hour), f.now.Add(-45*24*time.Hour)
	f.h = &ingestHarness{t: t, ctx: ctx, direct: direct, orgID: orgID, repo: zeroUUID}
	// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
	mustExec(t, ctx, direct, "SYSTEM STOP MERGES team_project_ownership")
	inserts := 1
	if layout == "parts" {
		inserts = memoryShapeUnmerged
	}
	seedProjectTeamOwnershipShape(t, f, memoryShapePairs*versions, inserts)
	if layout == "merged" {
		// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
		mustExec(t, ctx, direct, "SYSTEM START MERGES team_project_ownership")
		// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
		mustExec(t, ctx, direct, "OPTIMIZE TABLE team_project_ownership FINAL")
	}
	var parts uint64
	pr, err := direct.Query(ctx, "SELECT count() FROM system.parts WHERE active AND database = ? AND table = 'team_project_ownership'", database)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Next() {
		_ = pr.Scan(&parts)
	}
	_ = pr.Close()
	want := uint64(1)
	if layout == "parts" {
		want = memoryShapeUnmerged
	}
	if parts != want {
		t.Fatalf("layout %s: %d active parts in team_project_ownership, want %d (the layout is not the one this row claims)", layout, parts, want)
	}
	return layoutMeasurement{f: f, query: query, parts: parts, layout: layout}
}

// The statement's memory against the length of the ownership history, in a
// merged layout and in a fixed 12-part layout (a fresh database per point).
// Merged: flat, the rule is fail when large > 1.15x small and large > small +
// 2 MiB over 1,000 versus 2,000 versions per pair, on the lowest of three warm
// reads per size (noise removal). The absolute bounds (48 MiB at the prod shape
// and the 12-part line) use the highest of three: a cap is hit by the worst
// read, not the best. Twelve parts: FINAL over
// overlapping unmerged parts costs more per version, so the peak is bounded by
// a line in versions per pair (partsBoundBaseMiB + partsBoundPerVersion x v);
// the 100 point is measured as well to show the climb to block saturation.
func TestProjectTeamOwnershipStatementMemoryAgainstTheOwnershipHistory(t *testing.T) {
	for _, layout := range []string{"merged", "parts"} {
		t.Run(layout, func(t *testing.T) {
			peaks := map[int]float64{}
			for i, versions := range []int{100, 1000, 2000} {
				m := freshDatabaseFixture(t, fmt.Sprintf("95460000-0000-4000-8000-0000000001%d%d", len(layout), i), layout, versions)
				capped := contextpacket.NewMemoryCappedQueryClient(m.query, prodMaxMemoryUsage)
				only := map[string]bool{"ownership_raw_final_x1": true, "asserting_arm": true, "retraction_arm": true, "group_statement": true, "paged_statement": true}
				all := measureProjectTeamsFragments(t, m.f, m.query, capped, fmt.Sprintf("slope-%s-%d", layout, versions), only)
				peak := peakOf(t, all, "paged_statement")
				report(t, fmt.Sprintf("slope %s: %d versions per pair (%d rows, %d parts):\n%s", layout, versions, memoryShapePairs*versions, m.parts, fragmentTable("slope", all)))
				peaks[versions] = peak.mib
				if layout == "parts" {
					if limit := partsBoundBaseMiB + partsBoundPerVersion*float64(versions); peak.maxMiB > limit {
						t.Errorf("parts: the paged statement peaked at %.2f MiB (lowest read %.2f) at %d versions per pair, bound %.2f MiB (%.0f + %.4f x versions)", peak.maxMiB, peak.mib, versions, limit, partsBoundBaseMiB, partsBoundPerVersion)
					}
				}
			}
			if layout == "merged" {
				if small, large := peaks[1000], peaks[2000]; large > 1.15*small && large > small+2 {
					t.Errorf("merged: memory grows with the ownership history: %.2f MiB at 1,000 versions per pair, %.2f MiB at 2,000 (fail when large > 1.15x small and large > small + 2 MiB)", small, large)
				}
			}
		})
	}
}
