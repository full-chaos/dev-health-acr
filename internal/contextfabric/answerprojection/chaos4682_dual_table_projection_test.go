package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestProjectCopiesTheAdditiveTimeSeriesPair is codex round 1's P1 finding
// on CHAOS-4682 (§5.1 P2): Project() copied Rows/Table onto a retained
// driver's cited claims but dropped the additive TimeSeriesRows/
// TimeSeriesTable pair entirely, silently defeating P2 on the bounded
// answer-projection surface Ask Dev actually reads from -- a dual-table
// fact's trend would reach the canonical result but never the projection.
//
// EXECUTED repro that surfaced the gap (codex, reproduced independently
// here as this test): a canonical claim carrying both pairs projected with
// rows=1 table=legacy_table time_series_rows=0 time_series_table_nil=true.
func TestProjectCopiesTheAdditiveTimeSeriesPair(t *testing.T) {
	result := richResult()
	for i := range result.ClaimedFacts {
		if result.ClaimedFacts[i].ClaimID != "claim_status_001" {
			continue
		}
		result.ClaimedFacts[i].Rows = []contractsv1.ContextFabricClaimedFactRow{{Fields: map[string]contractsv1.ContextFabricScalarValue{"team_id": stringValue("team_a")}}}
		result.ClaimedFacts[i].Table = &contractsv1.ContextFabricClaimedFactTable{
			Field: "team_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"team_id"}, Measures: []string{"open_items"},
		}
		result.ClaimedFacts[i].TimeSeriesRows = []contractsv1.ContextFabricClaimedFactRow{
			{Fields: map[string]contractsv1.ContextFabricScalarValue{"day": stringValue("2026-08-03")}},
		}
		result.ClaimedFacts[i].TimeSeriesTable = &contractsv1.ContextFabricClaimedFactTable{
			Field: "daily_status", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries, Key: []string{"day"}, Measures: []string{"open_items"},
		}
	}

	projection := Project(result, Budget{})
	var got *contractsv1.ContextFabricProjectedFact
	for i := range projection.KeyFacts {
		if projection.KeyFacts[i].ClaimID == "claim_status_001" {
			got = &projection.KeyFacts[i]
		}
	}
	if got == nil {
		t.Fatal("claim_status_001 did not reach the projection at all")
	}
	// Non-vacuity: the legacy pair must still be there, unchanged --
	// otherwise this test would not be distinguishing "dropped everything"
	// from "dropped only the new pair".
	if len(got.Rows) != 1 || got.Table == nil {
		t.Fatalf("legacy Rows/Table were not copied: rows=%d table_nil=%v", len(got.Rows), got.Table == nil)
	}
	if len(got.TimeSeriesRows) != 1 {
		t.Fatalf("TimeSeriesRows = %d entries, want 1 -- the additive pair was dropped by Project()", len(got.TimeSeriesRows))
	}
	if got.TimeSeriesTable == nil {
		t.Fatal("TimeSeriesTable is nil, want the copied declaration -- the additive pair was dropped by Project()")
	}
	if got.TimeSeriesTable.Field != "daily_status" {
		t.Fatalf("TimeSeriesTable.Field = %q, want daily_status", got.TimeSeriesTable.Field)
	}
}
