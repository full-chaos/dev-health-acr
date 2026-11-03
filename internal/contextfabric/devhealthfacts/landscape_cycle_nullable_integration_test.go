package devhealthfacts_test

// Ops migration 146 makes ic_landscape_rolling_30d.cycle_p50_30d_hours
// Nullable(Float64): an identity with no merged pull request in its window
// holds NULL, and a row of zeros that retracts a key holds NULL too. acr serves
// the column on both types: the pre-migration Float64 (a stored 0 reads 0) and
// the Nullable one (NULL is no cycle, never 0, and no sample of a team mean).
// EXECUTED against a real ClickHouse.

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestLandscapeCycleOnBothColumnTypes(t *testing.T) {
	for _, arm := range []struct {
		name, typ string
		nullable  bool
	}{
		{name: "float64", typ: "Float64"},
		{name: "nullable", typ: "Nullable(Float64)", nullable: true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			logs := captureDebugLogs(t)
			ctx := context.Background()
			query, direct := newCHAOS3780IntegrationClient(t, ctx)
			statements := append(devhealthschema.DDL("projects", "team_project_ownership"),
				devhealthschema.DDLWithColumnType("", "ic_landscape_rolling_30d", "cycle_p50_30d_hours", arm.typ))
			for _, statement := range statements {
				if err := direct.Exec(ctx, statement); err != nil {
					t.Fatalf("create table: %v\n%s", err, statement)
				}
			}
			provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactLandscape)
			const orgID, teamNoCycle = "org-landscape-cycle", "team-no-cycle"
			synced := ownershipSyncedAfterWindow()
			exec := func(statement string, args ...any) {
				t.Helper()
				if err := direct.Exec(ctx, statement, args...); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				"proj-cy", orgID, "github", "CY1", "Project", uint8(1), "active", "", synced)
			for _, team := range []string{teamNoCycle, "team-mixed"} {
				exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
					orgID, "github", team, "proj-cy", "CY1", "native", synced, nil, synced)
			}
			// noCycle is what the column holds for an identity with no merged pull
			// request: NULL after the migration, 0 before it.
			var noCycle any = float64(0)
			if arm.nullable {
				noCycle = nil
			}
			point := func(team, identity, mapName string, x float64, cycle any) {
				exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					orgID, repoUUID("repo-cy"), date(2026, 6, 28), identity, team, mapName, x, 2.0, 0.5, 0.5, uint64(100), uint32(2), cycle, uint32(3), ts(2026, 6, 28, 6, 0, 0))
			}
			both := func(team, identity string, x float64, cycle any) {
				point(team, identity, "churn_throughput", x, cycle)
				point(team, identity, "cycle_throughput", x, cycle)
			}
			// A1: no identity of the team holds a cycle.
			point(teamNoCycle, "a1", "churn_throughput", 1, noCycle)
			point(teamNoCycle, "a2", "churn_throughput", 3, noCycle)
			// A2: cycles 10, 0 and none. Ops writes no cycle_throughput point for
			// an identity with no cycle.
			both("team-mixed", "b1", 2, 10.0)
			both("team-mixed", "b2", 4, 0.0)
			point("team-mixed", "b3", "churn_throughput", 9, noCycle)
			// A3: a key Ops retracted (its row of zeros holds the column default),
			// beside a live identity.
			point("team-retract", "c1", "churn_throughput", 1, 4.0)
			point("team-retract", "c2", "churn_throughput", 1, 6.0)
			exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, computed_at) VALUES (?,?,?,?,?,?,?)`,
				orgID, repoUUID("repo-cy"), date(2026, 6, 28), "c2", "team-retract", "churn_throughput", ts(2026, 6, 28, 7, 0, 0))
			// A3: a row whose only value other than 0 is the cycle is live.
			exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, cycle_p50_30d_hours, computed_at) VALUES (?,?,?,?,?,?,?,?)`,
				orgID, repoUUID("repo-cy"), date(2026, 6, 28), "d1", "team-cycle-only", "churn_throughput", 6.0, ts(2026, 6, 28, 6, 0, 0))

			read := func(subject contextfabric.SubjectRef) contextfabric.FactProviderResult {
				t.Helper()
				result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
					Time: pastRangeWindow(), Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{subject},
				})
				if err != nil {
					t.Fatalf("ReadFacts: %v", err)
				}
				return result
			}
			one := func(team string) contextfabric.CanonicalFact {
				t.Helper()
				result := read(teamSubject(team))
				if len(result.Facts) != 1 {
					t.Fatalf("%s: facts = %d, want 1 (state %s, reason %q)", team, len(result.Facts), result.State, result.Reason)
				}
				return result.Facts[0]
			}
			identities := func(label string, row contextfabric.FactValueRow, want int64) {
				t.Helper()
				if got := row.Fields["identity_count"].Integer; got == nil || *got != want {
					t.Fatalf("%s: identity_count = %#v, want %d", label, row.Fields["identity_count"], want)
				}
			}
			cycleNear := func(label string, value contextfabric.FactValue, want float64) {
				t.Helper()
				if value.Null || value.Number == nil || math.Abs(*value.Number-want) > 1e-9 {
					t.Fatalf("%s: cycle_p50_30d_hours_avg = %#v, want %v", label, value, want)
				}
			}

			// A1 / A4.
			noneFact := one(teamNoCycle)
			noneRow := noneFact.Fields["area_breakdown"].Rows[0]
			identities("A1 team", noneRow, 2)
			if arm.nullable {
				requireNoCycle(t, "A1 team area_breakdown", noneRow.Fields["cycle_p50_30d_hours_avg"])
			} else {
				cycleNear("A4 team area_breakdown, a stored 0 reads 0", noneRow.Fields["cycle_p50_30d_hours_avg"], 0)
			}

			// A2 / A4: the mean leaves NULL out and keeps 0.
			mixedFact := one("team-mixed")
			mixedRow := mixedFact.Fields["area_breakdown"].Rows[0]
			identities("A2 team", mixedRow, 3)
			mixedWant := 5.0
			if !arm.nullable {
				mixedWant = 10.0 / 3
			}
			cycleNear("A2 team area_breakdown", mixedRow.Fields["cycle_p50_30d_hours_avg"], mixedWant)
			coordinates := map[string]float64{}
			for _, row := range mixedFact.Fields["map_coordinates"].Rows {
				coordinates[*row.Fields["map_name"].String] = *row.Fields["x_raw_mean"].Number
			}
			if coordinates["churn_throughput"] != 5 || coordinates["cycle_throughput"] != 3 || len(coordinates) != 2 {
				t.Fatalf("A2 map_coordinates x_raw_mean = %v, want churn 5 over three identities and cycle 3 over the two with a cycle point", coordinates)
			}

			// A3: the retracted key is no person and no cycle sample.
			retractRow := one("team-retract").Fields["area_breakdown"].Rows[0]
			identities("A3 retracted team", retractRow, 1)
			cycleNear("A3 retracted team", retractRow.Fields["cycle_p50_30d_hours_avg"], 4)
			var sawRetracted bool
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var entry map[string]any
				if json.Unmarshal([]byte(line), &entry) != nil || entry["msg"] != "devhealthfacts.landscape_retracted_rows" {
					continue
				}
				if entry["retracted_rows"] == float64(1) && entry["groups_with_retracted_rows"] == float64(1) {
					sawRetracted = true
				}
			}
			if !sawRetracted {
				t.Fatalf("A3: the retraction row is not counted in the read's own log: %s", logs.String())
			}
			// A3: a row holding only a cycle is live.
			cycleOnlyRow := one("team-cycle-only").Fields["area_breakdown"].Rows[0]
			identities("A3 cycle-only team", cycleOnlyRow, 1)
			cycleNear("A3 cycle-only team", cycleOnlyRow.Fields["cycle_p50_30d_hours_avg"], 6)

			// A5: the project path.
			project := read(projectSubject("github", "proj-cy"))
			if len(project.Facts) != 1 {
				t.Fatalf("A5: project facts = %d, want 1", len(project.Facts))
			}
			rows := project.Facts[0].Fields["team_breakdown"].Rows
			if len(rows) != 2 {
				t.Fatalf("A5: team_breakdown rows = %d, want 2", len(rows))
			}
			for _, row := range rows {
				switch team := *row.Fields["team_id"].String; team {
				case teamNoCycle:
					identities("A5 project row "+team, row, 2)
					if arm.nullable {
						requireNoCycle(t, "A5 project row "+team, row.Fields["cycle_p50_30d_hours_avg"])
					} else {
						cycleNear("A5 project row "+team, row.Fields["cycle_p50_30d_hours_avg"], 0)
					}
				case "team-mixed":
					identities("A5 project row "+team, row, 3)
					cycleNear("A5 project row "+team, row.Fields["cycle_p50_30d_hours_avg"], mixedWant)
				default:
					t.Fatalf("A5: unexpected team %s", team)
				}
			}
		})
	}
}
