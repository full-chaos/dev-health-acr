package devhealthfacts

import (
	"fmt"
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

	drop, dropped, reason := disclosedDualTableDrop("flow", contextfabric.FactFlow, legacyRows, timeSeriesRows, 0)
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

	drop, dropped, reason := disclosedDualTableDrop("flow", contextfabric.FactFlow, legacyRows, timeSeriesRows, 3)
	if drop || dropped != 0 || reason != "" {
		t.Fatalf("disclosedDualTableDrop() = (%v, %d, %q), want (false, 0, \"\") for a small real-shaped dual-table fact under the bound (preCapOmitted must not matter when nothing was dropped)", drop, dropped, reason)
	}
}

// TestDisclosedDualTableDropFoldsPreCapOmission pins codex terra xhigh
// round-1 finding P2 (EXECUTED): a caller's own earlier row-cap
// (capFactValueRows, inside e.g. flowDailyTable) can already have removed
// rows from timeSeriesRows BEFORE it is ever passed here -- those rows
// never appear in len(timeSeriesRows), so a caller that reported only
// len(timeSeriesRows) as the drop count would UNDER-report the true
// omission whenever both the row cap and the CHAOS-4785 joint bound fire
// on the same fact (repro: 70 real days capped to 64 by the earlier cap,
// then the 64 retained rows combined with a maximal legacy table trip the
// joint bound -- the true omission is 70, not 64).
func TestDisclosedDualTableDropFoldsPreCapOmission(t *testing.T) {
	legacyRows := maxLegalFactValueRows(contractsv1.ContextFabricClaimedFactMaxRows)
	timeSeriesRows := maxLegalFactValueRows(contractsv1.ContextFabricClaimedFactMaxRows) // the 64 rows that SURVIVED an earlier cap
	const preCapOmitted = 6                                                              // the 6 rows the earlier cap already removed

	drop, dropped, _ := disclosedDualTableDrop("flow", contextfabric.FactFlow, legacyRows, timeSeriesRows, preCapOmitted)
	if !drop {
		t.Fatal("disclosedDualTableDrop() drop = false, want true")
	}
	want := len(timeSeriesRows) + preCapOmitted
	if dropped != want {
		t.Fatalf("disclosedDualTableDrop() droppedRows = %d, want %d (len(timeSeriesRows)=%d retained-then-dropped rows PLUS the %d rows an earlier cap already omitted) -- undercounting here means FactProviderResult.OmittedCount understates what was actually removed from the served answer", dropped, want, len(timeSeriesRows), preCapOmitted)
	}
}

// htmlEscapedFactValueRows mirrors contracts/v1's htmlEscapedClaimedFactRows
// for the devhealthfacts-side domain type: a value dominated by '<', which
// encoding/json escapes 1 raw byte -> 6 encoded bytes.
func htmlEscapedFactValueRows(rowCount, fieldCount, valueRunLength int) []contextfabric.FactValueRow {
	value := strings.Repeat("<", valueRunLength)
	rows := make([]contextfabric.FactValueRow, rowCount)
	for i := range rows {
		fields := make(map[string]contextfabric.FactValue, fieldCount)
		for f := 0; f < fieldCount; f++ {
			fields[fmt.Sprintf("k%d", f)] = contextfabric.FactValue{String: &value}
		}
		rows[i] = contextfabric.FactValueRow{Fields: fields}
	}
	return rows
}

// TestCombinedRowsExceedBytesBoundTrueForHTMLEscapeInflatedCombination pins
// codex terra xhigh round-2 finding P2 (EXECUTED): the producer-side
// pre-check (combinedRowsExceedBytesBound/factValueRowContentBytes) had its
// OWN raw-string-length copy of the exact bug round 1 fixed in
// contracts/v1 -- round 1's fix was not swept to this sibling. A fact whose
// values are dominated by '<'/'>'/'&' (codex's repro: 1,024-'<'
// work_scope_id values, an unbounded LowCardinality(String) ClickHouse
// column) could report "under bound" here while the contracts/v1 write-path
// validator correctly rejected it -- turning a disclosed, gracefully
// degraded answer into a whole-result ErrInvalidResult failure
// (engine.go:1895), exactly the failure mode this producer-side check
// exists to prevent.
func TestCombinedRowsExceedBytesBoundTrueForHTMLEscapeInflatedCombination(t *testing.T) {
	const rows, fields, valueRunLength = 32, 32, 120
	legacyRows := htmlEscapedFactValueRows(rows, fields, valueRunLength)
	timeSeriesRows := htmlEscapedFactValueRows(rows, fields, valueRunLength)

	if !combinedRowsExceedBytesBound(legacyRows, timeSeriesRows) {
		t.Fatal("combinedRowsExceedBytesBound() = false, want true: raw content bytes understate this pair's actual marshaled size past the joint bound -- the producer pre-check must agree with the contracts/v1 write-path validator, or a fact it waves through gets rejected outright downstream instead of gracefully degraded")
	}
}
