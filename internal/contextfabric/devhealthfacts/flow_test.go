package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// errUnexpectedCycleTimesQuery is returned by TestFlowProviderNeverReadsWorkItemCycleTimes's
// fake table so any query against work_item_cycle_times fails loudly
// instead of silently returning canned rows.
var errUnexpectedCycleTimesQuery = errors.New("devhealthfacts_test: work_item_cycle_times must never be queried by FlowProvider")

// workItemMetricsDailyRow shapes one work_item_metrics_daily row as
// FlowProvider's queryTeamScopeRows/readProjectFlow scan it: (team_id,
// provider, work_scope_id, day, items_started, items_completed,
// wip_count_end_of_day, has_wip_p50, wip_p50, has_wip_p90, wip_p90,
// has_cycle_p50, cycle_p50, has_cycle_p90, cycle_p90, has_lead_p50, lead_p50,
// has_lead_p90, lead_p90, bug_completed_ratio, story_points_completed).
// provider is a fixed "github" -- callers not testing cross-provider
// collision (TestFlowProviderProjectRollupSumsAcrossTeamOwnScopesAndProviders
// covers that, against real ClickHouse) don't need to vary it.
func workItemMetricsDailyRow(teamID, workScopeID string, itemsStarted, itemsCompleted, wipEnd int64) []any {
	return []any{
		teamID, "github", workScopeID, "2026-02-21", itemsStarted, itemsCompleted, wipEnd,
		uint8(1), float64(12), uint8(1), float64(30),
		uint8(1), float64(20), uint8(1), float64(48),
		uint8(1), float64(24), uint8(1), float64(60),
		float64(0.1), float64(3.5),
	}
}

// projectWorkItemMetricsRow shapes one readProjectFlow output row: (project
// subject key, team_id, items_started, items_completed, wip_count_end_of_day,
// has/value pairs..., bug_completed_ratio, story_points_completed) --
// CHAOS-4364 codex R2 P1 fix, the project rollup now SQL-aggregates every
// (provider, work_scope_id) row it finds for a team into one summed/
// averaged row per team, so work_scope_id/day no longer appear in its
// output the way they do in the per-scope team_breakdown shape
// workItemMetricsDailyRow builds. workScopeID is still accepted (kept
// realistic for callers that pass a distinguishing scope), just no longer
// scanned back out.
func projectWorkItemMetricsRow(provider, projectID, teamID, workScopeID string, itemsStarted, itemsCompleted int64) []any {
	row := []any{provider + ":" + projectID, teamID}
	row = append(row, workItemMetricsDailyRow(teamID, workScopeID, itemsStarted, itemsCompleted, 5)[4:]...)
	return row
}

func repoMetricsFlowRow(repoID string, prsMerged, prsWithFirstReview int64) []any {
	return []any{repoID, "2026-02-21", prsMerged, prsWithFirstReview,
		uint8(1), float64(4), uint8(1), float64(8), uint8(1), float64(2), uint8(1), float64(6)}
}

// TestFlowProviderTeamReadsScopeBreakdown is CHAOS-4364's core team shape:
// work_item_metrics_daily's per-scope Rows breakdown.
func TestFlowProviderTeamReadsScopeBreakdown(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_metrics_daily", rows: [][]any{workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4)}},
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
	breakdown := fact.Fields["scope_breakdown"].Rows
	if len(breakdown) != 1 || *breakdown[0].Fields["work_scope_id"].String != "scope-a" {
		t.Fatalf("scope_breakdown = %#v", breakdown)
	}
	if *breakdown[0].Fields["wip_age_p50_hours"].Number != 12 {
		t.Fatalf("wip_age_p50_hours = %#v", breakdown[0].Fields["wip_age_p50_hours"])
	}
}

// TestFlowProviderNeverReadsWorkItemCycleTimes is a regression guard
// (codex R1 finding, CHAOS-4364): work_item_cycle_times' flow_efficiency/
// active_time_hours/wait_time_hours columns are never actually populated by
// the Ops sink (they stay at the migration's DEFAULT 0), so reading them
// would publish a fabricated "0.0 flow efficiency" as a canonical fact.
// This pins the fix by proving the table is never queried at all, not just
// that no such field appears in the output -- so a future edit that
// re-adds the read trips this test even before checking field names.
func TestFlowProviderNeverReadsWorkItemCycleTimes(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_metrics_daily", rows: [][]any{workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4)}},
		{match: "FROM work_item_cycle_times", err: errUnexpectedCycleTimesQuery},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v (work_item_cycle_times must never be queried)", err)
	}
	fact := result.Facts[0]
	for _, forbidden := range []string{"flow_efficiency_avg", "active_time_hours_avg", "wait_time_hours_avg", "flow_efficiency_item_count"} {
		if _, present := fact.Fields[forbidden]; present {
			t.Fatalf("fact carries forbidden field %q sourced from an unpersisted column", forbidden)
		}
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
		{match: "FROM work_item_metrics_daily", rows: [][]any{
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

// TestFlowProviderProjectRollupCapsNestedRowsAtValidateBound is codex R1's
// P2 finding, CHAOS-4364: FactValue.Validate rejects a Rows table over 64
// entries OUTRIGHT (no truncation), so a project with more than 64 owning
// (team, scope) rows must be capped and disclosed by the PROVIDER before
// constructing the fact, or the whole fact read fails instead of returning
// a bounded, explicitly-truncated result.
func TestFlowProviderProjectRollupCapsNestedRowsAtValidateBound(t *testing.T) {
	t.Parallel()
	rows := make([][]any, 0, 70)
	for i := 0; i < 70; i++ {
		rows = append(rows, projectWorkItemMetricsRow("linear", "proj-1", fmt.Sprintf("team-%02d", i), "scope-a", 1, 1))
	}
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_metrics_daily", rows: rows}}}
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
	if err := fact.Fields["team_breakdown"].Validate(); err != nil {
		t.Fatalf("team_breakdown fails FactValue.Validate() even after capping: %v", err)
	}
	breakdown := fact.Fields["team_breakdown"].Rows
	if len(breakdown) != 64 {
		t.Fatalf("team_breakdown rows = %d, want capped to 64", len(breakdown))
	}
	if fact.Fields["team_breakdown_omitted_count"].Integer == nil || *fact.Fields["team_breakdown_omitted_count"].Integer != 6 {
		t.Fatalf("team_breakdown_omitted_count = %#v, want 6", fact.Fields["team_breakdown_omitted_count"])
	}
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 70 {
		t.Fatalf("team_count (uncapped total) = %#v, want 70", fact.Fields["team_count"])
	}
	if !result.Truncated {
		t.Fatalf("Truncated = false, want true when nested rows were capped")
	}
	if result.OmittedCount != 6 {
		t.Fatalf("OmittedCount = %d, want 6", result.OmittedCount)
	}
}
