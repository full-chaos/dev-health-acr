package v1

import "testing"

// chaos4682_time_series_table_test.go pins CHAOS-4682's (§5.1 P2, dual-read
// cutover) wire additions: ContextFabricClaimedFact/ContextFabricProjectedFact
// gain an ADDITIVE TimeSeriesTable/TimeSeriesRows pair, alongside the
// existing Table/Rows -- which keep their current meaning and legacy
// preference unconditionally. Every case here is red on origin/main (the
// pair does not exist there) and green on this branch.

func validClaimedFactWithLegacyTable() ContextFabricClaimedFact {
	ratio := 0.6
	day := "2026-08-30"
	return ContextFabricClaimedFact{
		ClaimID: "claim_chaos4682_ok",
		Kind:    "workload",
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project:ask-dev", Label: "Ask Dev"},
		Field:   "backlog_size",
		Value:   ContextFabricScalarValue{Number: &ratio},
		Rows: []ContextFabricClaimedFactRow{
			{Fields: map[string]ContextFabricScalarValue{"team_id": {String: &day}}},
		},
		Table: &ContextFabricClaimedFactTable{
			Field: "team_breakdown", Shape: ContextFabricFactTableShapeBreakdown,
			Key: []string{"team_id"}, Measures: []string{"backlog_size"},
		},
	}
}

func validTimeSeriesRow() ContextFabricClaimedFactRow {
	day := "2026-08-30"
	value := 12.0
	return ContextFabricClaimedFactRow{Fields: map[string]ContextFabricScalarValue{"day": {String: &day}, "backlog_size": {Number: &value}}}
}

func TestContextFabricClaimedFactValidateAcceptsTimeSeriesTablePair(t *testing.T) {
	fact := validClaimedFactWithLegacyTable()
	fact.TimeSeriesRows = []ContextFabricClaimedFactRow{validTimeSeriesRow()}
	fact.TimeSeriesTable = &ContextFabricClaimedFactTable{
		Field: "daily_workload", Shape: ContextFabricFactTableShapeTimeSeries,
		Key: []string{"day"}, Measures: []string{"backlog_size"},
	}
	if err := fact.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestContextFabricClaimedFactValidateAcceptsNilTimeSeriesPairOnASingleTableFact(t *testing.T) {
	// The common, pre-CHAOS-4682 case: nothing sets the additive pair, and
	// that must stay exactly as valid as it always was.
	fact := validClaimedFactWithLegacyTable()
	if err := fact.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestContextFabricClaimedFactValidateRejectsTimeSeriesTableWithNonTimeSeriesShape(t *testing.T) {
	fact := validClaimedFactWithLegacyTable()
	fact.TimeSeriesRows = []ContextFabricClaimedFactRow{validTimeSeriesRow()}
	// The whole reason this pair exists: it is ALWAYS the time_series table.
	// A breakdown or ranking shape here contradicts the field's own name and
	// would defeat datedFactTrendShape's "read TimeSeriesTable first"
	// preference silently.
	fact.TimeSeriesTable = &ContextFabricClaimedFactTable{
		Field: "daily_workload", Shape: ContextFabricFactTableShapeBreakdown,
		Key: []string{"day"}, Measures: []string{"backlog_size"},
	}
	if err := fact.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: time_series_table declared a non-time_series shape")
	}
}

func TestContextFabricClaimedFactValidateRejectsTimeSeriesTableDescribingRowsTheFactDoesNotCarry(t *testing.T) {
	fact := validClaimedFactWithLegacyTable()
	// No TimeSeriesRows set -- a declaration with nothing to describe,
	// mirroring validateClaimedFactTable's existing rule for the legacy
	// pair (a declaration about rows the fact does not carry is refused).
	fact.TimeSeriesTable = &ContextFabricClaimedFactTable{
		Field: "daily_workload", Shape: ContextFabricFactTableShapeTimeSeries,
		Key: []string{"day"}, Measures: []string{"backlog_size"},
	}
	if err := fact.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: time_series_table describes rows the fact does not carry")
	}
}

func TestContextFabricClaimedFactValidateRejectsTimeSeriesRowsExceedingBound(t *testing.T) {
	fact := validClaimedFactWithLegacyTable()
	rows := make([]ContextFabricClaimedFactRow, ContextFabricClaimedFactMaxRows+1)
	for i := range rows {
		rows[i] = validTimeSeriesRow()
	}
	fact.TimeSeriesRows = rows
	if err := fact.Validate(); err == nil {
		t.Fatalf("Validate() = nil, want an error: %d time_series_rows exceeds the %d bound", len(rows), ContextFabricClaimedFactMaxRows)
	}
}

// TestRenderableRowsPrefersTimeSeriesRowsOverRows is the discriminating
// proof behind datedFactTrendShape's own "read TimeSeriesTable first"
// preference and every render-point resolver that mirrors it
// (renderShapeSourcesFromResult/FromProjection, answerprojection's
// projectedRenderSources): given a claim carrying BOTH arrays, resolution
// prefers TimeSeriesRows, never Rows -- proven by giving the two arrays
// DIFFERENT content at the same index, so a resolver reading the wrong one
// would read the wrong value, not merely an empty one.
func TestRenderableRowsPrefersTimeSeriesRowsOverRows(t *testing.T) {
	legacyValue := "legacy"
	seriesValue := "series"
	fact := ContextFabricClaimedFact{
		ClaimID: "claim_pref",
		Rows:    []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{"marker": {String: &legacyValue}}}},
		TimeSeriesRows: []ContextFabricClaimedFactRow{
			{Fields: map[string]ContextFabricScalarValue{"marker": {String: &seriesValue}}},
		},
	}
	got := fact.renderableRows()
	if len(got) != 1 || got[0].Fields["marker"].String == nil || *got[0].Fields["marker"].String != "series" {
		t.Fatalf("renderableRows() = %+v, want the TimeSeriesRows entry (marker=series), not Rows (marker=legacy)", got)
	}
}

func TestRenderableRowsFallsBackToRowsWhenTimeSeriesRowsIsEmpty(t *testing.T) {
	legacyValue := "legacy"
	fact := ContextFabricClaimedFact{
		ClaimID: "claim_fallback",
		Rows:    []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{"marker": {String: &legacyValue}}}},
	}
	got := fact.renderableRows()
	if len(got) != 1 || got[0].Fields["marker"].String == nil || *got[0].Fields["marker"].String != "legacy" {
		t.Fatalf("renderableRows() = %+v, want the Rows entry (marker=legacy) when TimeSeriesRows is empty", got)
	}
}
