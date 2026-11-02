package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12607: the producer-side `distribution` table. Its rows are one
// completion distribution, so they have their own bound, one ascending
// integer axis, and are served whole or withheld, never cut.

func distributionFactTable(n int) FactTable {
	rows := make([]FactValueRow, 0, n)
	for day := 0; day < n; day++ {
		rows = append(rows, FactValueRow{Fields: map[string]FactValue{
			"day":              IntegerFactValue(int64(day)),
			"runs":             IntegerFactValue(1),
			"cumulative_share": NumberFactValue(float64(day+1) / float64(n)),
		}})
	}
	return FactTable{Shape: FactTableDistribution, Key: []string{"day"}, Measures: []string{"runs", "cumulative_share"}, Rows: rows}
}

func TestADistributionFactTableCarriesItsWholeAxis(t *testing.T) {
	for _, n := range []int{1, maxFactValueRows + 1, 359, MaxFactDistributionRows} {
		table := distributionFactTable(n)
		if err := table.Validate(); err != nil {
			t.Fatalf("%d rows: Validate() = %v", n, err)
		}
		if err := TableFactValue(table).Validate(); err != nil {
			t.Fatalf("%d rows as a fact value: Validate() = %v", n, err)
		}
	}
	if err := distributionFactTable(MaxFactDistributionRows + 1).Validate(); err == nil {
		t.Fatalf("%d rows validated, want the row bound", MaxFactDistributionRows+1)
	}
	if err := TableFactValue(distributionFactTable(MaxFactDistributionRows + 1)).Validate(); err == nil {
		t.Fatalf("%d rows validated as a fact value, want the row bound", MaxFactDistributionRows+1)
	}
	breakdown := distributionFactTable(maxFactValueRows + 1)
	breakdown.Shape = FactTableBreakdown
	if err := breakdown.Validate(); err == nil {
		t.Fatalf("a %d-row breakdown validated: only a distribution gets the larger bound", maxFactValueRows+1)
	}
	if MaxFactDistributionRows != contractsv1.ContextFabricClaimedFactDistributionMaxRows {
		t.Fatalf("producer bound %d, wire bound %d", MaxFactDistributionRows, contractsv1.ContextFabricClaimedFactDistributionMaxRows)
	}
	if string(FactTableDistribution) != string(contractsv1.ContextFabricFactTableShapeDistribution) {
		t.Fatalf("producer shape %q, wire shape %q", FactTableDistribution, contractsv1.ContextFabricFactTableShapeDistribution)
	}
}

func TestADistributionFactTableAxisIsAscendingIntegers(t *testing.T) {
	descending := distributionFactTable(3)
	descending.Rows[0], descending.Rows[2] = descending.Rows[2], descending.Rows[0]
	if err := descending.Validate(); err == nil || !strings.Contains(err.Error(), "not above the row before it") {
		t.Fatalf("descending: Validate() = %v", err)
	}
	text := distributionFactTable(3)
	text.Rows[1].Fields["day"] = StringFactValue("1")
	if err := text.Validate(); err == nil || !strings.Contains(err.Error(), "is not an integer") {
		t.Fatalf("a string day: Validate() = %v", err)
	}
	twoKeys := distributionFactTable(3)
	twoKeys.Key, twoKeys.Measures = []string{"day", "runs"}, []string{"cumulative_share"}
	if err := twoKeys.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one column") {
		t.Fatalf("two key columns: Validate() = %v", err)
	}
}

// TestTheRowCutWithholdsADistributionWholeAndCutsOtherTables: under the
// answer-size row cut, a distribution over the cap loses its rows and its
// declaration together (a table with no rows would not validate), and is
// counted apart; a breakdown beside it is cut as before.
func TestTheRowCutWithholdsADistributionWholeAndCutsOtherTables(t *testing.T) {
	distribution := claimedTableFact("claim_distribution", contractsv1.ContextFabricFactTableShapeDistribution, 359)
	short := claimedTableFact("claim_short_distribution", contractsv1.ContextFabricFactTableShapeDistribution, 5)
	breakdown := claimedTableFact("claim_breakdown", contractsv1.ContextFabricFactTableShapeBreakdown, 40)
	claims := []ClaimedFact{distribution, short, breakdown}

	truncated, served, rules := truncateClaimedFactRows(claims, 10)
	if truncated[0].Rows != nil || truncated[0].Table != nil {
		t.Fatalf("distribution over the cap: rows = %d, table = %v; want both withheld", len(truncated[0].Rows), truncated[0].Table)
	}
	if len(truncated[1].Rows) != 5 || truncated[1].Table == nil {
		t.Fatalf("distribution within the cap: rows = %d, table = %v; want it whole", len(truncated[1].Rows), truncated[1].Table)
	}
	if len(truncated[2].Rows) != 10 || truncated[2].Table == nil {
		t.Fatalf("breakdown: rows = %d; want it cut to 10", len(truncated[2].Rows))
	}
	if served != 15 || rules.Total != 2 || rules.DistributionWithheld != 1 || rules.SourcePrefix != 1 {
		t.Fatalf("served = %d, rules = %+v; want 15 served, one withheld distribution, one cut breakdown", served, rules)
	}
	if len(claims[0].Rows) != 359 || claims[0].Table == nil {
		t.Fatal("the input claim was written: the cut must work on a copy")
	}
	for _, claim := range truncated {
		if err := claim.Validate(); err != nil {
			t.Fatalf("%s after the cut: Validate() = %v", claim.ClaimID, err)
		}
	}
}

func claimedTableFact(claimID string, shape contractsv1.ContextFabricFactTableShape, n int) ClaimedFact {
	rows := make([]contractsv1.ContextFabricClaimedFactRow, 0, n)
	for day := 0; day < n; day++ {
		key, runs := int64(day), int64(1)
		share := float64(day+1) / float64(n)
		rows = append(rows, contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
			"day": {Integer: &key}, "runs": {Integer: &runs}, "cumulative_share": {Number: &share},
		}})
	}
	p50 := int64(12)
	return ClaimedFact{
		ClaimID: claimID,
		Kind:    "workload",
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:chaos", Label: "CHAOS"},
		Field:   "forecast_p50_days",
		Value:   contractsv1.ContextFabricScalarValue{Integer: &p50},
		Rows:    rows,
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: "completion_distribution", Shape: shape, Key: []string{"day"}, Measures: []string{"runs", "cumulative_share"},
		},
	}
}
