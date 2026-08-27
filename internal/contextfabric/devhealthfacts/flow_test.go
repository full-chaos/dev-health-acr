package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workItemMetricsDailyRow shapes one work_item_metrics_daily row as
// FlowProvider's queryTeamScopeRows/readProjectFlow scan it: (team_id,
// work_scope_id, day, items_started, items_completed, wip_count_end_of_day,
// has_wip_p50, wip_p50, has_wip_p90, wip_p90, has_cycle_p50, cycle_p50,
// has_cycle_p90, cycle_p90, has_lead_p50, lead_p50, has_lead_p90, lead_p90,
// bug_completed_ratio, story_points_completed).
func workItemMetricsDailyRow(teamID, workScopeID string, itemsStarted, itemsCompleted, wipEnd int64) []any {
	return []any{
		teamID, workScopeID, "2026-02-21", itemsStarted, itemsCompleted, wipEnd,
		uint8(1), float64(12), uint8(1), float64(30),
		uint8(1), float64(20), uint8(1), float64(48),
		uint8(1), float64(24), uint8(1), float64(60),
		float64(0.1), float64(3.5),
	}
}

func projectWorkItemMetricsRow(provider, projectID, teamID, workScopeID string, itemsStarted, itemsCompleted int64) []any {
	row := []any{provider + ":" + projectID, teamID}
	row = append(row, workItemMetricsDailyRow("", workScopeID, itemsStarted, itemsCompleted, 5)[1:]...)
	return row
}

// workItemCycleTimesEfficiencyRow shapes one queryTeamFlowEfficiency row:
// (team_id, avg(flow_efficiency), avg(active_time_hours),
// avg(wait_time_hours), count()).
func workItemCycleTimesEfficiencyRow(teamID string, flowEfficiency, activeHours, waitHours float64, itemCount int64) []any {
	return []any{teamID, flowEfficiency, activeHours, waitHours, itemCount}
}

func repoMetricsFlowRow(repoID string, prsMerged, prsWithFirstReview int64) []any {
	return []any{repoID, "2026-02-21", prsMerged, prsWithFirstReview,
		uint8(1), float64(4), uint8(1), float64(8), uint8(1), float64(2), uint8(1), float64(6)}
}

// TestFlowProviderTeamCombinesScopeRowsAndEfficiency is CHAOS-4364's core
// team shape: work_item_metrics_daily's per-scope Rows breakdown plus
// work_item_cycle_times' averaged flow_efficiency, in ONE CanonicalFact.
func TestFlowProviderTeamCombinesScopeRowsAndEfficiency(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_metrics_daily", rows: [][]any{workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4)}},
		{match: "FROM work_item_cycle_times", rows: [][]any{workItemCycleTimesEfficiencyRow("team-1", 0.62, 8.5, 5.2, 6)}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["items_started"].Integer == nil || *fact.Fields["items_started"].Integer != 10 {
		t.Fatalf("items_started = %#v", fact.Fields["items_started"])
	}
	if fact.Fields["flow_efficiency_avg"].Number == nil || *fact.Fields["flow_efficiency_avg"].Number != 0.62 {
		t.Fatalf("flow_efficiency_avg = %#v", fact.Fields["flow_efficiency_avg"])
	}
	breakdown := fact.Fields["scope_breakdown"].Rows
	if len(breakdown) != 1 || *breakdown[0].Fields["work_scope_id"].String != "scope-a" {
		t.Fatalf("scope_breakdown = %#v", breakdown)
	}
	if *breakdown[0].Fields["wip_age_p50_hours"].Number != 12 {
		t.Fatalf("wip_age_p50_hours = %#v", breakdown[0].Fields["wip_age_p50_hours"])
	}
}

// TestFlowProviderTeamMultipleScopesNeverStitched proves a team forecast/
// measured under several work_scope_id values gets one Rows entry PER
// scope, never one merged/averaged row -- the same F3-shaped discipline
// workload.go documents for capacity_forecasts.
func TestFlowProviderTeamMultipleScopesNeverStitched(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_metrics_daily", rows: [][]any{
			workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4),
			workItemMetricsDailyRow("team-1", "scope-b", 3, 1, 2),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if fact.Fields["scope_count"].Integer == nil || *fact.Fields["scope_count"].Integer != 2 {
		t.Fatalf("scope_count = %#v", fact.Fields["scope_count"])
	}
	if fact.Fields["items_started"].Integer == nil || *fact.Fields["items_started"].Integer != 13 {
		t.Fatalf("items_started (summed additive count) = %#v", fact.Fields["items_started"])
	}
	if len(fact.Fields["scope_breakdown"].Rows) != 2 {
		t.Fatalf("scope_breakdown rows = %d, want 2 (never merged)", len(fact.Fields["scope_breakdown"].Rows))
	}
}

// TestFlowProviderProjectRollupSumsCountsDisclosesPerTeamBreakdown mirrors
// metrics.go's own project-rollup contract: additive counts sum across
// owning teams, and the per-team detail rides unmodified in team_breakdown.
func TestFlowProviderProjectRollupSumsCountsDisclosesPerTeamBreakdown(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM team_project_ownership", rows: [][]any{
			projectWorkItemMetricsRow("linear", "proj-1", "team-1", "scope-a", 10, 6),
			projectWorkItemMetricsRow("linear", "proj-1", "team-2", "scope-b", 5, 2),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["rollup_basis"].String == nil || *fact.Fields["rollup_basis"].String != "team_project_ownership_sum" {
		t.Fatalf("rollup_basis = %#v", fact.Fields["rollup_basis"])
	}
	if fact.Fields["items_started"].Integer == nil || *fact.Fields["items_started"].Integer != 15 {
		t.Fatalf("items_started (summed) = %#v", fact.Fields["items_started"])
	}
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 2 {
		t.Fatalf("team_count = %#v", fact.Fields["team_count"])
	}
	if len(fact.Fields["team_breakdown"].Rows) != 2 {
		t.Fatalf("team_breakdown rows = %d, want 2", len(fact.Fields["team_breakdown"].Rows))
	}
}

// TestFlowProviderRepositoryReadsPRPickupReviewTimings is CHAOS-4364's
// second flow shape: repo_metrics_daily's PR pickup/review columns, under
// the SAME FactFlow kind, mirroring ci.go's dual-shape precedent.
func TestFlowProviderRepositoryReadsPRPickupReviewTimings(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repo_metrics_daily", rows: [][]any{repoMetricsFlowRow("repo-1", 8, 6)}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["pr_pickup_time_p50_hours"].Number == nil || *fact.Fields["pr_pickup_time_p50_hours"].Number != 4 {
		t.Fatalf("pr_pickup_time_p50_hours = %#v", fact.Fields["pr_pickup_time_p50_hours"])
	}
	if fact.Fields["prs_with_first_review"].Integer == nil || *fact.Fields["prs_with_first_review"].Integer != 6 {
		t.Fatalf("prs_with_first_review = %#v", fact.Fields["prs_with_first_review"])
	}
}

// TestFlowProviderScopesQueriesToOrgAndSubjects is AC-3780-5's guard,
// applied to the new team/repository shapes.
func TestFlowProviderScopesQueriesToOrgAndSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_metrics_daily", rows: [][]any{workItemMetricsDailyRow("team-1", "scope-a", 1, 1, 1)}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	found := false
	for _, q := range client.queries {
		if strings.Contains(q.statement, "FROM work_item_metrics_daily") {
			assertQueryScopedToOrgAndSubjects(t, q.statement)
			found = true
		}
	}
	if !found {
		t.Fatalf("no work_item_metrics_daily query captured")
	}
}
