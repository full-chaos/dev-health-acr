package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readTeamFlowWith(t *testing.T, scopeRow, dailyRow []any) (*fakeClient, map[string]contextfabric.FactValue) {
	t.Helper()
	client := &fakeClient{tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{scopeRow}},
		{match: flowDailySeriesMatch, rows: [][]any{dailyRow}},
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
	return client, result.Facts[0].Fields
}

func TestFlowMeasureStateShapesTheServedRows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		state        uint8
		wantValue    *float64
		wantReason   string
		dailyNumbers [2]float64
	}{
		{"tracked zero stays zero", 0, f64(0), "", [2]float64{0, 0}},
		{"tracked value", 0, f64(3.5), "", [2]float64{0.1, 3.5}},
		{"not tracked is omitted and named", 1, nil, "not_tracked_by_source", [2]float64{0, 0}},
		{"unknown is omitted and named", 2, nil, "capability_unknown", [2]float64{0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope := workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4)
			n := len(scope)
			scope[n-4], scope[n-3], scope[n-2], scope[n-1] = float64(0.1), tc.state, float64(3.5), tc.state
			daily := []any{"team-1", "2026-02-21", int64(10), int64(6), int64(4), tc.dailyNumbers[0], tc.state, tc.dailyNumbers[1], tc.state}
			_, fields := readTeamFlowWith(t, scope, daily)
			for _, name := range []string{"scope_breakdown", "daily_flow"} {
				if err := fields[name].Validate(); err != nil {
					t.Fatalf("%s fails Validate(): %v", name, err)
				}
			}
			scopeFields := fields["scope_breakdown"].Rows[0].Fields
			dailyFields := fields["daily_flow"].Rows[0].Fields
			if tc.wantValue != nil {
				wantFlowMeasure(t, "scope", scopeFields, "story_points_completed", f64(3.5), "")
				wantFlowMeasure(t, "daily", dailyFields, "story_points_completed", tc.wantValue, "")
			} else {
				wantFlowMeasure(t, "scope", scopeFields, "story_points_completed", nil, tc.wantReason)
				wantFlowMeasure(t, "scope", scopeFields, "bug_completed_ratio", nil, tc.wantReason)
				wantFlowMeasure(t, "daily", dailyFields, "story_points_completed", nil, tc.wantReason)
				wantFlowMeasure(t, "daily", dailyFields, "bug_completed_ratio", nil, tc.wantReason)
			}
		})
	}
}

// The reader contract of work_item_measure_capability: the answer for day D
// is the row whose window_end is D, deduplicated by computed_at; a day with
// no row is unknown, so the read never falls back to an earlier window.
func TestFlowReadsTheCapabilityAnswerOfTheExactDay(t *testing.T) {
	t.Parallel()
	scope := workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4)
	daily := flowDailySeriesRow("team-1", "2026-02-21", 10, 6, 4)
	client, _ := readTeamFlowWith(t, scope, daily)
	checked := 0
	for _, q := range client.queries {
		if !strings.Contains(q.statement, "FROM work_item_metrics_daily") {
			continue
		}
		if strings.Contains(q.statement, "flowWindow") || !strings.Contains(q.statement, "bug_completed_ratio") {
			continue
		}
		checked++
		for _, want := range []string{"FROM work_item_measure_capability", "argMax(tracked, computed_at)", "GROUP BY provider, window_end, measure", "org_id = {org_id:String}", "provider, day, 'story_points_completed'", "provider, day, 'bug_completed_ratio'"} {
			if !strings.Contains(q.statement, want) {
				t.Errorf("query lacks %q:\n%s", want, q.statement)
			}
		}
		for _, banned := range []string{"window_end <=", "window_end <", "ORDER BY window_end", "argMax(tracked, window_end)"} {
			if strings.Contains(q.statement, banned) {
				t.Errorf("query falls back to another window (%q):\n%s", banned, q.statement)
			}
		}
	}
	if checked < 2 {
		t.Fatalf("checked %d flow metric queries, want the scope and the daily read", checked)
	}
}
