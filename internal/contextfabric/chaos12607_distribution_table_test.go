package contextfabric

import (
	"strings"
	"testing"
	"time"

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

// TestAClaimCarriesAWholeDistribution (Codex #1940 r1, P2): attaching a
// canonical fact's rows to its claim used the 64-row cap for every table, so a
// 359-row distribution reached the claim cut to 64 and still declared a
// distribution, and the answer-size cut never withheld it. A distribution is
// attached whole, beside the fact's daily series; one past its own cap is not
// attached at all.
func TestAClaimCarriesAWholeDistribution(t *testing.T) {
	// The daily series at its full 64 rows and four columns, as the provider
	// serves it, so the claim's combined cell and byte bounds (which apply
	// only when both tables are present, Codex #1940 r2) are exercised.
	daily := FactTable{Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"backlog_size", "throughput_mean", "throughput_stddev"}}
	for day := 0; day < maxFactValueRows; day++ {
		daily.Rows = append(daily.Rows, FactValueRow{Fields: map[string]FactValue{
			"day":               StringFactValue(time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, day).Format("2006-01-02")),
			"backlog_size":      IntegerFactValue(int64(100 + day)),
			"throughput_mean":   NumberFactValue(12.345678901234567),
			"throughput_stddev": NumberFactValue(1.2345678901234567),
		}})
	}
	fact := func(distribution FactTable) CanonicalFact {
		return CanonicalFact{
			Kind:    FactWorkload,
			Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:chaos", Label: "CHAOS"},
			Fields: map[string]FactValue{
				"forecast_p50_days":       IntegerFactValue(12),
				"daily_workload":          TableFactValue(daily),
				"completion_distribution": TableFactValue(distribution),
			},
		}
	}
	claim := func() ClaimedFact {
		p50 := int64(12)
		return ClaimedFact{
			ClaimID: "claim_workload_chaos", Kind: contractsv1.ContextFabricFactKind(FactWorkload),
			Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:chaos", Label: "CHAOS"},
			Field:   "forecast_p50_days", Value: contractsv1.ContextFabricScalarValue{Integer: &p50},
		}
	}

	whole := fact(distributionFactTable(359))
	claims, _, _, truncated := attachCanonicalRows([]ClaimedFact{claim()}, []CanonicalFact{whole})
	if truncated || len(claims[0].Rows) != 359 || claims[0].Table == nil || claims[0].Table.Shape != contractsv1.ContextFabricFactTableShapeDistribution {
		t.Fatalf("359-row distribution: rows = %d, table = %+v, truncated = %t; want it whole", len(claims[0].Rows), claims[0].Table, truncated)
	}
	if len(claims[0].TimeSeriesRows) != maxFactValueRows || claims[0].TimeSeriesTable == nil {
		t.Fatalf("the daily series beside it: %d rows, table %+v", len(claims[0].TimeSeriesRows), claims[0].TimeSeriesTable)
	}
	if err := claims[0].Validate(); err != nil {
		t.Fatalf("the claim with a whole distribution: Validate() = %v", err)
	}

	over := distributionFactTable(MaxFactDistributionRows)
	over.Rows = append(over.Rows, FactValueRow{Fields: map[string]FactValue{
		"day": IntegerFactValue(int64(MaxFactDistributionRows)), "runs": IntegerFactValue(1), "cumulative_share": NumberFactValue(1),
	}})
	claims, _, _, truncated = attachCanonicalRows([]ClaimedFact{claim()}, []CanonicalFact{fact(over)})
	if !truncated || claims[0].Rows != nil || claims[0].Table != nil {
		t.Fatalf("a distribution past its cap: rows = %d, table = %+v, truncated = %t; want it withheld whole and reported", len(claims[0].Rows), claims[0].Table, truncated)
	}

	breakdown := distributionFactTable(maxFactValueRows + 1)
	breakdown.Shape = FactTableBreakdown
	claims, _, _, truncated = attachCanonicalRows([]ClaimedFact{claim()}, []CanonicalFact{fact(breakdown)})
	if !truncated || len(claims[0].Rows) != maxFactValueRows {
		t.Fatalf("a breakdown past 64: rows = %d, truncated = %t; want it cut to 64 as before", len(claims[0].Rows), truncated)
	}
}
