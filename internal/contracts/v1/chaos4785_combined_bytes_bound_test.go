package v1

import (
	"fmt"
	"strings"
	"testing"
)

// chaos4785_combined_bytes_bound_test.go pins CHAOS-4785: Rows and
// TimeSeriesRows were bounded independently, so a fact contract-legal on
// EACH collection alone could serialize far past MaxSerializedBytes once
// BOTH were populated at their per-table maximum -- a real, producible
// dual-table shape since CHAOS-4682 (§5.1 P2). Phase-1 verified this
// UNPRODUCED by any of today's five TimeSeriesRows producers against real
// data (largest observed fact: 16,246 bytes). This file's cases are RED on
// origin/main (validateClaimedFactRowsCombined does not exist there) and
// GREEN on this branch.

// maxLegalClaimedFactRows builds rowCount rows, each at the contract's own
// per-row maximum (ContextFabricClaimedFactRowMaxFields fields, every
// field's key and String value at their own individual max length) -- the
// most bytes a single legal row can carry.
func maxLegalClaimedFactRows(rowCount int) []ContextFabricClaimedFactRow {
	longValue := strings.Repeat("v", ContextFabricClaimedFactValueMaxLength)
	rows := make([]ContextFabricClaimedFactRow, rowCount)
	for i := range rows {
		fields := make(map[string]ContextFabricScalarValue, ContextFabricClaimedFactRowMaxFields)
		for f := 0; f < ContextFabricClaimedFactRowMaxFields; f++ {
			key := fmt.Sprintf("field_%02d_", f) + strings.Repeat("k", ContextFabricClaimedFieldMaxLength-len(fmt.Sprintf("field_%02d_", f)))
			value := longValue
			fields[key] = ContextFabricScalarValue{String: &value}
		}
		rows[i] = ContextFabricClaimedFactRow{Fields: fields}
	}
	return rows
}

// TestContextFabricClaimedFactValidateRejectsMaxLegalDualTableCombination
// is CHAOS-4785's central red-first case: a fact whose Rows AND
// TimeSeriesRows are EACH, independently, exactly at the per-table legal
// maximum (validateClaimedFactRows alone accepts either one on its own --
// proven by the sibling accept-case below) is refused once both are
// combined, because together they would serialize roughly 16.9M content
// bytes against a 1 MiB (ContextFabricSerializedBytesMax) service ceiling.
func TestContextFabricClaimedFactValidateRejectsMaxLegalDualTableCombination(t *testing.T) {
	fact := validClaimedFactWithLegacyTable()
	maxRows := maxLegalClaimedFactRows(ContextFabricClaimedFactMaxRows)
	fact.Rows = maxRows
	fact.Table = &ContextFabricClaimedFactTable{
		Field: "backlog_size", Shape: ContextFabricFactTableShapeBreakdown,
		Key: []string{"field_00_" + strings.Repeat("k", ContextFabricClaimedFieldMaxLength-len("field_00_"))},
	}
	fact.TimeSeriesRows = maxLegalClaimedFactRows(ContextFabricClaimedFactMaxRows)
	fact.TimeSeriesTable = &ContextFabricClaimedFactTable{
		Field: "daily_workload", Shape: ContextFabricFactTableShapeTimeSeries,
		Key: []string{"field_00_" + strings.Repeat("k", ContextFabricClaimedFieldMaxLength-len("field_00_"))},
	}

	// Each collection alone is legal -- isolating that the COMBINATION,
	// not either table by itself, is what triggers the rejection.
	if err := validateClaimedFactRows(fact.Rows); err != nil {
		t.Fatalf("Rows alone: validateClaimedFactRows() error = %v, want nil (Rows alone must stay legal)", err)
	}
	if err := validateClaimedFactRows(fact.TimeSeriesRows); err != nil {
		t.Fatalf("TimeSeriesRows alone: validateClaimedFactRows() error = %v, want nil (TimeSeriesRows alone must stay legal)", err)
	}

	if err := fact.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: Rows+TimeSeriesRows combined exceed the CHAOS-4785 joint bound")
	}
}

// TestContextFabricClaimedFactValidateAcceptsDualTableUnderTheCombinedBound
// is the both-shapes fixture's accept half: a genuinely dual-table fact
// (both Rows and TimeSeriesRows populated, the real CHAOS-4682 shape) whose
// combined cells/bytes stay under the new joint bound still validates --
// the fix must not make an ordinary dual-table fact unservable, only the
// pathological maximum.
func TestContextFabricClaimedFactValidateAcceptsDualTableUnderTheCombinedBound(t *testing.T) {
	fact := validClaimedFactWithLegacyTable()
	fact.TimeSeriesRows = []ContextFabricClaimedFactRow{validTimeSeriesRow()}
	fact.TimeSeriesTable = &ContextFabricClaimedFactTable{
		Field: "daily_workload", Shape: ContextFabricFactTableShapeTimeSeries,
		Key: []string{"day"}, Measures: []string{"backlog_size"},
	}
	if err := fact.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil (a small real-shaped dual-table fact must stay legal)", err)
	}
}

// TestCHAOS4785RegressionSingleTableAloneAtMaxMustNotBeRejected pins the
// bug an earlier draft of this fix introduced and this test caught before
// it shipped: ContextFabricClaimedFactCombinedContentBytesMax (262,144
// bytes, sized off REAL dual-table data) is far SMALLER than one table's
// own pre-existing legal maximum (~8.45M content bytes) -- so a fact using
// ONLY Rows (or only TimeSeriesRows), CHAOS-4347's shape and legal since
// long before this ticket, must NEVER be rejected by the new joint check
// just because a full-legal single table happens to be large. The joint
// bound applies exclusively to the COMBINATION.
func TestCHAOS4785RegressionSingleTableAloneAtMaxMustNotBeRejected(t *testing.T) {
	fact := validClaimedFactWithLegacyTable()
	fact.Rows = maxLegalClaimedFactRows(ContextFabricClaimedFactMaxRows)
	fact.Table = &ContextFabricClaimedFactTable{
		Field: "backlog_size", Shape: ContextFabricFactTableShapeBreakdown,
		Key: []string{"field_00_" + strings.Repeat("k", ContextFabricClaimedFieldMaxLength-len("field_00_"))},
	}
	// No TimeSeriesRows at all.
	if err := fact.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil: a SINGLE table alone at its own pre-existing legal max must not be newly rejected", err)
	}
}

// TestContextFabricAnswerProjectionValidateRejectsMaxLegalDualTableCombination
// pins the SAME bound on the answer-projection surface (validateFacts),
// which carries its own independent Rows/TimeSeriesRows pair
// (ContextFabricProjectedFact) and would otherwise let the identical
// pathological shape through to a consumer even after the canonical-result
// path is fixed.
func TestContextFabricAnswerProjectionValidateRejectsMaxLegalDualTableCombination(t *testing.T) {
	projection := validAnswerProjection()
	projection.KeyFacts[0].Rows = maxLegalClaimedFactRows(ContextFabricClaimedFactMaxRows)
	projection.KeyFacts[0].Table = &ContextFabricClaimedFactTable{
		Field: "status", Shape: ContextFabricFactTableShapeBreakdown,
		Key: []string{"field_00_" + strings.Repeat("k", ContextFabricClaimedFieldMaxLength-len("field_00_"))},
	}
	projection.KeyFacts[0].TimeSeriesRows = maxLegalClaimedFactRows(ContextFabricClaimedFactMaxRows)
	projection.KeyFacts[0].TimeSeriesTable = &ContextFabricClaimedFactTable{
		Field: "status_daily", Shape: ContextFabricFactTableShapeTimeSeries,
		Key: []string{"field_00_" + strings.Repeat("k", ContextFabricClaimedFieldMaxLength-len("field_00_"))},
	}
	if err := projection.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: projected key fact Rows+TimeSeriesRows combined exceed the CHAOS-4785 joint bound")
	}
}
