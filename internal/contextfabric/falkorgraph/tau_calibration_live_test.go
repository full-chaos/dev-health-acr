package falkorgraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// runnerT is the minimal *testing.T surface runCalibrationRunner needs.
// *testing.T satisfies it with zero adaptation, so
// TestCalibrateRetrievalPolicyFromReportFile is unaffected -- it exists so
// a test can substitute a FAKE implementation instead (see fakeRunnerT in
// tau_calibration_test.go) to OBSERVE whether Fatalf was called without
// that failure propagating to fail the OUTER test itself. A real subtest's
// failure (t.Run + real *testing.T) always marks its parent test failed too
// in Go's reporting, which would make a test asserting "this call correctly
// fails" itself report red -- codex round-4 FIX C follow-up, self-caught
// while verifying the fix's own pinning test actually verifies anything.
type runnerT interface {
	Helper()
	Logf(format string, args ...interface{})
	Fatalf(format string, args ...interface{})
}

// resolvePathForIdentity resolves path to an absolute, symlink-free form for
// IDENTITY comparison (codex round-7 P2 -- "resolve both paths ... before
// any removal"). A path that does not exist yet (the common case for
// outputPath on a first run) cannot be evaluated by filepath.EvalSymlinks,
// which errors on a missing file -- that is not a resolution failure here,
// it just means the path has no symlink to resolve, so the absolute form
// alone is the correct identity to compare against. Only a genuine
// filepath.Abs failure (a malformed path) is reported as an error.
func resolvePathForIdentity(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// runCalibrationRunner is TestCalibrateRetrievalPolicyFromReportFile's core
// logic, taking an ALREADY-PARSED report/options/output-path rather than
// reading them from the environment or disk -- extracted (codex round-4
// FIX C) so the FAIL-and-write-no-artifact behavior on a gate failure is
// unit-testable (see TestCalibrationRunner_GateFailingReportFailsAndWritesNoArtifact
// and its sibling in tau_calibration_test.go) without a real
// ACR_TEST_CALIBRATION_REPORT file on disk. reportPath is the SOURCE
// report's own path on disk (empty when the caller built `report` directly
// in memory, e.g. every non-live test in this package) -- carried through
// solely so the same-path collision check below has something to compare
// outputPath against; it is never read again here (the caller already
// parsed `report` from it).
//
// reportBytes is CHAOS-3852's port of codex r8 O3's fix (see
// NewCalibrationArtifact's doc comment): the RAW bytes the caller actually
// read from reportPath (nil when report was built in memory, e.g. every
// non-live test), passed straight through so the written artifact's
// SourceReportSHA256 hashes the SAME bytes a caller could independently
// verify with `sha256sum reportPath`.
func runCalibrationRunner(t runnerT, report CalibrationReport, opts CalibrationOptions, reportPath string, reportBytes []byte, outputPath string) {
	t.Helper()
	if outputPath != "" {
		// codex round-7 P2: input==output would DELETE THE SOURCE REPORT.
		// Checked and Fatalf'd BEFORE any removal -- round-6's stale-artifact
		// removal (below) would otherwise delete reportPath itself when both
		// env vars point at the same file (a plausible operator mistake, or
		// a deliberate-but-wrong "write the policy back over the report"
		// intent): a gate-failing run would leave NOTHING at all (the
		// report gone, no replacement written), and a gate-passing run
		// would silently REPLACE the raw measurement data with the policy
		// JSON, destroying the report either way. Resolved to absolute,
		// symlink-free form (not a raw string compare) so two different
		// spellings of the SAME file (a relative vs. absolute path, or a
		// symlink) are still caught.
		if reportPath != "" {
			resolvedReport, err := resolvePathForIdentity(reportPath)
			if err != nil {
				t.Fatalf("resolve report path %s for the input/output collision check: %v", reportPath, err)
			}
			resolvedOutput, err := resolvePathForIdentity(outputPath)
			if err != nil {
				t.Fatalf("resolve output path %s for the input/output collision check: %v", outputPath, err)
			}
			if resolvedReport == resolvedOutput {
				t.Fatalf("ACR_TEST_CALIBRATION_REPORT and the output path both resolve to %s -- refusing to run: writing the calibration result there would destroy the source report this run just read (a gate failure would delete it with nothing to replace it; a gate pass would silently overwrite it with the policy JSON). Use a different output path.", resolvedReport)
			}
		}
		// codex round-6 P2: remove any EXISTING artifact at outputPath
		// (now proven distinct from reportPath) FIRST, before any
		// measurement runs, so every exit path (pass, gate-fail, a crash
		// mid-run below) leaves either a fresh artifact or NO artifact --
		// never a STALE one. Without this, a prior successful run's file at
		// this same path survives untouched through a LATER gate-failing
		// run, and a downstream consumer reading outputPath sees that stale
		// artifact as if it were the current (failed) run's output. A
		// missing file is not an error (the common case, nothing to clean
		// up); any OTHER removal failure IS one -- proceeding without being
		// sure the stale file is gone would silently reopen that hazard.
		if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove stale artifact at %s before running: %v", outputPath, err)
		}
	}

	result, err := CalibrateFromReport(report, opts)
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}

	// codex round-5 FIX C: tau is printed ROUND-TRIPPABLE (strconv.FormatFloat
	// with 'g'/-1 precision -- the shortest decimal string that parses back
	// to the EXACT same float64 bits), not %.4f. The round-3/round-2
	// "re-run at the recommended tau" workflow requires a FOLLOW-UP report's
	// Tau field to EXACTLY equal this run's recommendation
	// (recallGateThreshold's Nextafter output, e.g. 0.29999999999999993,
	// never a clean decimal) before CalibrateFromReport trusts a total --
	// %.4f prints "0.3000", which an operator could paste verbatim into the
	// next report and never see it match, silently landing back in the
	// "insufficient data" branch with no obvious cause.
	tauExact := strconv.FormatFloat(result.Policy.SimilarityFloor, 'g', -1, 64)
	t.Logf("CHAOS-3834 calibration recommendation (target recall=%.2f, target identity=%s dim=%d):",
		opts.TargetRecall, opts.TargetEmbedIdentity, opts.TargetDimension)
	t.Logf("  SimilarityFloor(tau)=%s  OverFetchMultiplier(K)=%d  (EfRuntime is NOT set by this tool -- see CHAOS-3832's efRuntime sweep)",
		tauExact, result.Policy.OverFetchMultiplier)
	t.Logf("  For an EXACT re-run at this tau (required to trust a hard-negative total on the next pass): set the next report's \"tau\" field to precisely %s", tauExact)
	t.Logf("  S+ samples=%d  S- samples=%d  hard-negative samples=%d",
		result.SPlusSampleSize, result.SMinusSampleSize, result.HardNegativeSampleSize)
	t.Logf("  achieved recall=%.4f  hard-negative reject rate=%.4f  near-duplicate p90=%d",
		result.AchievedRecall, result.HardNegativeRejectRate, result.NearDuplicateP90)
	// codex round-2 P1: this tool is FAIL-CLOSED on the negative gate --
	// ApplyReady=false means the recall-gate tau above is a diagnostic, not a
	// ready-to-apply policy. Surfaced loudly here because this test's log
	// output IS the human-facing artifact operators read before writing a
	// retrieval_policy.go table entry.
	if result.ApplyReady {
		t.Logf("  NEGATIVE GATE: PASSED -- %s", result.NegativeGateNote)
	} else {
		t.Logf("  NEGATIVE GATE: FAILED -- NOT APPLY-READY -- %s", result.NegativeGateNote)
	}
	// codex round-2 P2: a SEPARATE fail-closed gate on K specifically -- see
	// KApplyReady's doc comment. Surfaced the same way as the negative gate,
	// for the same reason (this log output is what an operator reads).
	if result.KApplyReady {
		t.Logf("  K SIZING: RELIABLE -- %s", result.KInsufficientDataNote)
	} else {
		t.Logf("  K SIZING: INSUFFICIENT DATA -- OverFetchMultiplier forced to 0 -- %s", result.KInsufficientDataNote)
	}

	// codex round-4 FIX C: this runner used to log a gate failure and then
	// keep going -- write the artifact, exit 0 -- exactly the
	// go-test-skip-reads-as-ok class (a human or automation watching only
	// the process exit code sees green for a report that is NOT apply-ready
	// in either dimension). When EITHER readiness gate fails, this test
	// FAILS (t.Fatal, non-zero exit) and does NOT write to outputPath at
	// all -- not even a "clearly marked" diagnostic file at that path.
	// Decision recorded here: the risk of a downstream consumer treating
	// file-presence-at-the-expected-path as "policy ready" (regardless of a
	// marker inside the file) outweighs the convenience of a diagnostic
	// artifact: the t.Logf lines above and the t.Fatal message below are
	// the diagnostic surface instead.
	if !result.ApplyReady || !result.KApplyReady {
		t.Fatalf(
			"calibration NOT apply-ready (ApplyReady=%t, KApplyReady=%t) -- no artifact written. "+
				"See the NEGATIVE GATE / K SIZING log lines above for why. This is a FAILING run, not a "+
				"diagnostic-only one: a caller that only checks this test's exit status must see red here, "+
				"never green for a report this tool itself would not bless",
			result.ApplyReady, result.KApplyReady,
		)
	}

	if outputPath != "" {
		// codex round-7 P2 FIX D: the written artifact is a
		// CalibrationArtifact, not a bare CalibrationResult -- see its doc
		// comment. Without this, a file swapped between two embedding
		// spaces (or two TargetRecall values) is silently indistinguishable
		// from the correct one to whatever reads outputPath back later.
		artifact := NewCalibrationArtifact(result, report, opts, reportPath, reportBytes)
		encodedResult, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			t.Fatalf("encode calibration artifact: %v", err)
		}
		if err := writeFileMode0600(outputPath, encodedResult); err != nil {
			t.Fatalf("write calibration artifact to %s: %v", outputPath, err)
		}
		t.Logf("calibration result written to %s", outputPath)
	}
}

// TestCalibrateRetrievalPolicyFromReportFile is CHAOS-3834's calibration
// TOOLING runner (embed-text spec §5 L4 / §6 T4): it reads an oracle
// report's JSON path from the environment and prints the recommended
// per-identity RetrievalPolicy CalibrateFromReport computes from it. Its
// core decision/artifact logic lives in runCalibrationRunner above.
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
//	ACR_TEST_CALIBRATION_TARGET_IDENTITY='provider/model#tag' \
//	ACR_TEST_CALIBRATION_TARGET_DIMENSION=3072 \
//	[ACR_TEST_CALIBRATION_TARGET_RECALL=0.90] \
//	[ACR_TEST_CALIBRATION_OUTPUT=/path/to/policy.json] \
//	  go test ./internal/contextfabric/falkorgraph -run CalibrateRetrievalPolicyFromReportFile -v
//
// The report is expected to be exactly what TestExactSearchOracleDecomposesRetrievalMisses
// (oracle_live_test.go) writes -- CalibrationReport/CalibrationCase/
// CalibrationHardNegative mirror oracleReport/oracleCaseResult/hardNegative's
// JSON shape field-for-field (see tau_calibration.go's package doc comment
// for why the coupling is JSON, not a shared Go type).
//
// ACR_TEST_CALIBRATION_TARGET_IDENTITY/TARGET_DIMENSION (codex round-4 FIX A)
// are REQUIRED, not optional: the operator states which embed retrieval
// identity/dimension they intend to apply this recommendation to, and
// CalibrateFromReport refuses (ErrEmbeddingIdentityMismatch) if the report
// was measured against something else -- e.g. ACR_TEST_CALIBRATION_REPORT
// accidentally pointed at a stale or wrong-identity artifact. This makes the
// check meaningful here specifically (not a tautology against the report's
// own stamp): a human, not the report file, states the intent.
func TestCalibrateRetrievalPolicyFromReportFile(t *testing.T) {
	reportPath := os.Getenv("ACR_TEST_CALIBRATION_REPORT")
	if reportPath == "" {
		t.Skip("ACR_TEST_CALIBRATION_REPORT is not set; this harness calibrates from a real oracle report, not synthetic data (see tau_calibration_test.go for the synthetic-fixture tests)")
	}

	targetEmbedIdentity := os.Getenv("ACR_TEST_CALIBRATION_TARGET_IDENTITY")
	if targetEmbedIdentity == "" {
		t.Fatal("ACR_TEST_CALIBRATION_TARGET_IDENTITY is not set; state the embed retrieval identity you intend to apply this recommendation to (codex round-4 FIX A) -- this tool refuses to calibrate for an unstated target")
	}
	targetDimension, err := strconv.Atoi(os.Getenv("ACR_TEST_CALIBRATION_TARGET_DIMENSION"))
	if err != nil || targetDimension <= 0 {
		t.Fatalf("ACR_TEST_CALIBRATION_TARGET_DIMENSION must be a positive integer: %v", err)
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

	runCalibrationRunner(t, report, CalibrationOptions{
		TargetRecall:        targetRecall,
		TargetEmbedIdentity: targetEmbedIdentity,
		TargetDimension:     targetDimension,
	}, reportPath, encoded, os.Getenv("ACR_TEST_CALIBRATION_OUTPUT"))
}
