package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// landscapeTeamRow shapes one readTeamLandscape row: (team_id,
// as_of_day, identity_count, churn_loc_30d_sum, delivery_units_30d_sum,
// cycle_p50_30d_hours_avg, wip_max_30d_max).
func landscapeTeamRow(teamID string, identityCount, churnLOC, deliveryUnits int64, cycleP50Avg float64, wipMax int64) []any {
	return []any{teamID, "2026-02-21", identityCount, churnLOC, deliveryUnits, cycleP50Avg, wipMax, int64(0)}
}

func landscapeProjectRow(provider, projectID, teamID string, identityCount, churnLOC, deliveryUnits int64, cycleP50Avg float64, wipMax int64) []any {
	return []any{provider + ":" + projectID, teamID, "2026-02-21", identityCount, churnLOC, deliveryUnits, cycleP50Avg, wipMax, int64(0)}
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
		{match: "sumIf(one_churn", rows: [][]any{landscapeTeamRow("team-1", 4, 1200, 30, 18.5, 6)}},
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
	client := &fakeClient{tables: []fakeTable{{match: "sumIf(il.one_churn", rows: [][]any{
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
	client := &fakeClient{tables: []fakeTable{{match: "sumIf(one_churn", rows: [][]any{
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
	client := &fakeClient{tables: []fakeTable{{match: "sumIf(il.one_churn", rows: rows}}}
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

// TestLandscapeProviderProjectRollupIsNotTruncatedByTheSumOfTwoReads: the
// stats read and the coordinate read each have their own row limit, so two
// complete reads that together exceed one limit are not a cut result.
func TestLandscapeProviderProjectRollupIsNotTruncatedByTheSumOfTwoReads(t *testing.T) {
	t.Parallel()
	var stats, coordinates [][]any
	var subjects []contextfabric.SubjectRef
	for p := 1; p <= 3; p++ {
		projectID := fmt.Sprintf("proj-%d", p)
		subjects = append(subjects, projectSubject("linear", projectID))
		for tm := 0; tm < 20; tm++ {
			teamID := fmt.Sprintf("team-%d-%02d", p, tm)
			stats = append(stats, landscapeProjectRow("linear", projectID, teamID, 1, 10, 1, 5.0, 2))
			for _, mapName := range []string{"churn_throughput", "cycle_throughput", "wip_throughput"} {
				coordinates = append(coordinates, []any{"linear:" + projectID, teamID, mapName, "2026-02-21", 1.5, 2.5})
			}
		}
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "avg(il.x_raw)", rows: coordinates},
		{match: "sumIf(il.one_churn", rows: stats},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 3 {
		t.Fatalf("facts = %d, want 3", len(result.Facts))
	}
	if result.Truncated {
		t.Fatalf("Truncated = true: 60 stats rows and 180 coordinate rows each fit their own limit of 200")
	}
}

// captureDebugLogs routes the default logger into a buffer at debug level for
// one (non-parallel) test.
func captureDebugLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

// TestLandscapeProviderLogsTheRetractionFilterDecision: a team whose rows are
// all retractions serves no fact, and the read says why in its own artifacts.
func TestLandscapeProviderLogsTheRetractionFilterDecision(t *testing.T) {
	logs := captureDebugLogs(t)
	client := &fakeClient{tables: []fakeTable{{match: "sumIf(one_churn", rows: [][]any{
		{"team-gone", "1970-01-01", int64(0), int64(0), int64(0), float64(0), int64(0), int64(6)},
		landscapeTeamRow("team-live", 2, 100, 3, 5.0, 4),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactLandscape)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{teamSubject("team-gone"), teamSubject("team-live")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Subject.CanonicalID != teamSubject("team-live").CanonicalID {
		t.Fatalf("facts = %#v, want only team-live", result.Facts)
	}
	var entry map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var parsed map[string]any
		if json.Unmarshal([]byte(line), &parsed) == nil && parsed["msg"] == "devhealthfacts.landscape_retracted_rows" {
			entry = parsed
		}
	}
	if entry == nil {
		t.Fatalf("no devhealthfacts.landscape_retracted_rows record in %q", logs.String())
	}
	if entry["retracted_rows"] != float64(6) || entry["groups_without_live_rows"] != float64(1) || entry["groups_with_retracted_rows"] != float64(1) || entry["basis"] != "retraction_row_of_zeros" {
		t.Fatalf("decision record = %#v", entry)
	}
}
