package contextfabric

import (
	"slices"
	"testing"
)

func directOnlyRow(cells map[string]FactValue) FactValueRow { return FactValueRow{Fields: cells} }

// directOnlyFact is a fact of the shape that failed closed (CHAOS-12533): two
// breakdown tables, one of them declared out of the answer path.
func directOnlyFact() CanonicalFact {
	return CanonicalFact{Fields: map[string]FactValue{
		"area": TableFactValue(FactTable{
			Shape: FactTableBreakdown, Key: []string{"day"}, Measures: []string{"churn", "wip"}, DirectOnlyColumns: []string{"wip"},
			Rows: []FactValueRow{directOnlyRow(map[string]FactValue{"day": StringFactValue("2026-11-02"), "churn": IntegerFactValue(3), "wip": IntegerFactValue(0)})},
		}),
		"coordinates": TableFactValue(FactTable{
			Shape: FactTableBreakdown, Key: []string{"map"}, Measures: []string{"x"}, DirectOnly: true,
			Rows: []FactValueRow{directOnlyRow(map[string]FactValue{"map": StringFactValue("m"), "x": NumberFactValue(4.5)})},
		}),
	}}
}

func TestADirectOnlyTableNeverMakesAFactAmbiguous(t *testing.T) {
	fact := directOnlyFact()
	rows, truncated := canonicalFieldRows(fact)
	if truncated {
		t.Fatal("a fact with one attachable table reported a drop")
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the one attachable table's row", len(rows))
	}
	if _, chart := rows[0].Fields["x"]; chart {
		t.Fatalf("the direct-only table reached a claim: %+v", rows[0].Fields)
	}
	table := canonicalFieldTable(fact)
	if table == nil || table.Field != "area" {
		t.Fatalf("declaration = %+v, want the area table", table)
	}
}

func TestTwoAttachableTablesStillFailClosed(t *testing.T) {
	fact := directOnlyFact()
	coordinates := fact.Fields["coordinates"]
	table := *coordinates.Table
	table.DirectOnly = false
	fact.Fields["coordinates"] = TableFactValue(table)
	rows, ambiguous := canonicalFieldRows(fact)
	if rows != nil || !ambiguous {
		t.Fatalf("rows = %+v, ambiguous = %v: two attachable tables must fail closed", rows, ambiguous)
	}
}

func TestAFactOfOnlyDirectOnlyTablesAttachesNothing(t *testing.T) {
	fact := directOnlyFact()
	delete(fact.Fields, "area")
	rows, ambiguous := canonicalFieldRows(fact)
	if rows != nil || ambiguous {
		t.Fatalf("rows = %+v, ambiguous = %v: nothing attaches and nothing is reported dropped", rows, ambiguous)
	}
	if table := canonicalFieldTable(fact); table != nil {
		t.Fatalf("declaration = %+v, want none", table)
	}
}

func TestADirectOnlyColumnLeavesTheRowAndTheDeclaration(t *testing.T) {
	fact := directOnlyFact()
	rows, _ := canonicalFieldRows(fact)
	if _, shown := rows[0].Fields["wip"]; shown {
		t.Fatalf("a direct-only column reached a claim: %+v", rows[0].Fields)
	}
	if _, kept := rows[0].Fields["churn"]; !kept {
		t.Fatalf("a measure beside it left too: %+v", rows[0].Fields)
	}
	table := canonicalFieldTable(fact)
	if slices.Contains(table.Measures, "wip") || !slices.Contains(table.Measures, "churn") {
		t.Fatalf("measures = %v, want churn without wip", table.Measures)
	}
	source := fact.Fields["area"]
	if _, stored := source.Rows[0].Fields["wip"]; !stored {
		t.Fatal("the attachment changed the canonical row, which read_facts serves")
	}
	if !slices.Contains(source.Table.Measures, "wip") {
		t.Fatal("the attachment changed the canonical declaration, which read_facts serves")
	}
}

func TestADirectOnlyColumnMustBeADeclaredMeasure(t *testing.T) {
	table := FactTable{Shape: FactTableBreakdown, Key: []string{"day"}, Measures: []string{"churn"}, DirectOnlyColumns: []string{"day"},
		Rows: []FactValueRow{directOnlyRow(map[string]FactValue{"day": StringFactValue("2026-11-02"), "churn": IntegerFactValue(3)})}}
	if err := table.Validate(); err == nil {
		t.Fatal("a key column was accepted as a direct-only measure")
	}
	table.DirectOnlyColumns = []string{"churn"}
	if err := table.Validate(); err != nil {
		t.Fatalf("a declared measure was refused: %v", err)
	}
}

// A time series beside the attached table is the dual-table form. A direct-only
// table must not count in that choice either.
func TestADirectOnlyTableDoesNotBreakTheDualTableChoice(t *testing.T) {
	fact := directOnlyFact()
	fact.Fields["series"] = TableFactValue(FactTable{
		Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"churn"},
		Rows: []FactValueRow{directOnlyRow(map[string]FactValue{"day": StringFactValue("2026-11-02"), "churn": IntegerFactValue(3)})},
	})
	field, ambiguous, ok := canonicalRowsField(fact)
	if !ok || ambiguous || field != "area" {
		t.Fatalf("field = %q, ambiguous = %v, ok = %v: want the area table beside the series", field, ambiguous, ok)
	}
}
