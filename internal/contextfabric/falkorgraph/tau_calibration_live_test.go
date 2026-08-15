package falkorgraph

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// TestCalibrateRetrievalPolicyFromReportFile is CHAOS-3834's calibration
// TOOLING runner (embed-text spec §5 L4 / §6 T4): it reads an oracle
// report's JSON path from the environment and prints the recommended
// per-identity RetrievalPolicy CalibrateFromReport computes from it.
//
// Env-gated exactly like this package's other harnesses (oracle_live_test.go,
// hnsw_sweep_test.go): skips when its one required variable is unset, so a
// normal `go test ./...` run never depends on a real oracle artifact
// existing on disk. NOT hardcoded to any real data -- the report path is
// entirely the caller's choice; this lane's own tests exercise
// CalibrateFromReport directly against synthetic fixtures (see
// tau_calibration_test.go), never against a checked-in "real" report.
//
//	ACR_TEST_CALIBRATION_REPORT=/path/to/oracle-report.json \
//	[ACR_TEST_CALIBRATION_TARGET_RECALL=0.90] \
//	[ACR_TEST_CALIBRATION_OUTPUT=/path/to/policy.json] \
//	  go test ./internal/contextfabric/falkorgraph -run CalibrateRetrievalPolicyFromReportFile -v
//
// The report is expected to be exactly what TestExactSearchOracleDecomposesRetrievalMisses
// (oracle_live_test.go) writes -- CalibrationReport/CalibrationCase/
// CalibrationHardNegative mirror oracleReport/oracleCaseResult/hardNegative's
// JSON shape field-for-field (see tau_calibration.go's package doc comment
// for why the coupling is JSON, not a shared Go type).
func TestCalibrateRetrievalPolicyFromReportFile(t *testing.T) {
	reportPath := os.Getenv("ACR_TEST_CALIBRATION_REPORT")
	if reportPath == "" {
		t.Skip("ACR_TEST_CALIBRATION_REPORT is not set; this harness calibrates from a real oracle report, not synthetic data (see tau_calibration_test.go for the synthetic-fixture tests)")
	}

	targetRecall := 0.90
	if raw := os.Getenv("ACR_TEST_CALIBRATION_TARGET_RECALL"); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("ACR_TEST_CALIBRATION_TARGET_RECALL must be a number: %v", err)
		}
		targetRecall = parsed
	}

	encoded, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read oracle report %s: %v", reportPath, err)
	}
	var report CalibrationReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatalf("decode oracle report %s: %v", reportPath, err)
	}

	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: targetRecall})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}

	t.Logf("CHAOS-3834 calibration recommendation from %s (target recall=%.2f):", reportPath, targetRecall)
	t.Logf("  SimilarityFloor(tau)=%.4f  OverFetchMultiplier(K)=%d  (EfRuntime is NOT set by this tool -- see CHAOS-3832's efRuntime sweep)",
		result.Policy.SimilarityFloor, result.Policy.OverFetchMultiplier)
	t.Logf("  S+ samples=%d  S- samples=%d  hard-negative samples=%d",
		result.SPlusSampleSize, result.SMinusSampleSize, result.HardNegativeSampleSize)
	t.Logf("  achieved recall=%.4f  hard-negative reject rate=%.4f  near-duplicate p90=%d",
		result.AchievedRecall, result.HardNegativeRejectRate, result.NearDuplicateP90)

	if outputPath := os.Getenv("ACR_TEST_CALIBRATION_OUTPUT"); outputPath != "" {
		encodedResult, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			t.Fatalf("encode calibration result: %v", err)
		}
		if err := writeFileMode0600(outputPath, encodedResult); err != nil {
			t.Fatalf("write calibration result to %s: %v", outputPath, err)
		}
		t.Logf("calibration result written to %s", outputPath)
	}
}
