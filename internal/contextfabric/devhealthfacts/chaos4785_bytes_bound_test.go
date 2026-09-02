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
