package v1

import "testing"

// chaos4680_claimed_fact_table_observations_test.go pins the wire
// declaration's third role (CHAOS-4680): ContextFabricClaimedFactTable
// carries Observations alongside Key/Measures, so the domain's third role
// moves to the wire with it. Every case here is red on origin/main
// (Observations does not exist there) and green on this branch.

func validClaimedTimeSeriesTable() ContextFabricClaimedFactTable {
	return ContextFabricClaimedFactTable{
		Field:    "daily_health",
		Shape:    ContextFabricFactTableShapeTimeSeries,
		Key:      []string{"day"},
		Measures: []string{"compounding_risk"},
	}
}

func TestContextFabricClaimedFactTableValidateAcceptsObservations(t *testing.T) {
	table := validClaimedTimeSeriesTable()
	table.Observations = []string{"severity"}
	if err := table.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestContextFabricClaimedFactTableHasObservation(t *testing.T) {
	table := validClaimedTimeSeriesTable()
	table.Observations = []string{"severity"}
	if !table.HasObservation("severity") {
		t.Fatal("HasObservation(\"severity\") = false, want true")
	}
	if table.HasObservation("compounding_risk") {
		t.Fatal("HasObservation(\"compounding_risk\") = true, want false: that column is a measure, not an observation")
	}
}

func TestContextFabricClaimedFactTableValidateRejectsColumnInBothMeasuresAndObservations(t *testing.T) {
	table := validClaimedTimeSeriesTable()
	table.Observations = []string{"compounding_risk"}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a column may not be both a measure and an observation")
	}
}

func TestContextFabricClaimedFactTableValidateRejectsColumnInBothKeyAndObservations(t *testing.T) {
	table := validClaimedTimeSeriesTable()
	table.Observations = []string{"day"}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a column may not be both key and observation")
	}
}

func TestContextFabricClaimedFactTableValidateRejectsTooManyObservations(t *testing.T) {
	table := validClaimedTimeSeriesTable()
	table.Observations = make([]string, ContextFabricFactTableObservationsMaxCount+1)
	for i := range table.Observations {
		table.Observations[i] = "obs" + string(rune('a'+i%26))
	}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: observations exceeding the bound must be rejected")
	}
}

func TestContextFabricClaimedFactTableValidateRejectsBlankObservationColumnName(t *testing.T) {
	table := validClaimedTimeSeriesTable()
	table.Observations = []string{"  "}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error: a blank observation column name must be rejected")
	}
}
