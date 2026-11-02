package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos11984_measure_units_test.go pins where a claimed table's measure units
// come from (CHAOS-11984): the producer's own column declaration, copied by the
// server, and nothing else.

func unitsCapability() FactCapability {
	return FactCapability{Kind: FactMetrics, Fields: []FactFieldDeclaration{{
		Name: "daily_metrics", Type: FactFieldTable, DailySeries: true,
		SubjectKinds: []SubjectKind{contractsv1.ContextFabricSubjectRepository},
		Columns: []FactColumnDeclaration{
			{Name: "day", Type: FactFieldString},
			{Name: "commits_count", Type: FactFieldInteger, Unit: "count", Additivity: FactAdditive},
			{Name: "median_pr_cycle_hours", Type: FactFieldNumber, Unit: "hours", Additivity: FactNonAdditive},
			{Name: "churn_loc", Type: FactFieldInteger, Unit: "loc", Additivity: FactNonAdditive},
			{Name: "throughput_mean", Type: FactFieldNumber, Additivity: FactNonAdditive},
		},
	}}}
}

func unitsClaim(subject SubjectKind, units []contractsv1.ContextFabricMeasureUnitDeclaration) ClaimedFact {
	table := &contractsv1.ContextFabricClaimedFactTable{
		Field: "daily_metrics", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries, Key: []string{"day"},
		Measures:     []string{"commits_count", "median_pr_cycle_hours", "churn_loc", "throughput_mean", "undeclared"},
		MeasureUnits: units,
	}
	return ClaimedFact{
		ClaimID: "claim_units", Kind: FactMetrics, Field: "commits_count",
		Subject: SubjectRef{Kind: subject, CanonicalID: string(subject) + ":one"},
		Table:   table, TimeSeriesTable: table,
	}
}

var declaredUnits = []contractsv1.ContextFabricMeasureUnitDeclaration{
	{Measure: "commits_count", Unit: contractsv1.ContextFabricMeasureUnitCount},
	{Measure: "median_pr_cycle_hours", Unit: contractsv1.ContextFabricMeasureUnitHours},
}

// A declared token outside the wire vocabulary (loc), a column with no unit
// and a measure the declaration does not name all get no entry.
func TestCHAOS11984AMeasureGetsTheUnitItsColumnDeclares(t *testing.T) {
	t.Parallel()
	units := DeclaredMeasureUnits(unitsCapability(), contractsv1.ContextFabricSubjectRepository, "daily_metrics",
		[]string{"commits_count", "median_pr_cycle_hours", "churn_loc", "throughput_mean", "undeclared"})
	if !slices.Equal(units, declaredUnits) {
		t.Fatalf("DeclaredMeasureUnits = %+v, want %+v", units, declaredUnits)
	}
	if got := DeclaredMeasureUnits(unitsCapability(), contractsv1.ContextFabricSubjectRepository, "daily_metrics", []string{"median_pr_cycle_hours", "commits_count"}); !slices.Equal(got, []contractsv1.ContextFabricMeasureUnitDeclaration{declaredUnits[1], declaredUnits[0]}) {
		t.Fatalf("DeclaredMeasureUnits = %+v, want the units in the table's measures order", got)
	}
}

// A declaration limited to another subject kind, or another field, states
// nothing.
func TestCHAOS11984AUnitNeedsTheDeclarationOfThatSubjectAndField(t *testing.T) {
	t.Parallel()
	if got := DeclaredMeasureUnits(unitsCapability(), contractsv1.ContextFabricSubjectTeam, "daily_metrics", []string{"commits_count"}); got != nil {
		t.Fatalf("team: DeclaredMeasureUnits = %+v, want none: the declaration covers repositories only", got)
	}
	if got := DeclaredMeasureUnits(unitsCapability(), contractsv1.ContextFabricSubjectRepository, "daily_flow", []string{"commits_count"}); got != nil {
		t.Fatalf("other field: DeclaredMeasureUnits = %+v, want none", got)
	}
}

// The units replace whatever the table carried, on both tables, and the
// table the claim pointed at is not rewritten.
func TestCHAOS11984TheDeclarationReplacesAnyUnitATableCarried(t *testing.T) {
	t.Parallel()
	forged := []contractsv1.ContextFabricMeasureUnitDeclaration{{Measure: "churn_loc", Unit: contractsv1.ContextFabricMeasureUnitDays}}
	claim := unitsClaim(contractsv1.ContextFabricSubjectRepository, forged)
	stated := withDeclaredMeasureUnits([]ClaimedFact{claim}, declaredMeasureUnitsOf([]FactCapability{unitsCapability()}))
	for name, table := range map[string]*contractsv1.ContextFabricClaimedFactTable{"table": stated[0].Table, "time_series_table": stated[0].TimeSeriesTable} {
		if !slices.Equal(table.MeasureUnits, declaredUnits) {
			t.Errorf("%s units = %+v, want the declared %+v", name, table.MeasureUnits, declaredUnits)
		}
	}
	if !slices.Equal(claim.Table.MeasureUnits, forged) {
		t.Fatalf("the source table was rewritten to %+v", claim.Table.MeasureUnits)
	}
	// No declaration clears a carried unit.
	cleared := withDeclaredMeasureUnits([]ClaimedFact{claim}, declaredMeasureUnitsOf(nil))
	if cleared[0].Table.MeasureUnits != nil || cleared[0].TimeSeriesTable.MeasureUnits != nil {
		t.Fatalf("with no declarations: units = %+v / %+v, want none", cleared[0].Table.MeasureUnits, cleared[0].TimeSeriesTable.MeasureUnits)
	}
	// A second pass changes nothing and keeps the same table.
	again := withDeclaredMeasureUnits(stated, declaredMeasureUnitsOf([]FactCapability{unitsCapability()}))
	if again[0].Table != stated[0].Table {
		t.Fatal("a second pass copied a table it did not change")
	}
}

// finalizeResult states the units, from the engine's own declarations, before
// any shape reads the table.
func TestCHAOS11984TheEngineStatesTheUnitsWhenItFinalizes(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine(tableEngineDependencies(), EngineOptions{ServiceVersion: "units", NewResultID: resultIDSequence(), FactCapabilities: []FactCapability{unitsCapability()}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	result := InvestigationResult{
		Status: InvestigationComplete, ResultID: "result_11984000",
		Coverage:     Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		ClaimedFacts: []ClaimedFact{unitsClaim(contractsv1.ContextFabricSubjectRepository, nil)},
	}
	plan := AnswerPlan{Family: QuestionFamilyScopedCohortStatus, FamilyVersion: QuestionFamilyTableVersion}
	finalized := engine.finalizeResult(context.Background(), storage.Principal{}, result, plan, tableFrame(), CanonicalFactBundle{}, &assemblyTelemetry{}, answerPassFirst, MembershipCardinality{})
	if got := finalized.ClaimedFacts[0].Table.MeasureUnits; !slices.Equal(got, declaredUnits) {
		t.Fatalf("finalized units = %+v, want %+v", got, declaredUnits)
	}
}
