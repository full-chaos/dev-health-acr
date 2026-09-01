package contextfabric

import "testing"

// chaos4680_fact_table_observations_test.go pins FactTable's third declared
// role (CHAOS-4680): a per-row categorical OBSERVATION is neither a row's
// identity nor a quantity it measures. Every case here is red on origin/main
// (Observations does not exist there, and Measures carries no numeric
// requirement) and green on this branch.

// declaredHealthLikeTable mirrors health.go's real pre-fix shape: a
// time_series whose second column is a per-day categorical value. Building
// it inline (rather than importing devhealthfacts, which would be a import
// cycle from this package) keeps the executed defect from CHAOS-4680's own
// evidence trail directly reproducible here.
func declaredHealthLikeTable(severityInObservations bool) FactTable {
	table := FactTable{
		Shape: FactTableTimeSeries,
		Key:   []string{"day"},
		Rows: []FactValueRow{
			{Fields: map[string]FactValue{
				"day":              StringFactValue("2026-08-15"),
				"compounding_risk": NumberFactValue(0.61),
				"severity":         StringFactValue("high"),
			}},
			{Fields: map[string]FactValue{
				"day":              StringFactValue("2026-08-16"),
				"compounding_risk": NumberFactValue(0.42),
				"severity":         StringFactValue("elevated"),
			}},
		},
	}
	if severityInObservations {
		table.Measures = []string{"compounding_risk"}
		table.Observations = []string{"severity"}
	} else {
		table.Measures = []string{"compounding_risk", "severity"}
	}
	return table
}

// TestFactTableValidateRejectsANonNumericMeasure is the core CHAOS-4680 pin:
// the "obvious fix" S6 could not ship (RenderShapeSkipUnresolvableMeasureRoles's
// own doc comment) because it invalidated health.go's real declaration. It is
// safe now that severity has somewhere else to go.
func TestFactTableValidateRejectsANonNumericMeasure(t *testing.T) {
	table := declaredHealthLikeTable(false)
	err := table.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want an error: a Measures column with a non-numeric cell must be rejected")
	}
	if got := err.Error(); got == "" {
		t.Fatal("Validate() returned an empty error message")
	}
}

// TestFactTableValidateAcceptsANonNumericObservation proves the fix: the
// IDENTICAL data validates once severity is declared where it belongs.
func TestFactTableValidateAcceptsANonNumericObservation(t *testing.T) {
	table := declaredHealthLikeTable(true)
	if err := table.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil: a categorical Observation must not be required to be numeric", err)
	}
}

// TestFactTableValidateMeasureAbsentOrNullCellIsNotDisqualifying mirrors the
// existing validTimeSeriesTable() row[1] omission (prs_merged missing) but
// pins it explicitly against the NEW numeric-measures rule: a conditionally
// computed measure being absent, or explicitly null, must not trip the
// numeric check -- only a PRESENT, non-numeric cell does.
func TestFactTableValidateMeasureAbsentOrNullCellIsNotDisqualifying(t *testing.T) {
	table := validTimeSeriesTable()
	table.Rows[1].Fields["prs_merged"] = NullFactValue()
	if err := table.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil: an explicit null measure cell is missing data, not a role violation", err)
	}
}

// TestFactTableValidateRejectsColumnDeclaredInMoreThanOneRole extends the
// pre-existing Key/Measures overlap pin to the third role: Observations must
// not overlap Key OR Measures.
func TestFactTableValidateRejectsColumnDeclaredInMoreThanOneRole(t *testing.T) {
	overlapsMeasures := declaredHealthLikeTable(true)
	overlapsMeasures.Observations = append(overlapsMeasures.Observations, "compounding_risk")
	if err := overlapsMeasures.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a column may not be both a measure and an observation")
	}

	overlapsKey := declaredHealthLikeTable(true)
	overlapsKey.Observations = append(overlapsKey.Observations, "day")
	if err := overlapsKey.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a column may not be both key and observation")
	}
}

// TestFactTableValidateRejectsBlankObservationColumnName mirrors the
// existing blank-name checks Key/Measures already carry.
func TestFactTableValidateRejectsBlankObservationColumnName(t *testing.T) {
	table := declaredHealthLikeTable(true)
	table.Observations = []string{"  "}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a blank observation column name must be rejected")
	}
}

// TestFactTableValidateRejectsTooManyObservations mirrors the existing
// maxFactTableMeasures bound.
func TestFactTableValidateRejectsTooManyObservations(t *testing.T) {
	table := declaredHealthLikeTable(true)
	table.Observations = make([]string, maxFactTableObservations+1)
	for i := range table.Observations {
		table.Observations[i] = "obs"
	}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: observations exceeding the bound must be rejected")
	}
}

// TestFactTableValidateRejectsColumnNotDeclaredInKeyMeasuresOrObservations
// extends the pre-existing "undeclared column" pin: a column present on a
// row but absent from all three roles must still be rejected, now that a
// third role exists to check membership against.
func TestFactTableValidateRejectsColumnNotDeclaredInKeyMeasuresOrObservations(t *testing.T) {
	table := declaredHealthLikeTable(true)
	table.Rows[0].Fields["region"] = StringFactValue("us-east") // constant, undeclared
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a column not in key, measures, or observations must be rejected")
	}
}

// TestTableFactValueCarriesObservationsThroughUnchanged proves TableFactValue
// (the P1 dual-write helper) passes Observations through exactly like Key
// and Measures -- no separate wiring was needed for the third role, but this
// pins that observation as fact rather than assumption.
func TestTableFactValueCarriesObservationsThroughUnchanged(t *testing.T) {
	table := declaredHealthLikeTable(true)
	value := TableFactValue(table)
	if len(value.Table.Observations) != 1 || value.Table.Observations[0] != "severity" {
		t.Fatalf("Table.Observations = %#v, want [severity]", value.Table.Observations)
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}
