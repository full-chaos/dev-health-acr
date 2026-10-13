package devhealthfacts_test

// Ops writes the same churn, delivery, cycle and WIP values on each of an
// identity's three landscape map rows. A landscape fact must serve one copy
// of them. EXECUTED against a real ClickHouse.

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestLandscapeServesOneCopyOfTheStatsForTeamAndProject(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "team_project_ownership", "ic_landscape_rolling_30d") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactLandscape)
	const orgID = "org-landscape-one-copy"
	synced := ownershipSyncedAfterWindow()
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-oc", orgID, "github", "OC1", "Project", uint8(1), "active", "", synced)
	for _, team := range []string{"team-three-maps", "team-one-map"} {
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "github", team, "proj-oc", "OC1", "native", synced, nil, synced)
	}
	seed := func(team, identity, mapName string, x, y float64, churn uint64, delivery uint32, cycle float64, wip uint32) {
		exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, repoUUID("repo-oc"), date(2026, 6, 28), identity, team, mapName, x, y, 0.5, 0.5, churn, delivery, cycle, wip, ts(2026, 6, 28, 6, 0, 0))
	}
	for index, mapName := range []string{"churn_throughput", "cycle_throughput", "wip_throughput"} {
		base := float64(index * 10)
		seed("team-three-maps", "alice", mapName, base+1, 1, 100, 2, 5.0, 3)
		seed("team-three-maps", "bob", mapName, base+3, 3, 50, 1, 7.0, 4)
	}
	seed("team-one-map", "carol", "churn_throughput", 7, 8, 10, 1, 3.0, 1)

	read := func(subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: pastRangeWindow(), Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %d, want 1 (state %s, reason %q)", len(result.Facts), result.State, result.Reason)
		}
		return result.Facts[0]
	}
	type stats struct {
		identities, churn, delivery, wip int64
		cycle                            float64
	}
	check := func(label string, row contextfabric.FactValueRow, want stats) {
		t.Helper()
		if _, hasMap := row.Fields["map_name"]; hasMap {
			t.Fatalf("%s: row carries map_name, but the stats are one copy per team: %#v", label, row.Fields)
		}
		got := stats{
			identities: *row.Fields["identity_count"].Integer, churn: *row.Fields["churn_loc_30d"].Integer,
			delivery: *row.Fields["delivery_units_30d"].Integer, wip: *row.Fields["wip_max_30d"].Integer,
			cycle: *row.Fields["cycle_p50_30d_hours_avg"].Number,
		}
		if got != want {
			t.Fatalf("%s: stats = %+v, want %+v", label, got, want)
		}
	}
	threeMaps := stats{identities: 2, churn: 150, delivery: 3, wip: 4, cycle: 6.0}
	oneMap := stats{identities: 1, churn: 10, delivery: 1, wip: 1, cycle: 3.0}

	teamRows := read(teamSubject("team-three-maps")).Fields["area_breakdown"].Rows
	if len(teamRows) != 1 {
		t.Fatalf("team area_breakdown rows = %d, want 1", len(teamRows))
	}
	check("team with three maps", teamRows[0], threeMaps)
	checkCoordinates := func(label string, rows []contextfabric.FactValueRow, want map[string][2]float64) {
		t.Helper()
		if len(rows) != len(want) {
			t.Fatalf("%s: map_coordinates rows = %d, want %d", label, len(rows), len(want))
		}
		for _, row := range rows {
			name := *row.Fields["map_name"].String
			expected, ok := want[name]
			if !ok {
				t.Fatalf("%s: unexpected map %s", label, name)
			}
			if *row.Fields["x_raw_mean"].Number != expected[0] || *row.Fields["y_raw_mean"].Number != expected[1] {
				t.Fatalf("%s: %s coordinates = (%v, %v), want %v", label, name, *row.Fields["x_raw_mean"].Number, *row.Fields["y_raw_mean"].Number, expected)
			}
			for _, stat := range []string{"identity_count", "churn_loc_30d", "delivery_units_30d", "cycle_p50_30d_hours_avg", "wip_max_30d"} {
				if _, has := row.Fields[stat]; has {
					t.Fatalf("%s: map_coordinates row repeats the shared stat %s", label, stat)
				}
			}
		}
	}
	threeMapFact := read(teamSubject("team-three-maps"))
	checkCoordinates("team", threeMapFact.Fields["map_coordinates"].Rows, map[string][2]float64{
		"churn_throughput": {2, 2}, "cycle_throughput": {12, 2}, "wip_throughput": {22, 2},
	})
	oneMapFact := read(teamSubject("team-one-map"))
	checkCoordinates("one-map team", oneMapFact.Fields["map_coordinates"].Rows, map[string][2]float64{"churn_throughput": {7, 8}})
	oneMapRows := read(teamSubject("team-one-map")).Fields["area_breakdown"].Rows
	if len(oneMapRows) != 1 {
		t.Fatalf("one-map team area_breakdown rows = %d, want 1", len(oneMapRows))
	}
	check("team with one map", oneMapRows[0], oneMap)

	projectFact := read(projectSubject("github", "proj-oc"))
	if got := len(projectFact.Fields["map_coordinates"].Rows); got != 4 {
		t.Fatalf("project map_coordinates rows = %d, want 4 (three maps of one team, one of the other)", got)
	}
	projectRows := projectFact.Fields["team_breakdown"].Rows
	if len(projectRows) != 2 {
		t.Fatalf("project team_breakdown rows = %d, want 2 (one per owning team)", len(projectRows))
	}
	for _, row := range projectRows {
		switch *row.Fields["team_id"].String {
		case "team-three-maps":
			check("project row of the three-map team", row, threeMaps)
		case "team-one-map":
			check("project row of the one-map team", row, oneMap)
		default:
			t.Fatalf("unexpected team in team_breakdown: %#v", row.Fields)
		}
	}

	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT max(memory_usage) FROM system.query_log WHERE type = 'QueryFinish' AND (query LIKE '%one_churn%' OR query LIKE '%avg(x_raw)%' OR query LIKE '%avg(il.x_raw)%') AND query NOT LIKE '%system.query_log%'`)
	if err != nil {
		t.Fatalf("query_log: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("query_log returned no row: the memory peak of the changed statements was not measured")
	}
	var peak uint64
	if err := rows.Scan(&peak); err != nil {
		t.Fatalf("scan memory peak: %v", err)
	}
	if peak == 0 {
		t.Fatal("memory peak of the changed statements is 0: the measurement did not happen")
	}
	const peakBound = 20 << 20
	figure := fmt.Sprintf("MEMORY-PEAK changed landscape statements: %d bytes (%.1f MiB), bound %d MiB", peak, float64(peak)/(1<<20), peakBound>>20)
	t.Log(figure)
	t.Run(fmt.Sprintf("memory_peak_%dKiB", (peak+1023)>>10), func(t *testing.T) {
		if peak > peakBound {
			t.Fatalf("%s: the changed statements exceed the memory bound", figure)
		}
	})
}
