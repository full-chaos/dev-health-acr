package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// landscapeTeamRow shapes one readTeamLandscape row: (team_id,
// as_of_day, identity_count, churn_loc_30d_sum, delivery_units_30d_sum,
// cycle_p50_30d_hours_avg, wip_max_30d_max).
func landscapeTeamRow(teamID string, identityCount, churnLOC, deliveryUnits int64, cycleP50Avg float64, wipMax int64) []any {
	return []any{teamID, "2026-02-21", identityCount, churnLOC, deliveryUnits, cycleP50Avg, wipMax}
}

func landscapeProjectRow(provider, projectID, teamID string, identityCount, churnLOC, deliveryUnits int64, cycleP50Avg float64, wipMax int64) []any {
	return []any{provider + ":" + projectID, teamID, "2026-02-21", identityCount, churnLOC, deliveryUnits, cycleP50Avg, wipMax}
}

// TestLandscapeProviderTeamAggregatesByMapNameNeverPerIdentity is the
// platform's no-person-ranking guardrail applied structurally: the fact
// must carry only (team, map_name) aggregates, never a per-identity row.
func TestLandscapeProviderTeamAggregatesByMapNameNeverPerIdentity(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "avg(x_raw)", rows: [][]any{
			{"team-1", "churn_throughput", "2026-02-21", 6.1, 3.4},
			{"team-1", "cycle_throughput", "2026-02-21", 2.2, 3.4},
			{"team-1", "wip_throughput", "2026-02-21", 1.5, 3.4},
		}},
		{match: "sum(one_churn)", rows: [][]any{landscapeTeamRow("team-1", 4, 1200, 30, 18.5, 6)}},
	}}
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
	rows := fact.Fields["area_breakdown"].Rows
	if len(rows) != 1 {
		t.Fatalf("area_breakdown rows = %d, want 1", len(rows))
	}
	for _, row := range rows {
		if _, hasIdentityID := row.Fields["identity_id"]; hasIdentityID {
			t.Fatalf("area_breakdown row carries identity_id, violating the no-person-ranking guardrail: %#v", row)
		}
		if row.Fields["identity_count"].Integer == nil || *row.Fields["identity_count"].Integer != 4 {
			t.Fatalf("identity_count = %#v", row.Fields["identity_count"])
		}
	}
	if _, hasMap := rows[0].Fields["map_name"]; hasMap {
		t.Fatalf("area_breakdown row carries map_name: the stats are one copy per team, not one per map: %#v", rows[0])
	}
	coordinates := fact.Fields["map_coordinates"].Rows
	if len(coordinates) != 3 {
		t.Fatalf("map_coordinates rows = %d, want 3 (one per map)", len(coordinates))
	}
	for _, row := range coordinates {
		for _, stat := range []string{"identity_count", "churn_loc_30d", "delivery_units_30d", "wip_max_30d"} {
			if _, has := row.Fields[stat]; has {
				t.Fatalf("map_coordinates row repeats the shared stat %s: %#v", stat, row.Fields)
			}
		}
		if _, hasIdentity := row.Fields["identity_id"]; hasIdentity {
			t.Fatalf("map_coordinates row carries identity_id: %#v", row.Fields)
		}
	}
	if got := coordinates[0].Fields["x_raw_mean"].Number; got == nil || *got != 6.1 {
		t.Fatalf("x_raw_mean = %#v", coordinates[0].Fields["x_raw_mean"])
	}
}

// TestLandscapeProviderProjectRollupDisclosesOwningTeams mirrors
// metrics.go's rollup_basis pattern: the project's landscape is expressed
// as which teams own it (team_project_ownership) and each owning team's
// own area breakdown, never averaged across teams.
func TestLandscapeProviderProjectRollupDisclosesOwningTeams(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "sum(il.one_churn)", rows: [][]any{
		landscapeProjectRow("linear", "proj-1", "team-1", 4, 1200, 30, 18.5, 6),
		landscapeProjectRow("linear", "proj-1", "team-2", 2, 400, 10, 12.0, 3),
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
	client := &fakeClient{tables: []fakeTable{{match: "sum(one_churn)", rows: [][]any{
		landscapeTeamRow("team-1", 1, 1, 1, 1, 1),
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

// TestLandscapeProviderProjectRollupCapsNestedRowsAtValidateBound is
// codex R1's P2 finding, CHAOS-4364: a project with many owning teams
// (e.g. 66) exceeds FactValue.Validate's
// 64-row bound, which REJECTS the whole fact rather than truncating -- so
// the provider must cap and disclose before constructing the fact.
func TestLandscapeProviderProjectRollupCapsNestedRowsAtValidateBound(t *testing.T) {
	t.Parallel()
	rows := make([][]any, 0, 66)
	for i := 0; i < 66; i++ {
		rows = append(rows, landscapeProjectRow("linear", "proj-1", fmt.Sprintf("team-%02d", i), 1, 10, 1, 5.0, 2))
	}
	client := &fakeClient{tables: []fakeTable{{match: "sum(il.one_churn)", rows: rows}}}
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
	if err := fact.Fields["team_breakdown"].Validate(); err != nil {
		t.Fatalf("team_breakdown fails FactValue.Validate() even after capping: %v", err)
	}
	breakdown := fact.Fields["team_breakdown"].Rows
	if len(breakdown) != 64 {
		t.Fatalf("team_breakdown rows = %d, want capped to 64", len(breakdown))
	}
	if fact.Fields["team_breakdown_omitted_count"].Integer == nil || *fact.Fields["team_breakdown_omitted_count"].Integer != 2 {
		t.Fatalf("team_breakdown_omitted_count = %#v, want 2", fact.Fields["team_breakdown_omitted_count"])
	}
	if !result.Truncated {
		t.Fatalf("Truncated = false, want true when nested rows were capped")
	}
	if result.OmittedCount != 2 {
		t.Fatalf("OmittedCount = %d, want 2", result.OmittedCount)
	}
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
