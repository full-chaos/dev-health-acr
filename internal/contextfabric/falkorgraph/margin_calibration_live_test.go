package falkorgraph

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// runMarginCalibrationRunner is TestCalibrateVectorMarginFromReportFile's
// core logic, taking an ALREADY-PARSED report/options rather than reading
// them from the environment or disk -- mirrors runCalibrationRunner's shape
// exactly (tau_calibration_live_test.go), including reuse of the SAME
// runnerT interface so tau_calibration_test.go's fakeRunnerT double covers
// this runner too without any new test-double machinery.
//
// Unlike runCalibrationRunner, this runner does NOT fail the test (t.Fatalf)
// when ApplyReady is false: CHAOS-3829's ratified sequencing explicitly asks
// for phase 1+2 results to be REPORTED to team-lead for ratification, not
// silently gated on a mandatory pass here -- an insufficient-data verdict IS
// the deliverable phase 2 is supposed to surface (e.g. "M UNMEASURED, needs
// a broader corpus"), not a reason to hide the run behind a failing exit
// code the way a NOT-apply-ready RETRIEVAL policy is (that tool's fail-closed
// posture protects an operator from silently shipping tau/K; this one is a
// measurement report a human reads before ANY commit-path code exists to
// ship at all).
// reportPath is the SOURCE report's own path on disk (empty when the
// caller built report directly in memory, e.g. every non-live test in this
// package) -- codex r3 H3: threaded through solely so
// NewMarginCalibrationArtifact can stamp the written artifact with full
// provenance (target tau/topK, source report path + content hash),
// mirroring CalibrationArtifact's identical shape and rationale.
func runMarginCalibrationRunner(t runnerT, report CalibrationReport, opts MarginCalibrationOptions, reportPath, outputPath string) {
	t.Helper()

	result, err := CalibrateMarginFromReport(report, opts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}

	t.Logf("CHAOS-3829 Phase 2 margin calibration (target identity=%s dim=%d tau=%v topK=%d):", opts.TargetEmbedIdentity, opts.TargetDimension, opts.TargetTau, opts.TargetTopK)
	t.Logf("  eligible population: correct-top1=%d wrong-top1=%d (of which %d from a corroborated control)",
		result.CorrectSampleSize, result.WrongSampleSize, result.WrongSampleSizeFromControls)
	t.Logf("  controls: in_report=%d with_vector_arm_data=%d corroborated=%d corroborated_without_margin=%d",
		result.ControlsInReport, result.ControlsWithVectorArmData, result.ControlsCorroborated, result.ControlsCorroboratedWithoutMargin)
	if result.ThresholdM != nil {
		t.Logf("  ThresholdM(M)=%s  WrongMarginMax=%s  AchievedReach=%.4f",
			strconv.FormatFloat(*result.ThresholdM, 'g', -1, 64),
			strconv.FormatFloat(*result.WrongMarginMax, 'g', -1, 64),
			result.AchievedReach)
	} else {
		t.Logf("  ThresholdM: UNMEASURED (no wrong-top1 case in the eligible population)")
	}
	if result.ApplyReady {
		t.Logf("  VERDICT: SAFETY-MEASURED -- %s", result.Note)
	} else {
		t.Logf("  VERDICT: NOT SAFETY-MEASURED -- %s", result.Note)
	}

	if outputPath != "" {
		artifact := NewMarginCalibrationArtifact(result, report, opts, reportPath)
		encoded, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			t.Fatalf("encode margin calibration artifact: %v", err)
		}
		if err := writeFileMode0600(outputPath, encoded); err != nil {
			t.Fatalf("write margin calibration artifact to %s: %v", outputPath, err)
		}
		t.Logf("margin calibration result written to %s", outputPath)
	}
}

// TestCalibrateVectorMarginFromReportFile is CHAOS-3829 Phase 2's
// calibration TOOLING runner: it reads an EXTENDED oracle report's (CHAOS-3829
// Phase 1 fields populated) JSON path from the environment and prints the
// recommended VectorMarginCommitThreshold CalibrateMarginFromReport computes
// from it. Its core decision/artifact logic lives in
// runMarginCalibrationRunner above.
//
// Env-gated exactly like this package's other harnesses:
//
//	ACR_TEST_MARGIN_CALIBRATION_REPORT=/path/to/oracle-report.json \
//	ACR_TEST_MARGIN_CALIBRATION_TARGET_IDENTITY='provider/model#tag' \
//	ACR_TEST_MARGIN_CALIBRATION_TARGET_DIMENSION=3072 \
//	ACR_TEST_MARGIN_CALIBRATION_TARGET_TAU=0.30 \
//	ACR_TEST_MARGIN_CALIBRATION_TARGET_TOPK=20 \
//	[ACR_TEST_MARGIN_CALIBRATION_OUTPUT=/path/to/margin.json] \
//	  go test ./internal/contextfabric/falkorgraph -run CalibrateVectorMarginFromReportFile -v
//
// TARGET_TAU/TARGET_TOPK (codex r1 F7) state the similarity floor/ANN
// result-set size the operator intends this M to gate under; the report
// must have been measured at EXACTLY those values or CalibrateMarginFromReport
// refuses (ErrMarginReportConfigMismatch) -- same discipline as
// TARGET_IDENTITY/TARGET_DIMENSION.
//
// The report is expected to be exactly what TestExactSearchOracleDecomposesRetrievalMisses
// (oracle_live_test.go, CHAOS-3829 Phase 1 extension) writes.
func TestCalibrateVectorMarginFromReportFile(t *testing.T) {
	reportPath := os.Getenv("ACR_TEST_MARGIN_CALIBRATION_REPORT")
	if reportPath == "" {
		t.Skip("ACR_TEST_MARGIN_CALIBRATION_REPORT is not set; this harness calibrates from a real oracle report, not synthetic data (see margin_calibration_test.go for the synthetic-fixture tests)")
	}
	targetEmbedIdentity := os.Getenv("ACR_TEST_MARGIN_CALIBRATION_TARGET_IDENTITY")
	if targetEmbedIdentity == "" {
		t.Fatal("ACR_TEST_MARGIN_CALIBRATION_TARGET_IDENTITY is not set; state the embed retrieval identity you intend to apply this recommendation to -- this tool refuses to calibrate for an unstated target")
	}
	targetDimension, err := strconv.Atoi(os.Getenv("ACR_TEST_MARGIN_CALIBRATION_TARGET_DIMENSION"))
	if err != nil || targetDimension <= 0 {
		t.Fatalf("ACR_TEST_MARGIN_CALIBRATION_TARGET_DIMENSION must be a positive integer: %v", err)
	}
	targetTau, err := strconv.ParseFloat(os.Getenv("ACR_TEST_MARGIN_CALIBRATION_TARGET_TAU"), 64)
	if err != nil || targetTau <= 0 {
		t.Fatalf("ACR_TEST_MARGIN_CALIBRATION_TARGET_TAU must be a positive number: %v", err)
	}
	targetTopK, err := strconv.Atoi(os.Getenv("ACR_TEST_MARGIN_CALIBRATION_TARGET_TOPK"))
	if err != nil || targetTopK <= 0 {
		t.Fatalf("ACR_TEST_MARGIN_CALIBRATION_TARGET_TOPK must be a positive integer: %v", err)
	}

	encoded, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read oracle report %s: %v", reportPath, err)
	}
	var report CalibrationReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatalf("decode oracle report %s: %v", reportPath, err)
	}

	runMarginCalibrationRunner(t, report, MarginCalibrationOptions{
		TargetEmbedIdentity: targetEmbedIdentity,
		TargetDimension:     targetDimension,
		TargetTau:           targetTau,
		TargetTopK:          targetTopK,
	}, reportPath, os.Getenv("ACR_TEST_MARGIN_CALIBRATION_OUTPUT"))
}
