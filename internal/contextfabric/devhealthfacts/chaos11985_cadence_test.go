package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos11985_cadence_test.go pins the cadence the daily producers declare
// (CHAOS-11985), read through the same function the engine states it with,
// and the alignment of the rows they actually emit with stub rows.

// cadenceOfProducedTable reads one produced table's declared cadence and holds
// its rows to it.
func cadenceOfProducedTable(t *testing.T, provider contextfabric.FactProvider, fact contextfabric.CanonicalFact, field string) {
	t.Helper()
	value, ok := fact.Fields[field]
	if !ok || value.Table == nil || len(value.Rows) == 0 {
		t.Fatalf("%s: the producer emitted no declared table with rows", field)
	}
	cadence := contextfabric.DeclaredCadence(provider.Capability(), fact.Subject.Kind, field)
	if cadence != contractsv1.ContextFabricFactTableCadenceDay {
		t.Fatalf("%s: declared cadence = %q, want day", field, cadence)
	}
	if value.Table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("%s: shape = %q, want time_series", field, value.Table.Shape)
	}
	table := contractsv1.ContextFabricClaimedFactTable{
		Field: field, Shape: contractsv1.ContextFabricFactTableShapeTimeSeries,
		Key: value.Table.Key, Measures: value.Table.Measures, Cadence: cadence,
	}
	rows := make([]contractsv1.ContextFabricClaimedFactRow, 0, len(value.Rows))
	for _, row := range value.Rows {
		fields := make(map[string]contractsv1.ContextFabricScalarValue, len(row.Fields))
		for name, cell := range row.Fields {
			fields[name] = contractsv1.ContextFabricScalarValue{String: cell.String, Integer: cell.Integer, Number: cell.Number, Boolean: cell.Boolean, Null: cell.Null}
		}
		rows = append(rows, contractsv1.ContextFabricClaimedFactRow{Fields: fields})
	}
	if err := table.CadenceAligned(rows); err != nil {
		t.Fatalf("%s: the emitted rows do not keep the day cadence: %v", field, err)
	}
}

func readOneFact(t *testing.T, client *fakeClient, kind contextfabric.FactKind, subject contextfabric.SubjectRef) (contextfabric.FactProvider, contextfabric.CanonicalFact) {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: kind, Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		t.Fatalf("%s ReadFacts() error = %v", kind, err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("%s facts = %d, want 1", kind, len(result.Facts))
	}
	return provider, result.Facts[0]
}

func TestCHAOS11985TheRepositoryDailyMetricsKeepADayCadence(t *testing.T) {
	t.Parallel()
	provider, fact := readOneFact(t, &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: metricsRowsForOneRepoOverDays("repo-1", 3)}}},
		contextfabric.FactMetrics, repoSubject("repo-1"))
	cadenceOfProducedTable(t, provider, fact, "daily_metrics")
}

func TestCHAOS11985TheDailyFlowKeepsADayCadence(t *testing.T) {
	t.Parallel()
	provider, fact := readOneFact(t, &fakeClient{tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4)}},
		{match: flowDailySeriesMatch, rows: [][]any{
			flowDailySeriesRow("team-1", "2026-02-21", 10, 6, 4),
			flowDailySeriesRow("team-1", "2026-02-19", 8, 5, 3),
		}},
	}}, contextfabric.FactFlow, teamSubject("team-1"))
	cadenceOfProducedTable(t, provider, fact, "daily_flow")
}

func TestCHAOS11985TheDailyHealthKeepsADayCadence(t *testing.T) {
	t.Parallel()
	provider, fact := readOneFact(t, &fakeClient{tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("team-1")}},
		{match: healthDailySeriesMatch, rows: [][]any{
			healthTeamDailySeriesRow("team-1", "2026-02-21", "high", uint8(1), 0.61),
			healthTeamDailySeriesRow("team-1", "2026-02-20", "elevated", uint8(1), 0.42),
		}},
	}}, contextfabric.FactHealth, teamSubject("team-1"))
	cadenceOfProducedTable(t, provider, fact, "daily_health")
}

func TestCHAOS11985TheDailyReadinessKeepsADayCadence(t *testing.T) {
	t.Parallel()
	provider, fact := readOneFact(t, &fakeClient{tables: []fakeTable{
		{match: readinessOriginalQueryMatch, rows: [][]any{readinessRow("CHAOS")}},
		{match: readinessDailySeriesMatch, rows: [][]any{
			readinessDailySeriesRow("CHAOS", "2026-02-22", 18, 2, 20),
			readinessDailySeriesRow("CHAOS", "2026-02-21", 10, 10, 20),
		}},
	}}, contextfabric.FactReadiness, teamSubject("CHAOS"))
	cadenceOfProducedTable(t, provider, fact, "daily_readiness")
}

func TestCHAOS11985TheDailyWorkloadKeepsADayCadence(t *testing.T) {
	t.Parallel()
	provider, fact := readOneFact(t, &fakeClient{tables: []fakeTable{
		{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("CHAOS", "scope-a")}},
		{match: workloadDailySeriesMatch, rows: [][]any{
			workloadDailySeriesRow("CHAOS", "2026-07-27", 120, 3.2, 0.8),
			workloadDailySeriesRow("CHAOS", "2026-07-26", 100, 4.0, 1.0),
		}},
	}}, contextfabric.FactWorkload, teamSubject("CHAOS"))
	cadenceOfProducedTable(t, provider, fact, "daily_workload")
}

// Every daily series any producer declares states a day cadence, and the five
// the ticket names are among them. A new daily series gets its cadence from
// the same declaration, with no list to update.
func TestCHAOS11985EveryDailySeriesDeclaresADayCadence(t *testing.T) {
	t.Parallel()
	named := map[string]bool{"daily_health": false, "daily_readiness": false, "daily_workload": false, "daily_flow": false, "daily_metrics": false}
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		capability := provider.Capability()
		for _, field := range capability.Fields {
			if !field.DailySeries {
				continue
			}
			kinds := field.SubjectKinds
			if len(kinds) == 0 {
				kinds = capability.SupportedSubjectKinds
			}
			for _, kind := range kinds {
				if got := contextfabric.DeclaredCadence(capability, kind, field.Name); got != contractsv1.ContextFabricFactTableCadenceDay {
					t.Errorf("%s %s on %s: cadence = %q, want day", capability.Kind, field.Name, kind, got)
				}
			}
			if _, ok := named[field.Name]; ok {
				named[field.Name] = true
			}
		}
	}
	for name, found := range named {
		if !found {
			t.Errorf("%s is not declared a daily series", name)
		}
	}
}
