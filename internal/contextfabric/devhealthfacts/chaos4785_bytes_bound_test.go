package devhealthfacts

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// chaos4785_bytes_bound_test.go pins the devhealthfacts-side half of
// CHAOS-4785 (producer-side pre-check, defense in depth ahead of the
// internal/contracts/v1 write-path bound): combinedRowsExceedBytesBound
// must agree with the SAME arithmetic the contract validator enforces, and
// must fire ONLY once Rows+TimeSeriesRows are combined, never for either
// table alone at its own legal maximum. Host-only -- no ClickHouse, no
// testcontainers.

func maxLegalFactValueRows(rowCount int) []contextfabric.FactValueRow {
	longValue := strings.Repeat("v", contractsv1.ContextFabricClaimedFactValueMaxLength)
	rows := make([]contextfabric.FactValueRow, rowCount)
	for i := range rows {
		fields := make(map[string]contextfabric.FactValue, contractsv1.ContextFabricClaimedFactRowMaxFields)
		for f := 0; f < contractsv1.ContextFabricClaimedFactRowMaxFields; f++ {
			key := "field_" + strings.Repeat("k", 20) + strings.Repeat("0", f+1)
			value := longValue
			fields[key] = contextfabric.FactValue{String: &value}
		}
		rows[i] = contextfabric.FactValueRow{Fields: fields}
	}
	return rows
}

func TestCombinedRowsExceedBytesBoundFalseForEitherTableAloneAtItsOwnMax(t *testing.T) {
	maxRows := maxLegalFactValueRows(contractsv1.ContextFabricClaimedFactMaxRows)
	if combinedRowsExceedBytesBound(maxRows, nil) {
		t.Fatal("combinedRowsExceedBytesBound(maxRows, nil) = true, want false: one max-legal table alone must stay under the joint bound")
	}
	if combinedRowsExceedBytesBound(nil, maxRows) {
		t.Fatal("combinedRowsExceedBytesBound(nil, maxRows) = true, want false: one max-legal table alone must stay under the joint bound")
	}
}

func TestCombinedRowsExceedBytesBoundTrueForBothTablesAtMax(t *testing.T) {
	legacyRows := maxLegalFactValueRows(contractsv1.ContextFabricClaimedFactMaxRows)
	timeSeriesRows := maxLegalFactValueRows(contractsv1.ContextFabricClaimedFactMaxRows)
	if !combinedRowsExceedBytesBound(legacyRows, timeSeriesRows) {
		t.Fatal("combinedRowsExceedBytesBound(legacyRows, timeSeriesRows) = false, want true: both tables at their own legal max combined must exceed the joint bound")
	}
}

func TestCombinedRowsExceedBytesBoundFalseForRealShapedDualTable(t *testing.T) {
	// The actual shape today's producers emit: a handful of narrow rows on
	// each side, nowhere near either table's own per-table maximum -- see
	// CHAOS-4785's Phase 1 measurement (largest real observed combined
	// fact: 16,246 bytes against a 262,144-byte bound).
	day := "2026-08-30"
	itemsStarted := int64(4)
	legacyRows := []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{
		"team_id": {String: &day}, "items_started": {Integer: &itemsStarted},
	}}}
	timeSeriesRows := []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{
		"day": {String: &day}, "items_started": {Integer: &itemsStarted},
	}}}
	if combinedRowsExceedBytesBound(legacyRows, timeSeriesRows) {
		t.Fatal("combinedRowsExceedBytesBound() = true, want false: a small real-shaped dual-table fact must stay under the joint bound")
	}
}

// TestDisclosedDualTableDropNeverSilent pins chris's disclosure ruling: a
// dropped additive time series must be reported as unavailable-class
// coverage, never silently dropped -- a log line alone is not disclosure.
// disclosedDualTableDrop is the one seam every producer call site routes
// through, so this is the single test that keeps that promise true for all
// of them: drop=true must ALWAYS carry a non-zero droppedRows count (which
// the caller folds into FactProviderResult.Truncated/OmittedCount) and the
// SAME closed-vocabulary reason recordFactBytesBoundExceeded's telemetry
// event uses.
func TestDisclosedDualTableDropNeverSilent(t *testing.T) {
	legacyRows := maxLegalFactValueRows(contractsv1.ContextFabricClaimedFactMaxRows)
	timeSeriesRows := maxLegalFactValueRows(contractsv1.ContextFabricClaimedFactMaxRows)

	drop, dropped, reason := disclosedDualTableDrop("flow", contextfabric.FactFlow, legacyRows, timeSeriesRows)
	if !drop {
		t.Fatal("disclosedDualTableDrop() drop = false, want true: both tables at their own legal max combined must exceed the joint bound")
	}
	if dropped != len(timeSeriesRows) {
		t.Fatalf("disclosedDualTableDrop() droppedRows = %d, want %d (the WHOLE dropped series, not the row-cap overflow): a caller must fold this into its own omitted-rows accounting so FactProviderResult.Truncated/OmittedCount discloses the drop -- an undisclosed drop is exactly what chris's ruling forbids", dropped, len(timeSeriesRows))
	}
	if reason != factBytesBoundExceededReason {
		t.Fatalf("disclosedDualTableDrop() reason = %q, want %q: the served fact's reason field must match the telemetry token so a reader can tell this cause apart from an ordinary row-cap truncation", reason, factBytesBoundExceededReason)
	}
}

// TestDisclosedDualTableDropFalseUnderTheBound proves the disclosure
// contract does not fire when nothing was dropped -- an ordinary
// dual-table fact must keep behaving exactly as before this ticket.
func TestDisclosedDualTableDropFalseUnderTheBound(t *testing.T) {
	day := "2026-08-30"
	legacyRows := []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{"team_id": {String: &day}}}}
	timeSeriesRows := []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{"day": {String: &day}}}}

	drop, dropped, reason := disclosedDualTableDrop("flow", contextfabric.FactFlow, legacyRows, timeSeriesRows)
	if drop || dropped != 0 || reason != "" {
		t.Fatalf("disclosedDualTableDrop() = (%v, %d, %q), want (false, 0, \"\") for a small real-shaped dual-table fact under the bound", drop, dropped, reason)
	}
}
