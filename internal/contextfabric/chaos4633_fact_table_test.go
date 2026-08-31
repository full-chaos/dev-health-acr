package contextfabric

import "testing"

// chaos4633_fact_table_test.go pins FactTable.Validate() -- the "wrong
// states unrepresentable" property design doc §5.1 asks for. Every case
// here is red on origin/main (FactTable/TableFactValue do not exist there)
// and green on this branch.

func validTimeSeriesTable() FactTable {
	return FactTable{
		Shape: FactTableTimeSeries,
		Key:   []string{"day"},
		Measures: []string{
			"commits_count", "prs_merged",
		},
		Grain: GrainDay,
		Rows: []FactValueRow{
			{Fields: map[string]FactValue{"day": StringFactValue("2026-08-28"), "commits_count": IntegerFactValue(4), "prs_merged": IntegerFactValue(1)}},
			{Fields: map[string]FactValue{"day": StringFactValue("2026-08-29"), "commits_count": IntegerFactValue(2)}},
		},
	}
}

func TestFactTableValidateAcceptsAWellFormedTimeSeries(t *testing.T) {
	if err := validTimeSeriesTable().Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestFactTableValidateRejectsUnknownShape(t *testing.T) {
	table := validTimeSeriesTable()
	table.Shape = "not_a_shape"
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error for an unknown shape")
	}
}

func TestFactTableValidateRejectsSingleColumnAxisMasqueradingAsCompositeKey(t *testing.T) {
	// flow.go's own scope_breakdown case: Key must be composite
	// ([provider, work_scope_id]), and dropping either column back to a
	// single-column axis must be rejected once the row values are no
	// longer distinct on that one column alone.
	table := FactTable{
		Shape:    FactTableBreakdown,
		Key:      []string{"work_scope_id"},
		Measures: []string{"items_started"},
		Rows: []FactValueRow{
			{Fields: map[string]FactValue{"work_scope_id": StringFactValue("SCOPE-1"), "items_started": IntegerFactValue(3)}},
			// A DIFFERENT provider, same work_scope_id string -- the exact
			// collision CHAOS-4364 documents. Under a single-column axis
			// this is indistinguishable from a duplicate row.
			{Fields: map[string]FactValue{"work_scope_id": StringFactValue("SCOPE-1"), "items_started": IntegerFactValue(7)}},
		},
	}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: the key is not distinct across rows")
	}
	// Widening Key to the real composite fixes it, without changing a
	// single row value -- this is the design's own point about Key.
	table.Key = []string{"provider", "work_scope_id"}
	table.Rows[0].Fields["provider"] = StringFactValue("github")
	table.Rows[1].Fields["provider"] = StringFactValue("linear")
	if err := table.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil once the composite key disambiguates the two providers", err)
	}
}

func TestFactTableValidateRejectsColumnNotDeclaredInKeyOrMeasures(t *testing.T) {
	table := validTimeSeriesTable()
	table.Rows[0].Fields["provider"] = StringFactValue("github") // constant, undeclared
	err := table.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want an error: an undeclared column must be rejected")
	}
	if got := err.Error(); got == "" {
		t.Fatal("Validate() returned an empty error message")
	}
}

func TestFactTableValidateTimeSeriesRequiresExactlyOneKeyColumn(t *testing.T) {
	table := validTimeSeriesTable()
	table.Key = []string{"day", "provider"}
	table.Measures = append(table.Measures, "provider")
	for i := range table.Rows {
		table.Rows[i].Fields["provider"] = StringFactValue("github")
	}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a time_series table with a second identity column is a breakdown by definition, not a judgement call")
	}
}

func TestFactTableValidateTimeSeriesRequiresKeyColumnParsesAsInstant(t *testing.T) {
	table := validTimeSeriesTable()
	table.Rows[0].Fields["day"] = StringFactValue("not-a-date")
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a time_series key column that does not parse as an instant must be rejected")
	}
}

func TestFactTableValidateRankingRequiresOrderByToNameAMeasure(t *testing.T) {
	table := FactTable{
		Shape:    FactTableRanking,
		Key:      []string{"team_id"},
		Measures: []string{"score"},
		Rows: []FactValueRow{
			{Fields: map[string]FactValue{"team_id": StringFactValue("a"), "score": NumberFactValue(9)}},
			{Fields: map[string]FactValue{"team_id": StringFactValue("b"), "score": NumberFactValue(3)}},
		},
	}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: ranking without order_by must be rejected")
	}
	table.OrderBy = "not_a_measure"
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: order_by must name a declared measure")
	}
	table.OrderBy = "score"
	if err := table.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil once order_by names a real measure and rows are ordered", err)
	}
}

func TestFactTableValidateOrderByOnlyValidForRanking(t *testing.T) {
	table := validTimeSeriesTable()
	table.OrderBy = "commits_count"
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: order_by is only valid on a ranking table")
	}
}

func TestFactTableValidateRankingRowsMustActuallyBeInOrder(t *testing.T) {
	table := FactTable{
		Shape:    FactTableRanking,
		Key:      []string{"team_id"},
		Measures: []string{"score"},
		OrderBy:  "score",
		Rows: []FactValueRow{
			{Fields: map[string]FactValue{"team_id": StringFactValue("a"), "score": NumberFactValue(3)}},
			{Fields: map[string]FactValue{"team_id": StringFactValue("b"), "score": NumberFactValue(9)}}, // out of order
		},
	}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: rows must be in order_by order")
	}
}

func TestFactTableValidateRejectsEmptyRows(t *testing.T) {
	table := validTimeSeriesTable()
	table.Rows = nil
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a table must declare at least one row")
	}
}

func TestFactTableValidateRejectsDuplicateColumnInKeyAndMeasures(t *testing.T) {
	table := validTimeSeriesTable()
	table.Measures = append(table.Measures, "day")
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a column may not be both key and measure")
	}
}

// --- FactValue-level "keep Rows identical" structural checks ---

func TestTableFactValueBuildsRowsAndTableFromTheSameSlice(t *testing.T) {
	table := validTimeSeriesTable()
	value := TableFactValue(table)
	if len(value.Rows) != len(value.Table.Rows) {
		t.Fatalf("Rows (%d) and Table.Rows (%d) differ in length", len(value.Rows), len(value.Table.Rows))
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestFactValueValidateRejectsDivergentRowsAndTableRows(t *testing.T) {
	table := validTimeSeriesTable()
	value := FactValue{Rows: table.Rows, Table: &table}
	// Hand-diverge: drop a row from the sibling Rows field only.
	value.Rows = value.Rows[:1]
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: Rows and Table.Rows must stay identical")
	}
}

func TestFactValueValidateRejectsNestedTable(t *testing.T) {
	inner := validTimeSeriesTable()
	outer := validTimeSeriesTable()
	outer.Rows[0].Fields["nested"] = TableFactValue(inner)
	outer.Measures = append(outer.Measures, "nested")
	value := TableFactValue(outer)
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a table must not nest inside a row")
	}
}

func TestFactValueRowsFactValueStillWorksUnchanged(t *testing.T) {
	// RowsFactValue (pre-CHAOS-4633) must keep working byte-for-byte: no
	// Table, still validates.
	value := RowsFactValue([]FactValueRow{{Fields: map[string]FactValue{"x": StringFactValue("y")}}})
	if value.Table != nil {
		t.Fatalf("RowsFactValue set Table = %#v, want nil", value.Table)
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}
