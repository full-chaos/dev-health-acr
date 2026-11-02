package v1

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// chaos11984_measure_units_test.go pins the wire declaration of a measure's
// unit (CHAOS-11984): a claimed table may state, for each of its measures, the
// unit its producer declared, from a closed vocabulary. Every case is red on
// the base (MeasureUnits does not exist there).

func unitTable() ContextFabricClaimedFactTable {
	return ContextFabricClaimedFactTable{
		Field:        "daily_metrics",
		Shape:        ContextFabricFactTableShapeTimeSeries,
		Key:          []string{"day"},
		Measures:     []string{"commits_count", "median_pr_cycle_hours", "revert_rate"},
		Observations: []string{"severity"},
		MeasureUnits: []ContextFabricMeasureUnitDeclaration{
			{Measure: "commits_count", Unit: ContextFabricMeasureUnitCount},
			{Measure: "median_pr_cycle_hours", Unit: ContextFabricMeasureUnitHours},
		},
	}
}

func TestCHAOS11984ATableMayStateTheUnitOfItsMeasures(t *testing.T) {
	table := unitTable()
	if err := table.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if unit, ok := table.MeasureUnit("median_pr_cycle_hours"); !ok || unit != ContextFabricMeasureUnitHours {
		t.Fatalf("MeasureUnit(median_pr_cycle_hours) = %q, %v; want hours, true", unit, ok)
	}
	// A measure with no entry has no unit: the consumer adds none.
	if unit, ok := table.MeasureUnit("revert_rate"); ok {
		t.Fatalf("MeasureUnit(revert_rate) = %q, true; want no unit, it states none", unit)
	}
	table.MeasureUnits = nil
	if err := table.Validate(); err != nil {
		t.Fatalf("Validate() with no units error = %v, want nil: the field is optional", err)
	}
}

func TestCHAOS11984EveryVocabularyUnitIsAccepted(t *testing.T) {
	for _, unit := range ContextFabricMeasureUnitVocabulary() {
		table := unitTable()
		table.MeasureUnits = []ContextFabricMeasureUnitDeclaration{{Measure: "commits_count", Unit: unit}}
		if err := table.Validate(); err != nil {
			t.Errorf("unit %q: Validate() error = %v, want nil", unit, err)
		}
	}
}

// Each refusal is matched on its own message, so a case fails only at the
// guard it names.
func TestCHAOS11984AUnitOnlyNamesADeclaredMeasureOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		units []ContextFabricMeasureUnitDeclaration
		want  string
	}{
		{"key column", []ContextFabricMeasureUnitDeclaration{{Measure: "day", Unit: ContextFabricMeasureUnitDays}}, "is not one of its measures"},
		{"observation column", []ContextFabricMeasureUnitDeclaration{{Measure: "severity", Unit: ContextFabricMeasureUnitCount}}, "is not one of its measures"},
		{"unknown column", []ContextFabricMeasureUnitDeclaration{{Measure: "mttr_hours", Unit: ContextFabricMeasureUnitHours}}, "is not one of its measures"},
		{"stated twice", []ContextFabricMeasureUnitDeclaration{
			{Measure: "commits_count", Unit: ContextFabricMeasureUnitCount},
			{Measure: "commits_count", Unit: ContextFabricMeasureUnitCount},
		}, "more than once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := unitTable()
			table.MeasureUnits = tc.units
			err := table.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestCHAOS11984AnUnknownUnitTokenIsRefused(t *testing.T) {
	// loc, lines, score, share and axis are declared by producers today and
	// are not served; a blank or differently cased token is no member either.
	for _, token := range []ContextFabricMeasureUnit{"loc", "lines", "score", "share", "axis", "", "Hours", " hours"} {
		table := unitTable()
		table.MeasureUnits = []ContextFabricMeasureUnitDeclaration{{Measure: "commits_count", Unit: token}}
		err := table.Validate()
		if err == nil || !strings.Contains(err.Error(), "closed vocabulary") {
			t.Errorf("unit %q: Validate() error = %v, want a closed-vocabulary refusal", token, err)
		}
	}
}

// The unit check runs wherever the table is validated: on the time_series
// table and on the legacy table alike.
func TestCHAOS11984BothClaimedTablesCheckTheirUnits(t *testing.T) {
	day := "2026-11-01"
	rows := []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{"day": {String: &day}}}}
	table := unitTable()
	table.MeasureUnits = []ContextFabricMeasureUnitDeclaration{{Measure: "commits_count", Unit: "loc"}}
	if err := validateTimeSeriesTable(&table, rows); err == nil || !strings.Contains(err.Error(), "closed vocabulary") {
		t.Fatalf("validateTimeSeriesTable() error = %v, want a closed-vocabulary refusal", err)
	}
	if err := validateClaimedFactTable(&table, rows); err == nil || !strings.Contains(err.Error(), "closed vocabulary") {
		t.Fatalf("validateClaimedFactTable() error = %v, want a closed-vocabulary refusal", err)
	}
}

// The Go vocabulary and every published enum are one list, in one order.
func TestCHAOS11984TheUnitVocabularyIsTheSchemaEnum(t *testing.T) {
	vocabulary := ContextFabricMeasureUnitVocabulary()
	want := make([]string, 0, len(vocabulary))
	for _, unit := range vocabulary {
		want = append(want, string(unit))
	}
	documents := schemaDocuments(t)
	for _, reference := range []string{
		"common#$defs.ClaimedFactTable.properties.measure_units.items.properties.unit",
		"answer#$defs.ProjectedFactTable.properties.measure_units.items.properties.unit",
	} {
		node := schemaNodeAt(t, documents, reference)
		raw, err := json.Marshal(node["enum"])
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: enum is not a list of strings: %v", reference, err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s enum = %v, want the Go vocabulary %v", reference, got, want)
		}
	}
}

// The wire encodes the units under measure_units, omitted when none is stated.
func TestCHAOS11984TheUnitsEncodeUnderMeasureUnits(t *testing.T) {
	table := unitTable()
	encoded, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"measure_units":[{"measure":"commits_count","unit":"count"},{"measure":"median_pr_cycle_hours","unit":"hours"}]`) {
		t.Fatalf("encoded table = %s, want measure_units in measures order", encoded)
	}
	table.MeasureUnits = nil
	encoded, err = json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "measure_units") {
		t.Fatalf("encoded table = %s, want no measure_units key when none is stated", encoded)
	}
}
