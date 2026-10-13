package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The landscape fact at the read_facts boundary: directread.FactsReader over
// the real registry and the real provider on a real ClickHouse. The served
// answer holds one copy of the shared stats, the per-map coordinates in their
// own table, and it satisfies the published response schema strictly.
func TestLandscapeOneCopyAtTheReadFactsBoundary(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("ic_landscape_rolling_30d") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-landscape-boundary"
	at := ts(2026, 6, 28, 6, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-lb", "team-lb", at, orgID, "github", uint8(1))
	for index, mapName := range []string{"churn_throughput", "cycle_throughput", "wip_throughput"} {
		for _, person := range []struct {
			identity string
			x        float64
			churn    uint64
		}{{"alice", 1, 100}, {"bob", 3, 50}} {
			exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				orgID, repoUUID("repo-lb"), date(2026, 6, 28), person.identity, "team-lb", mapName, person.x+float64(index*10), 2.0, 0.5, 0.5, person.churn, uint32(2), 5.0, uint32(3), at)
		}
	}

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(&seamCapture{registry: registry.WithoutScopeExpansion()}), nil)
	start, end := ts(2026, 6, 1, 0, 0, 0), ts(2026, 7, 1, 0, 0, 0)
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000009398"),
		storage.Principal{OrgID: orgID, Subject: "t9398", CredentialID: "t9398"}, directread.FactsRequest{
			Kinds:    []string{"landscape"},
			Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: teamSubject("team-lb").CanonicalID}},
			Window:   &directread.RequestWindow{Mode: directread.WindowRange, Start: &start, End: &end},
		})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if err := contractcheck.ValidateSerializedStrict("", "mcp_read_facts_response.v1.schema.json", raw); err != nil {
		t.Fatalf("the served landscape answer does not satisfy the published read_facts response schema: %v\n%s", err, raw)
	}
	if len(response.Facts) != 1 {
		t.Fatalf("facts = %d, want 1: %s", len(response.Facts), raw)
	}
	tables := response.Facts[0].Tables
	area := tables["area_breakdown"]
	if area.RowsReturned != 1 || len(area.Rows) != 1 {
		t.Fatalf("area_breakdown rows = %d, want 1 (one copy of the stats): %s", area.RowsReturned, raw)
	}
	for index, column := range area.Columns {
		if column == "map_name" {
			t.Fatalf("area_breakdown still carries map_name: %v", area.Columns)
		}
		if column == "churn_loc_30d" {
			if got := area.Rows[0][index]; fmt.Sprint(got) != "150" {
				t.Fatalf("churn_loc_30d = %#v, want \"150\" (alice 100 + bob 50, once): %s", got, raw)
			}
		}
	}
	coordinates := tables["map_coordinates"]
	if coordinates.RowsReturned != 3 {
		t.Fatalf("map_coordinates rows = %d, want 3 (one per map): %s", coordinates.RowsReturned, raw)
	}
}
