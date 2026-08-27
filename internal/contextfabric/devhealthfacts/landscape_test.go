package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// landscapeTeamRow shapes one readTeamLandscape row: (team_id, map_name,
// as_of_day, identity_count, churn_loc_30d_sum, delivery_units_30d_sum,
// cycle_p50_30d_hours_avg, wip_max_30d_max).
func landscapeTeamRow(teamID, mapName string, identityCount, churnLOC, deliveryUnits int64, cycleP50Avg float64, wipMax int64) []any {
	return []any{teamID, mapName, "2026-02-21", identityCount, churnLOC, deliveryUnits, cycleP50Avg, wipMax}
}

func landscapeProjectRow(provider, projectID, teamID, mapName string, identityCount, churnLOC, deliveryUnits int64, cycleP50Avg float64, wipMax int64) []any {
	return []any{provider + ":" + projectID, teamID, mapName, "2026-02-21", identityCount, churnLOC, deliveryUnits, cycleP50Avg, wipMax}
}

// TestLandscapeProviderTeamAggregatesByMapNameNeverPerIdentity is the
// platform's no-person-ranking guardrail applied structurally: the fact
// must carry only (team, map_name) aggregates, never a per-identity row.
func TestLandscapeProviderTeamAggregatesByMapNameNeverPerIdentity(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM ic_landscape_rolling_30d", rows: [][]any{
		landscapeTeamRow("team-1", "churn_throughput", 4, 1200, 30, 18.5, 6),
		landscapeTeamRow("team-1", "cycle_throughput", 4, 1200, 30, 22.0, 6),
		landscapeTeamRow("team-1", "wip_throughput", 4, 1200, 30, 15.0, 6),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["area_count"].Integer == nil || *fact.Fields["area_count"].Integer != 3 {
		t.Fatalf("area_count = %#v", fact.Fields["area_count"])
	}
	rows := fact.Fields["area_breakdown"].Rows
	if len(rows) != 3 {
		t.Fatalf("area_breakdown rows = %d, want 3", len(rows))
	}
	for _, row := range rows {
		if _, hasIdentityID := row.Fields["identity_id"]; hasIdentityID {
			t.Fatalf("area_breakdown row carries identity_id, violating the no-person-ranking guardrail: %#v", row)
		}
		if row.Fields["identity_count"].Integer == nil || *row.Fields["identity_count"].Integer != 4 {
			t.Fatalf("identity_count = %#v", row.Fields["identity_count"])
		}
	}
	if *rows[0].Fields["map_name"].String != "churn_throughput" {
		t.Fatalf("map_name = %#v", rows[0].Fields["map_name"])
	}
}

// TestLandscapeProviderProjectRollupDisclosesOwningTeams mirrors
// metrics.go's rollup_basis pattern: the project's landscape is expressed
// as which teams own it (team_project_ownership) and each owning team's
// own area breakdown, never averaged across teams.
func TestLandscapeProviderProjectRollupDisclosesOwningTeams(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: [][]any{
		landscapeProjectRow("linear", "proj-1", "team-1", "churn_throughput", 4, 1200, 30, 18.5, 6),
		landscapeProjectRow("linear", "proj-1", "team-2", "churn_throughput", 2, 400, 10, 12.0, 3),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["rollup_basis"].String == nil || *fact.Fields["rollup_basis"].String != "team_project_ownership_landscape" {
		t.Fatalf("rollup_basis = %#v", fact.Fields["rollup_basis"])
	}
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 2 {
		t.Fatalf("team_count = %#v", fact.Fields["team_count"])
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("team_breakdown rows = %d, want 2", len(rows))
	}
	teamIDs := map[string]bool{}
	for _, row := range rows {
		teamIDs[*row.Fields["team_id"].String] = true
	}
	if !teamIDs["team-1"] || !teamIDs["team-2"] {
		t.Fatalf("team_breakdown team_ids = %#v, want team-1 and team-2", teamIDs)
	}
}

// TestLandscapeProviderScopesQueriesToOrgAndSubjects is AC-3780-5's guard.
func TestLandscapeProviderScopesQueriesToOrgAndSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM ic_landscape_rolling_30d", rows: [][]any{
		landscapeTeamRow("team-1", "churn_throughput", 1, 1, 1, 1, 1),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestLandscapeProviderNoSubjectsIsEmptyNotError is the ordinary "capability
// ran, matched nothing" contract every provider in this package shares.
func TestLandscapeProviderNoSubjectsIsEmptyNotError(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want none", result.Facts)
	}
}
