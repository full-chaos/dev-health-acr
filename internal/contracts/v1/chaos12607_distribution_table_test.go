package v1

import (
	"strings"
	"testing"
)

// chaos12607_distribution_table_test.go pins the `distribution` table shape
// (CHAOS-12607, CHAOS-4613 ruling 4): a completion forecast's distribution is
// one row per distinct day to the 365-day horizon, the largest measured on
// askdev-1 has 359 rows, and every other table keeps the 64-row cap.

// distributionRows returns n rows keyed by day 0..n-1, each with runs and a
// cumulative share: the shape the workload fact serves.
func distributionRows(n int) []ContextFabricClaimedFactRow {
	rows := make([]ContextFabricClaimedFactRow, 0, n)
	for day := 0; day < n; day++ {
		key := int64(day)
		runs := int64(1)
		share := float64(day+1) / float64(n)
		rows = append(rows, ContextFabricClaimedFactRow{Fields: map[string]ContextFabricScalarValue{
			"day": {Integer: &key}, "runs": {Integer: &runs}, "cumulative_share": {Number: &share},
		}})
	}
	return rows
}

func claimedFactWithTable(shape ContextFabricFactTableShape, rows []ContextFabricClaimedFactRow) ContextFabricClaimedFact {
	fact := validClaimedFactWithLegacyTable()
	fact.Subject = ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:chaos", Label: "CHAOS"}
	fact.Field = "forecast_p50_days"
	fact.Rows = rows
	fact.Table = &ContextFabricClaimedFactTable{
		Field: "completion_distribution", Shape: shape,
		Key: []string{"day"}, Measures: []string{"runs", "cumulative_share"},
	}
	return fact
}

// TestABreakdownCannotCarryAWholeCompletionDistribution is ruling 4's reason
// for the new shape, shown rather than argued: the same 359 rows the largest
// askdev-1 forecast holds are refused as a breakdown, because the claimed-fact
// row cap is one cap for every non-distribution table.
func TestABreakdownCannotCarryAWholeCompletionDistribution(t *testing.T) {
	fact := claimedFactWithTable(ContextFabricFactTableShapeBreakdown, distributionRows(359))
	err := fact.Validate()
	if err == nil || !strings.Contains(err.Error(), "claimed fact rows exceed v1 bounds") {
		t.Fatalf("a 359-row breakdown: Validate() = %v, want the row bound refusal", err)
	}
	if got := ContextFabricClaimedFactRowsMax(fact.Table); got != ContextFabricClaimedFactMaxRows {
		t.Fatalf("a breakdown's row cap = %d, want %d", got, ContextFabricClaimedFactMaxRows)
	}
}

func TestADistributionCarriesEveryDayToTheHorizonAndNoMore(t *testing.T) {
	for _, n := range []int{1, 64, 65, 359, ContextFabricClaimedFactDistributionMaxRows} {
		fact := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(n))
		if err := fact.Validate(); err != nil {
			t.Fatalf("a %d-row distribution: Validate() = %v, want nil", n, err)
		}
	}
	over := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(ContextFabricClaimedFactDistributionMaxRows+1))
	if err := over.Validate(); err == nil || !strings.Contains(err.Error(), "claimed fact rows exceed v1 bounds") {
		t.Fatalf("a %d-row distribution: Validate() = %v, want the row bound refusal", ContextFabricClaimedFactDistributionMaxRows+1, err)
	}
}

// TestOnlyTheDistributionShapeGetsTheLargerCap: the larger cap follows the
// declared shape, not the field or the fact kind, and rows with no
// declaration keep 64.
func TestOnlyTheDistributionShapeGetsTheLargerCap(t *testing.T) {
	for _, shape := range []ContextFabricFactTableShape{ContextFabricFactTableShapeBreakdown, ContextFabricFactTableShapeRanking, ContextFabricFactTableShapeTimeSeries} {
		table := &ContextFabricClaimedFactTable{Field: "completion_distribution", Shape: shape}
		if got := ContextFabricClaimedFactRowsMax(table); got != ContextFabricClaimedFactMaxRows {
			t.Errorf("%s row cap = %d, want %d", shape, got, ContextFabricClaimedFactMaxRows)
		}
	}
	if got := ContextFabricClaimedFactRowsMax(nil); got != ContextFabricClaimedFactMaxRows {
		t.Errorf("undeclared row cap = %d, want %d", got, ContextFabricClaimedFactMaxRows)
	}
	undeclared := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(65))
	undeclared.Table = nil
	if err := undeclared.Validate(); err == nil {
		t.Fatal("65 undeclared rows validated: the larger cap must need a distribution declaration")
	}
}

func TestADistributionKeyIsOneAscendingIntegerAxis(t *testing.T) {
	twoKeys := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(3))
	twoKeys.Table.Key = []string{"day", "runs"}
	twoKeys.Table.Measures = []string{"cumulative_share"}
	if err := twoKeys.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one key column") {
		t.Fatalf("two key columns: Validate() = %v, want the key arity refusal", err)
	}

	repeated := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(3))
	same := int64(0)
	repeated.Rows[1].Fields["day"] = ContextFabricScalarValue{Integer: &same}
	if err := repeated.Validate(); err == nil || !strings.Contains(err.Error(), "not above the row before it") {
		t.Fatalf("a repeated day: Validate() = %v, want the ascending refusal", err)
	}

	descending := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(3))
	descending.Rows[0], descending.Rows[2] = descending.Rows[2], descending.Rows[0]
	if err := descending.Validate(); err == nil || !strings.Contains(err.Error(), "not above the row before it") {
		t.Fatalf("descending days: Validate() = %v, want the ascending refusal", err)
	}

	text := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(3))
	label := "12"
	text.Rows[0].Fields["day"] = ContextFabricScalarValue{String: &label}
	if err := text.Validate(); err == nil || !strings.Contains(err.Error(), "is not an integer") {
		t.Fatalf("a string day: Validate() = %v, want the integer refusal", err)
	}

	// Other shapes are not held to the axis rule.
	breakdown := claimedFactWithTable(ContextFabricFactTableShapeBreakdown, distributionRows(3))
	breakdown.Rows[0], breakdown.Rows[2] = breakdown.Rows[2], breakdown.Rows[0]
	if err := breakdown.Validate(); err != nil {
		t.Fatalf("an unordered breakdown: Validate() = %v, want nil", err)
	}
}
