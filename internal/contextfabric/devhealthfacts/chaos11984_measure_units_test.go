package devhealthfacts_test

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos11984_measure_units_test.go pins the units the producers declare for
// their table measures (CHAOS-11984), read through the same function the engine
// states them with, over the tables the producers actually emit.

// unitsOfProducedTable reads one produced table's stated units, keyed by
// measure, and fails when a measure of the table has none.
func unitsOfProducedTable(t *testing.T, provider contextfabric.FactProvider, fact contextfabric.CanonicalFact, field string) map[string]contractsv1.ContextFabricMeasureUnit {
	t.Helper()
	table := fact.Fields[field].Table
	if table == nil || len(table.Measures) == 0 {
		t.Fatalf("%s: the producer emitted no declared table with measures", field)
	}
	units := contextfabric.DeclaredMeasureUnits(provider.Capability(), fact.Subject.Kind, field, table.Measures)
	got := make(map[string]contractsv1.ContextFabricMeasureUnit, len(units))
	for _, declaration := range units {
		got[declaration.Measure] = declaration.Unit
	}
	for _, measure := range table.Measures {
		if _, ok := got[measure]; !ok {
			t.Errorf("%s: measure %q states no unit", field, measure)
		}
	}
	return got
}

// count, hours and ratio: the repository daily metrics series.
func TestCHAOS11984TheDailyMetricsSeriesStatesItsUnits(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: metricsRowsForOneRepoOverDays("repo-1", 3)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	got := unitsOfProducedTable(t, provider, result.Facts[0], "daily_metrics")
	want := map[string]contractsv1.ContextFabricMeasureUnit{
		"commits_count":         contractsv1.ContextFabricMeasureUnitCount,
		"prs_merged":            contractsv1.ContextFabricMeasureUnitCount,
		"median_pr_cycle_hours": contractsv1.ContextFabricMeasureUnitHours,
		"revert_rate":           contractsv1.ContextFabricMeasureUnitRatio,
		"bus_factor":            contractsv1.ContextFabricMeasureUnitCount,
		"code_ownership_gini":   contractsv1.ContextFabricMeasureUnitRatio,
		"mttr_hours":            contractsv1.ContextFabricMeasureUnitHours,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("daily_metrics units = %v, want %v", got, want)
	}
}

// count, ratio and points: the team daily flow series.
func TestCHAOS11984TheDailyFlowSeriesStatesItsUnits(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4)}},
		{match: flowDailySeriesMatch, rows: [][]any{
			flowDailySeriesRow("team-1", "2026-02-21", 10, 6, 4),
			flowDailySeriesRow("team-1", "2026-02-20", 8, 5, 3),
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
	got := unitsOfProducedTable(t, provider, result.Facts[0], "daily_flow")
	want := map[string]contractsv1.ContextFabricMeasureUnit{
		"items_started":          contractsv1.ContextFabricMeasureUnitCount,
		"items_completed":        contractsv1.ContextFabricMeasureUnitCount,
		"wip_count_end_of_day":   contractsv1.ContextFabricMeasureUnitCount,
		"bug_completed_ratio":    contractsv1.ContextFabricMeasureUnitRatio,
		"story_points_completed": contractsv1.ContextFabricMeasureUnitPoints,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("daily_flow units = %v, want %v", got, want)
	}
}

// days: the project workload breakdown's forecast, from its declaration. A
// throughput column declares no unit and gets none.
func TestCHAOS11984TheWorkloadForecastStatesDays(t *testing.T) {
	t.Parallel()
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{}), contextfabric.FactWorkload)
	got := contextfabric.DeclaredMeasureUnits(provider.Capability(), contractsv1.ContextFabricSubjectProject, "team_breakdown", []string{"forecast_p50_days", "throughput_mean", "backlog_size"})
	want := []contractsv1.ContextFabricMeasureUnitDeclaration{
		{Measure: "forecast_p50_days", Unit: contractsv1.ContextFabricMeasureUnitDays},
		{Measure: "backlog_size", Unit: contractsv1.ContextFabricMeasureUnitCount},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("team_breakdown units = %+v, want %+v", got, want)
	}
}

// The tokens producers declare on numeric table columns and the wire does not
// serve. A new token fails here until it is served or listed. loc and lines
// name the same measure (churn_loc_30d) in two producers; CHAOS-11984 records
// the conflict and serves neither.
var declaredUnservedMeasureUnits = []string{"", "axis", "lines", "loc", "score", "share"}

func TestCHAOS11984EveryDeclaredMeasureUnitIsServedOrListed(t *testing.T) {
	t.Parallel()
	served := map[string]bool{}
	unserved := map[string]bool{}
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		for _, field := range provider.Capability().Fields {
			for _, column := range field.Columns {
				if column.Type != contextfabric.FactFieldInteger && column.Type != contextfabric.FactFieldNumber {
					continue
				}
				if contractsv1.ValidContextFabricMeasureUnit(contractsv1.ContextFabricMeasureUnit(column.Unit)) {
					served[column.Unit] = true
				} else {
					unserved[column.Unit] = true
				}
			}
		}
	}
	if got := slices.Sorted(maps.Keys(unserved)); !slices.Equal(got, declaredUnservedMeasureUnits) {
		t.Fatalf("declared, unserved unit tokens = %q, want %q: serve a new token or list it here", got, declaredUnservedMeasureUnits)
	}
	for _, unit := range contractsv1.ContextFabricMeasureUnitVocabulary() {
		if !served[string(unit)] {
			t.Errorf("unit %q is in the wire vocabulary and no producer declares it", unit)
		}
	}
}
