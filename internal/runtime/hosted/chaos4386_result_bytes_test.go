package hosted_test

// CHAOS-4386 RED/GREEN acceptance test: "a synthetic ~300 KB result must be
// reported as over budget by the harness" -- the half of this ticket's own
// acceptance bullet that belongs to the trial harness itself (as opposed to
// the HTTP route, proven separately by
// internal/api/chaos4386_result_bytes_test.go's
// TestChaos4386HTTPSampleRejectsSyntheticOversizedResult). No live corpus,
// no kiac data plane -- pure logic, runs unconditionally under `make
// verify`, same discipline as this package's own
// "pure-logic tests: no live infra" section.

import (
	"strconv"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// chaos4386SyntheticOversizedResultRow mirrors the shape a project rollup's
// Rows table actually carries in production (CHAOS-4363/CHAOS-4347) closely
// enough to be a representative fixture, without depending on
// internal/api's own private test fixtures (a different package; its
// _test.go helpers cannot be imported here).
func chaos4386SyntheticOversizedResultRow(i int) contractsv1.ContextFabricClaimedFactRow {
	teamID := "team_" + strconv.Itoa(i)
	teamName := "Platform Team " + strconv.Itoa(i)
	day := "2026-08-27"
	area := "area_" + strconv.Itoa(i%4)
	delivery := int64(120 + i)
	items := int64(45 + i)
	cycle := 18.5 + float64(i)*0.1
	return contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
		"team_id":              {String: &teamID},
		"team_name":            {String: &teamName},
		"day":                  {String: &day},
		"delivery_units":       {Integer: &delivery},
		"work_items_completed": {Integer: &items},
		"cycle_p50_hours":      {Number: &cycle},
		"investment_area":      {String: &area},
	}}
}

// chaos4386SyntheticOversizedResult builds a ContextFabricInvestigationResult
// with 18 full (ContextFabricClaimedFactMaxRows-row) Rows-bearing
// ClaimedFacts -- calibrated (measured empirically) to land in the ~300 KB
// band this ticket's own acceptance bullet names, comfortably over
// ACR_MAX_SERIALIZED_BYTES (262144). This is the synthetic RED fixture: a
// case whose serialized InvestigationResult this harness must report as
// over budget.
func chaos4386SyntheticOversizedResult() contractsv1.ContextFabricInvestigationResult {
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_chaos4386_synthetic", Label: "CHAOS-4386 synthetic"}
	result := contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_chaos4386_synthetic_300kb", RequestID: "request_chaos4386_synthetic",
		Status:            contractsv1.ContextFabricInvestigationComplete,
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: []contractsv1.ContextFabricSubjectRef{project}},
		Coverage:          contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{}},
	}
	rows := make([]contractsv1.ContextFabricClaimedFactRow, contractsv1.ContextFabricClaimedFactMaxRows)
	for i := range rows {
		rows[i] = chaos4386SyntheticOversizedResultRow(i)
	}
	for i := 0; i < 18; i++ {
		count := int64(contractsv1.ContextFabricClaimedFactMaxRows)
		result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
			ClaimID: "claim_rollup_" + strconv.Itoa(i), Kind: contractsv1.ContextFabricFactInvestment, Subject: project, Field: "team_count",
			Value: contractsv1.ContextFabricScalarValue{Integer: &count}, Rows: rows,
		})
	}
	return result
}

// TestChaos4386SyntheticOversizedResultReportedOverBudget is the RED/GREEN
// acceptance test: chaos4386MeasureResult (the SAME encoder the HTTP route
// uses, api.MarshalContextFabricResponse) must measure the synthetic ~300 KB
// fixture over both chaos4386DefaultMaxSerializedBytes (production
// ACR_MAX_SERIALIZED_BYTES default, 262144) and chaos4386LegacyResponseByteCap
// (the retired 16,000-byte effective cap), and chaos4386ResultByteStats --
// the exact run-level aggregation TestChaos3742TwoTurnConfirmationReplay/
// TestChaos4360NTurnConfirmationCarry call -- must report it via
// OverMaxSerializedBytesCount/OverLegacy16KCount. Before CHAOS-4386, neither
// function existed and this measurement was structurally impossible: the
// harness never serialized a case's own final InvestigationResult at all.
func TestChaos4386SyntheticOversizedResultReportedOverBudget(t *testing.T) {
	result := chaos4386SyntheticOversizedResult()
	resultBytes, estTokens := chaos4386MeasureResult(result)
	t.Logf("synthetic oversized result: %d bytes (~%d KB), ~%d estimated tokens", resultBytes, resultBytes/1000, estTokens)

	if resultBytes < 200_000 || resultBytes > 400_000 {
		t.Fatalf("chaos4386MeasureResult measured %d bytes, want a result in the ~300 KB band this ticket's acceptance bullet names", resultBytes)
	}
	if resultBytes <= chaos4386DefaultMaxSerializedBytes {
		t.Fatalf("chaos4386MeasureResult measured %d bytes, want > the production ACR_MAX_SERIALIZED_BYTES default (%d) -- fixture does not reproduce an over-budget shape", resultBytes, chaos4386DefaultMaxSerializedBytes)
	}
	wantEstTokens := (resultBytes + 3) / 4
	if estTokens != wantEstTokens {
		t.Fatalf("estTokens = %d, want %d (route's own (bytes+3)/4 estimate)", estTokens, wantEstTokens)
	}

	_, _, _, overConfigured, overLegacy16K := chaos4386ResultByteStats([]int64{resultBytes}, chaos4386DefaultMaxSerializedBytes)
	if overConfigured != 1 {
		t.Fatalf("OverMaxSerializedBytesCount = %d, want 1 -- the harness must report a %d-byte result as over the %d-byte ACR_MAX_SERIALIZED_BYTES budget", overConfigured, resultBytes, chaos4386DefaultMaxSerializedBytes)
	}
	if overLegacy16K != 1 {
		t.Fatalf("OverLegacy16KCount = %d, want 1 -- a %d-byte result is also over the retired %d-byte legacy cap", overLegacy16K, resultBytes, chaos4386LegacyResponseByteCap)
	}
}

// TestChaos4386ResultByteStatsGreenBaseline is the GREEN control:
// chaos4386ResultByteStats must NOT flag an ordinary, well-under-budget
// result set as over budget, and must compute max/p50/p99 correctly against
// a known literal distribution -- proving TestChaos4386SyntheticOversizedResultReportedOverBudget
// fails because of the fixture's real size, not because the stats function
// itself is miscalibrated to always report "over budget".
func TestChaos4386ResultByteStatsGreenBaseline(t *testing.T) {
	values := []int64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}
	max, p50, p99, overConfigured, overLegacy16K := chaos4386ResultByteStats(values, 262144)
	if max != 1000 {
		t.Errorf("max = %d, want 1000", max)
	}
	if p50 != 600 {
		t.Errorf("p50 = %d, want 600 (rank len/2=5, zero-indexed sorted value)", p50)
	}
	if p99 != 1000 {
		t.Errorf("p99 = %d, want 1000 (rank 9 of 10, the last value)", p99)
	}
	if overConfigured != 0 {
		t.Errorf("overConfigured = %d, want 0 -- nothing here exceeds ACR_MAX_SERIALIZED_BYTES", overConfigured)
	}
	if overLegacy16K != 0 {
		t.Errorf("overLegacy16K = %d, want 0 -- nothing here exceeds the legacy 16,000-byte cap either", overLegacy16K)
	}

	// Zero/negative entries (OfferMiss/ArmInvalid rows) must be excluded
	// entirely, never counted as an under-budget zero.
	maxZ, p50Z, p99Z, overZ, legacyZ := chaos4386ResultByteStats([]int64{0, -1}, 262144)
	if maxZ != 0 || p50Z != 0 || p99Z != 0 || overZ != 0 || legacyZ != 0 {
		t.Errorf("chaos4386ResultByteStats([]int64{0, -1}, ...) = (%d,%d,%d,%d,%d), want all zero -- absent results must not enter the distribution", maxZ, p50Z, p99Z, overZ, legacyZ)
	}
}
