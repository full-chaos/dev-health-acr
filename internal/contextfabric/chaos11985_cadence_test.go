package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos11985_cadence_test.go pins where a claimed table's cadence comes from
// (CHAOS-11985): the producer's daily-series declaration, stated only on a
// time_series table whose rows keep one row per calendar day.

func cadenceCapability() FactCapability {
	return FactCapability{Kind: FactMetrics, Fields: []FactFieldDeclaration{
		{
			Name: "daily_metrics", Type: FactFieldTable, DailySeries: true,
			SubjectKinds: []SubjectKind{contractsv1.ContextFabricSubjectRepository},
			Columns: []FactColumnDeclaration{
				{Name: "day", Type: FactFieldString},
				{Name: "commits_count", Type: FactFieldInteger, Unit: "count", Additivity: FactAdditive},
			},
		},
		{
			Name: "team_breakdown", Type: FactFieldTable,
			Columns: []FactColumnDeclaration{{Name: "day", Type: FactFieldString}, {Name: "commits_count", Type: FactFieldInteger}},
		},
	}}
}

func cadenceDays(days ...string) []contractsv1.ContextFabricClaimedFactRow {
	rows := make([]contractsv1.ContextFabricClaimedFactRow, 0, len(days))
	for _, day := range days {
		day := day
		rows = append(rows, contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{"day": {String: &day}}})
	}
	return rows
}

func cadenceClaim(field string, shape contractsv1.ContextFabricFactTableShape, carried contractsv1.ContextFabricFactTableCadence, rows []contractsv1.ContextFabricClaimedFactRow) ClaimedFact {
	table := func() *contractsv1.ContextFabricClaimedFactTable {
		return &contractsv1.ContextFabricClaimedFactTable{Field: field, Shape: shape, Key: []string{"day"}, Measures: []string{"commits_count"}, Cadence: carried}
	}
	return ClaimedFact{
		ClaimID: "claim_cadence", Kind: FactMetrics, Field: "commits_count",
		Subject: SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:one"},
		Rows:    rows, Table: table(), TimeSeriesRows: rows, TimeSeriesTable: table(),
	}
}

func TestCHAOS11985OnlyADailySeriesDeclaresACadence(t *testing.T) {
	t.Parallel()
	capability := cadenceCapability()
	if got := DeclaredCadence(capability, contractsv1.ContextFabricSubjectRepository, "daily_metrics"); got != contractsv1.ContextFabricFactTableCadenceDay {
		t.Fatalf("daily series: DeclaredCadence = %q, want day", got)
	}
	if got := DeclaredCadence(capability, contractsv1.ContextFabricSubjectRepository, "team_breakdown"); got != "" {
		t.Fatalf("not a daily series: DeclaredCadence = %q, want none", got)
	}
	if got := DeclaredCadence(capability, contractsv1.ContextFabricSubjectTeam, "daily_metrics"); got != "" {
		t.Fatalf("other subject kind: DeclaredCadence = %q, want none: the declaration covers repositories only", got)
	}
}

func TestCHAOS11985AKeptCadenceIsStatedOnBothTables(t *testing.T) {
	t.Parallel()
	claim := cadenceClaim("daily_metrics", contractsv1.ContextFabricFactTableShapeTimeSeries, "", cadenceDays("2026-11-01", "2026-10-30"))
	lookup := declaredCadenceOf([]FactCapability{cadenceCapability()})
	stated := withDeclaredCadence([]ClaimedFact{claim}, lookup)
	if stated[0].Table.Cadence != contractsv1.ContextFabricFactTableCadenceDay || stated[0].TimeSeriesTable.Cadence != contractsv1.ContextFabricFactTableCadenceDay {
		t.Fatalf("cadence = %q / %q, want day on both tables", stated[0].Table.Cadence, stated[0].TimeSeriesTable.Cadence)
	}
	if claim.Table.Cadence != "" {
		t.Fatalf("the source table was rewritten to %q", claim.Table.Cadence)
	}
	if again := withDeclaredCadence(stated, lookup); again[0].Table != stated[0].Table {
		t.Fatal("a second pass copied a table it did not change")
	}
	if withheld := claimedTablesCadenceWithheld(stated, lookup); withheld != 0 {
		t.Fatalf("withheld = %d, want 0", withheld)
	}
}

// Each table is held to its own rows: the table to Rows, the time_series
// table to TimeSeriesRows.
func TestCHAOS11985EachTableIsHeldToItsOwnRows(t *testing.T) {
	t.Parallel()
	claim := cadenceClaim("daily_metrics", contractsv1.ContextFabricFactTableShapeTimeSeries, "", cadenceDays("2026-11-01", "2026-10-31"))
	claim.TimeSeriesRows = cadenceDays("2026-11-01", "2026-11-01")
	stated := withDeclaredCadence([]ClaimedFact{claim}, declaredCadenceOf([]FactCapability{cadenceCapability()}))
	if stated[0].Table.Cadence != contractsv1.ContextFabricFactTableCadenceDay || stated[0].TimeSeriesTable.Cadence != "" {
		t.Fatalf("cadence = %q / %q, want day on the table and none on the time_series table, whose rows repeat a day", stated[0].Table.Cadence, stated[0].TimeSeriesTable.Cadence)
	}
	claim.Rows, claim.TimeSeriesRows = claim.TimeSeriesRows, claim.Rows
	stated = withDeclaredCadence([]ClaimedFact{claim}, declaredCadenceOf([]FactCapability{cadenceCapability()}))
	if stated[0].Table.Cadence != "" || stated[0].TimeSeriesTable.Cadence != contractsv1.ContextFabricFactTableCadenceDay {
		t.Fatalf("swapped: cadence = %q / %q, want none on the table and day on the time_series table", stated[0].Table.Cadence, stated[0].TimeSeriesTable.Cadence)
	}
}

// Rows that do not keep one row per calendar day, a table that is not a
// time_series, a field that is no daily series, and no declarations: each
// leaves no cadence, and replaces one the table carried.
func TestCHAOS11985NoCadenceIsStatedThatTheTableDoesNotKeep(t *testing.T) {
	t.Parallel()
	lookup := declaredCadenceOf([]FactCapability{cadenceCapability()})
	day := contractsv1.ContextFabricFactTableCadenceDay
	for _, tc := range []struct {
		name     string
		claim    ClaimedFact
		lookup   FactTableCadenceLookup
		withheld int
	}{
		{"a repeated day", cadenceClaim("daily_metrics", contractsv1.ContextFabricFactTableShapeTimeSeries, day, cadenceDays("2026-11-01", "2026-11-01")), lookup, 2},
		{"an instant key", cadenceClaim("daily_metrics", contractsv1.ContextFabricFactTableShapeTimeSeries, day, cadenceDays("2026-11-01T00:00:00Z")), lookup, 2},
		{"not a time_series", cadenceClaim("daily_metrics", contractsv1.ContextFabricFactTableShapeBreakdown, day, cadenceDays("2026-11-01")), lookup, 2},
		{"no daily series", cadenceClaim("team_breakdown", contractsv1.ContextFabricFactTableShapeTimeSeries, day, cadenceDays("2026-11-01")), lookup, 0},
		{"no declarations", cadenceClaim("daily_metrics", contractsv1.ContextFabricFactTableShapeTimeSeries, day, cadenceDays("2026-11-01")), declaredCadenceOf(nil), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stated := withDeclaredCadence([]ClaimedFact{tc.claim}, tc.lookup)
			if stated[0].Table.Cadence != "" || stated[0].TimeSeriesTable.Cadence != "" {
				t.Fatalf("cadence = %q / %q, want none", stated[0].Table.Cadence, stated[0].TimeSeriesTable.Cadence)
			}
			if got := claimedTablesCadenceWithheld(stated, tc.lookup); got != tc.withheld {
				t.Fatalf("withheld = %d, want %d", got, tc.withheld)
			}
		})
	}
}

// finalizeResult states the cadence from the engine's own declarations, and
// the selection event counts what it withheld.
func TestCHAOS11985TheEngineStatesTheCadenceWhenItFinalizes(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine(tableEngineDependencies(), EngineOptions{ServiceVersion: "cadence", NewResultID: resultIDSequence(), FactCapabilities: []FactCapability{cadenceCapability()}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	kept := cadenceClaim("daily_metrics", contractsv1.ContextFabricFactTableShapeTimeSeries, "", cadenceDays("2026-11-01", "2026-10-29"))
	broken := cadenceClaim("daily_metrics", contractsv1.ContextFabricFactTableShapeTimeSeries, "", cadenceDays("2026-11-01", "2026-11-01"))
	broken.ClaimID = "claim_cadence_broken"
	result := InvestigationResult{
		Status: InvestigationComplete, ResultID: "result_11985000",
		Coverage:     Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		ClaimedFacts: []ClaimedFact{kept, broken},
	}
	plan := AnswerPlan{Family: QuestionFamilyScopedCohortStatus, FamilyVersion: QuestionFamilyTableVersion}
	finalized := engine.finalizeResult(context.Background(), storage.Principal{}, result, plan, tableFrame(), CanonicalFactBundle{}, &assemblyTelemetry{}, answerPassFirst, MembershipCardinality{})
	if got := finalized.ClaimedFacts[0].TimeSeriesTable.Cadence; got != contractsv1.ContextFabricFactTableCadenceDay {
		t.Fatalf("kept: cadence = %q, want day", got)
	}
	if got := finalized.ClaimedFacts[1].TimeSeriesTable.Cadence; got != "" {
		t.Fatalf("broken: cadence = %q, want none", got)
	}
	_, event := selectRenderShapes(finalized, tableFrame(), engine.renderShapeOptions)
	if event.TablesCadenceWithheld != 2 {
		t.Fatalf("TablesCadenceWithheld = %d, want 2: both tables of the broken claim", event.TablesCadenceWithheld)
	}
}
