package devhealthfacts_test

// Ops retracts a landscape key by writing a row of zeros over it (the key and
// computed_at only, every other column its default 0). A landscape fact must
// not count such a row as a person. EXECUTED against a real ClickHouse.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestLandscapeDoesNotCountRetractedRows(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("ic_landscape_rolling_30d") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactLandscape)
	const orgID = "org-landscape-retracted"
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	live := func(team, identity string, day int) {
		exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, repoUUID("repo-rt"), date(2026, 6, day), identity, team, "churn_throughput", uint64(100), uint32(2), 5.0, uint32(3), ts(2026, 6, day, 6, 0, 0))
	}
	retract := func(team, identity string, day int) {
		exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, computed_at) VALUES (?,?,?,?,?,?,?)`,
			orgID, repoUUID("repo-rt"), date(2026, 6, day), identity, team, "churn_throughput", ts(2026, 6, day, 7, 0, 0))
	}
	live("team-partly-retracted", "eve", 28)
	live("team-partly-retracted", "frank", 28)
	retract("team-partly-retracted", "frank", 28)
	live("team-all-retracted", "grace", 28)
	retract("team-all-retracted", "grace", 28)
	live("team-older-day-live", "heidi", 20)
	live("team-older-day-live", "ivan", 28)
	retract("team-older-day-live", "ivan", 28)

	read := func(team string) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: pastRangeWindow(), Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}

	partly := read("team-partly-retracted")
	if len(partly.Facts) != 1 {
		t.Fatalf("partly retracted team: facts = %d, want 1", len(partly.Facts))
	}
	row := partly.Facts[0].Fields["area_breakdown"].Rows[0]
	if got := *row.Fields["identity_count"].Integer; got != 1 {
		t.Fatalf("identity_count = %d, want 1: the retracted key must not count as a person", got)
	}
	if got := *row.Fields["churn_loc_30d"].Integer; got != 100 {
		t.Fatalf("churn_loc_30d = %d, want 100", got)
	}

	if all := read("team-all-retracted"); len(all.Facts) != 0 {
		t.Fatalf("all-retracted team: facts = %#v, want none: a day with only retracted keys holds no landscape", all.Facts)
	}

	older := read("team-older-day-live")
	if len(older.Facts) != 1 {
		t.Fatalf("team whose latest day is retracted: facts = %d, want 1 (the earlier live day)", len(older.Facts))
	}
	olderRow := older.Facts[0].Fields["area_breakdown"].Rows[0]
	if got := *olderRow.Fields["as_of_day"].String; got != "2026-06-20" {
		t.Fatalf("as_of_day = %s, want 2026-06-20: a retracted day is not the latest day", got)
	}
}
