package falkorgraph

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

// testMarginTau/testMarginTopK are the shared target tau/topK every
// synthetic fixture in this file uses (codex r1 F7), mirroring
// testEmbedIdentity/testDimension's convention.
const (
	testMarginTau  = 0.30
	testMarginTopK = 20
)

// marginTestOpts is the shared target identity/dimension/tau/topK every
// synthetic fixture in this file uses -- mirrors tau_calibration_test.go's
// testEmbedIdentity/testDimension convention (these tests exercise the
// margin MATH, not the identity/tau/topK gate, which has its own dedicated
// tests below).
var marginTestOpts = MarginCalibrationOptions{
	TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension,
	TargetTau: testMarginTau, TargetTopK: testMarginTopK,
}

// eligibleCase builds one CalibrationCase already inside CalibrateMarginFromReport's
// eligible population (corroborated top-1, measurable margin --
// vectorSearchComplete is NOT part of eligibility as of Phase 2(c)) --
// top1Kind/top1ID is the vector arm's top-1 pick, expectKind/expectID is the
// case's OWN expected subject (equal for a CORRECT case, different for a
// WRONG one), margin is VectorMargin.
func eligibleCase(top1Kind, top1ID, expectKind, expectID string, margin float64) CalibrationCase {
	top2Similarity := 0.5 // arbitrary; only top1's identity/margin matter to the math
	top1Similarity := top2Similarity + margin
	return CalibrationCase{
		ExpectKind:            expectKind,
		ExpectID:              expectID,
		VectorSearchTruncated: boolPtr(false),
		VectorTop1:            &CalibrationVectorArmSubject{Kind: top1Kind, CanonicalID: top1ID, Similarity: top1Similarity, Corroborated: true},
		VectorTop2:            &CalibrationVectorArmSubject{Kind: "project", CanonicalID: "top2", Similarity: top2Similarity},
		// codex r3 H2: derived from the SAME subtraction checkMarginConsistency
		// requires (top1.Similarity - top2.Similarity), not the raw `margin`
		// argument directly -- top1Similarity itself was computed as
		// top2Similarity+margin above, and floating-point addition/
		// subtraction is not exactly invertible ((a+b)-a != b bit-for-bit
		// in general), so passing `margin` here would fail the new
		// consistency check on a fixture that is otherwise perfectly valid.
		VectorMargin: floatPtr(top1Similarity - top2Similarity),
	}
}

func marginReport(cases ...CalibrationCase) CalibrationReport {
	return CalibrationReport{
		EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		Tau: testMarginTau, TopK: testMarginTopK, Cases: cases,
		// codex r7 M3: Scored must equal len(cases) -- exact symmetry with
		// marginReportWithControls' own Controls fix (codex r6 L2), so
		// every fixture built through this helper stays a VALID
		// (non-truncated) report by construction.
		Scored: len(cases),
	}
}

// eligibleControlCase builds one no-match CONTROL CalibrationCase (Phase
// 2(c)) already inside the eligible population -- ExpectKind/ExpectID are
// deliberately left empty (a control has no correct subject), and
// corroborated defaults true unless the caller flips it via the returned
// value.
func eligibleControlCase(top1Kind, top1ID string, margin float64) CalibrationCase {
	top2Similarity := 0.5
	top1Similarity := top2Similarity + margin
	return CalibrationCase{
		VectorSearchTruncated: boolPtr(false),
		VectorTop1:            &CalibrationVectorArmSubject{Kind: top1Kind, CanonicalID: top1ID, Similarity: top1Similarity, Corroborated: true},
		VectorTop2:            &CalibrationVectorArmSubject{Kind: "project", CanonicalID: "control-top2", Similarity: top2Similarity},
		// codex r3 H2: see eligibleCase's identical comment.
		VectorMargin: floatPtr(top1Similarity - top2Similarity),
	}
}

func marginReportWithControls(cases []CalibrationCase, controls []CalibrationCase) CalibrationReport {
	return CalibrationReport{
		EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		Tau: testMarginTau, TopK: testMarginTopK, Cases: cases, ControlCases: controls,
		// codex r6 L2 / r7 M3: Controls/Scored must equal
		// len(controls)/len(cases) -- mirrors the harness's own
		// honest-write invariant (both incremented together,
		// oracle_live_test.go), so every fixture built through this helper
		// stays a VALID (non-truncated) report by construction.
		Scored:   len(cases),
		Controls: len(controls),
	}
}

func TestCalibrateMarginFromReport_MismatchedEmbedIdentityIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	_, err := CalibrateMarginFromReport(report, MarginCalibrationOptions{TargetEmbedIdentity: "other/embed#tag", TargetDimension: testDimension})
	if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
		t.Fatalf("err = %v, want ErrEmbeddingIdentityMismatch", err)
	}
}

func TestCalibrateMarginFromReport_AbsentReportEmbedIdentityIsAnError(t *testing.T) {
	report := CalibrationReport{Cases: []CalibrationCase{eligibleCase("project", "p1", "project", "p1", 0.3)}}
	_, err := CalibrateMarginFromReport(report, marginTestOpts)
	if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
		t.Fatalf("err = %v, want ErrEmbeddingIdentityMismatch", err)
	}
}

func TestCalibrateMarginFromReport_AbsentTargetIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	_, err := CalibrateMarginFromReport(report, MarginCalibrationOptions{})
	if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
		t.Fatalf("err = %v, want ErrEmbeddingIdentityMismatch", err)
	}
}

// --- codex r1 F7: report Tau/TopK must be pinned, same as identity/dimension.

func TestCalibrateMarginFromReport_MismatchedTauIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	opts := marginTestOpts
	opts.TargetTau = testMarginTau + 0.05
	_, err := CalibrateMarginFromReport(report, opts)
	if !errors.Is(err, ErrMarginReportConfigMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportConfigMismatch", err)
	}
}

func TestCalibrateMarginFromReport_MismatchedTopKIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	opts := marginTestOpts
	opts.TargetTopK = testMarginTopK + 5
	_, err := CalibrateMarginFromReport(report, opts)
	if !errors.Is(err, ErrMarginReportConfigMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportConfigMismatch", err)
	}
}

func TestCalibrateMarginFromReport_AbsentReportTauIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	report.Tau = 0
	_, err := CalibrateMarginFromReport(report, marginTestOpts)
	if !errors.Is(err, ErrMarginReportConfigMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportConfigMismatch", err)
	}
}

func TestCalibrateMarginFromReport_AbsentReportTopKIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	report.TopK = 0
	_, err := CalibrateMarginFromReport(report, marginTestOpts)
	if !errors.Is(err, ErrMarginReportConfigMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportConfigMismatch", err)
	}
}

func TestCalibrateMarginFromReport_NonPositiveTargetTauIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	opts := marginTestOpts
	opts.TargetTau = 0
	_, err := CalibrateMarginFromReport(report, opts)
	if !errors.Is(err, ErrMarginReportConfigMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportConfigMismatch", err)
	}
}

func TestCalibrateMarginFromReport_NonPositiveTargetTopKIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	opts := marginTestOpts
	opts.TargetTopK = 0
	_, err := CalibrateMarginFromReport(report, opts)
	if !errors.Is(err, ErrMarginReportConfigMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportConfigMismatch", err)
	}
}

// TestCalibrateMarginFromReport_MatchingTauAndTopKProceeds is the positive
// counterpart: exact-matching Tau/TopK must NOT be refused.
func TestCalibrateMarginFromReport_MatchingTauAndTopKProceeds(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.3))
	if _, err := CalibrateMarginFromReport(report, marginTestOpts); err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v, want no error for exact-matching tau/topK", err)
	}
}

// TestCalibrateMarginFromReport_NoEligibleCasesIsAnError proves a report
// whose cases NEVER reach the eligible population (e.g. every case's top-1
// was uncorroborated) refuses with ErrNoMarginSamples rather than silently
// returning a zero-value result.
func TestCalibrateMarginFromReport_NoEligibleCasesIsAnError(t *testing.T) {
	uncorroborated := eligibleCase("project", "p1", "project", "p1", 0.3)
	uncorroborated.VectorTop1.Corroborated = false // no longer eligible
	report := marginReport(uncorroborated)
	_, err := CalibrateMarginFromReport(report, marginTestOpts)
	if !errors.Is(err, ErrNoMarginSamples) {
		t.Fatalf("err = %v, want ErrNoMarginSamples", err)
	}
}

func TestCalibrateMarginFromReport_EmptyReportIsAnError(t *testing.T) {
	_, err := CalibrateMarginFromReport(marginReport(), marginTestOpts)
	if !errors.Is(err, ErrNoMarginSamples) {
		t.Fatalf("err = %v, want ErrNoMarginSamples", err)
	}
}

// --- Eligibility predicate: Phase 2(c) revision -----------------------------
//
// vectorSearchComplete is NO LONGER part of the eligibility predicate (see
// CalibrateMarginFromReport's doc comment for the geometric justification:
// truncation only ever cuts the TAIL of a distance-ordered ANN result, never
// reorders or drops top-1/top-2). These tests pin that a truncated -- or
// unmeasured-truncation -- vector arm is now INCLUDED, the exact inverse of
// the pre-Phase-2(c) behavior.

func TestCalibrateMarginFromReport_IncludesNilVectorSearchTruncated(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.3)
	c.VectorSearchTruncated = nil
	result, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.CorrectSampleSize != 1 {
		t.Fatalf("CorrectSampleSize = %d, want 1 -- a nil VectorSearchTruncated must no longer exclude a case", result.CorrectSampleSize)
	}
}

func TestCalibrateMarginFromReport_IncludesTruncatedVectorArm(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.3)
	c.VectorSearchTruncated = boolPtr(true)
	result, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.CorrectSampleSize != 1 {
		t.Fatalf("CorrectSampleSize = %d, want 1 -- a truncated vector arm must no longer exclude a case", result.CorrectSampleSize)
	}
}

func TestCalibrateMarginFromReport_ExcludesNilVectorTop2(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.3)
	// codex r6 L3: VectorTop2 nil with VectorMargin still set is an
	// IMPOSSIBLE shape (checkMarginConsistency now refuses it before this
	// eligibility check ever runs) -- clear both together, the shape a
	// real "only one distinct vector-arm subject" case actually has.
	c.VectorTop2 = nil
	c.VectorMargin = nil
	_, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if !errors.Is(err, ErrNoMarginSamples) {
		t.Fatalf("a case with no second vector-arm subject (no margin) must be excluded, got err=%v", err)
	}
}

func TestCalibrateMarginFromReport_ExcludesUncorroboratedTop1(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.3)
	c.VectorTop1.Corroborated = false
	_, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if !errors.Is(err, ErrNoMarginSamples) {
		t.Fatalf("an uncorroborated top-1 must be excluded (never reaches the carve-out in production), got err=%v", err)
	}
}

// --- Safety-unmeasured (no wrong-top1 case at all) -------------------------

// TestCalibrateMarginFromReport_AllCorrectIsUnmeasuredNotApplyReady is the
// vacuous-truth guard's pinning test: a report where every eligible case's
// top-1 happens to be correct has NOTHING to validate M against, so
// ApplyReady must be false and ThresholdM must stay nil -- never a number
// that reads as "zero wrong commits, safe" when zero wrong CASES were even
// measured.
func TestCalibrateMarginFromReport_AllCorrectIsUnmeasuredNotApplyReady(t *testing.T) {
	report := marginReport(
		eligibleCase("project", "p1", "project", "p1", 0.10),
		eligibleCase("project", "p2", "project", "p2", 0.40),
	)
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.ApplyReady {
		t.Fatal("ApplyReady = true, want false -- no wrong-top1 case was ever measured")
	}
	if result.SafetyMeasured {
		t.Fatal("SafetyMeasured = true, want false")
	}
	if result.ThresholdM != nil {
		t.Fatalf("ThresholdM = %v, want nil -- an M computed from zero wrong samples is a vacuous-truth hazard", *result.ThresholdM)
	}
	if result.CorrectSampleSize != 2 || result.WrongSampleSize != 0 {
		t.Fatalf("sample sizes = correct:%d wrong:%d, want 2/0", result.CorrectSampleSize, result.WrongSampleSize)
	}
}

// --- Zero-tolerance M construction ------------------------------------------

// TestCalibrateMarginFromReport_MRejectsEveryObservedWrongMargin is the core
// pinning test: M must be strictly greater than the LARGEST wrong-top1
// margin observed, so that re-testing every wrong case against M (margin >=
// M) never commits.
func TestCalibrateMarginFromReport_MRejectsEveryObservedWrongMargin(t *testing.T) {
	wrongA := eligibleCase("project", "wrong-a", "project", "correct-a", 0.05) // wrong, low margin
	wrongB := eligibleCase("project", "wrong-b", "project", "correct-b", 0.20) // wrong, HIGHEST margin -- must set M
	p3 := eligibleCase("project", "p3", "project", "p3", 0.35)                 // correct, clears the resulting M
	p4 := eligibleCase("project", "p4", "project", "p4", 0.15)                 // correct, does NOT clear it
	report := marginReport(wrongA, wrongB, p3, p4)
	// The actual stored/recomputed margins -- NOT the clean decimal
	// arguments above, which eligibleCase's H2-safe construction does not
	// reproduce bit-for-bit (see that function's own comment).
	wrongAMargin, wrongBMargin := *wrongA.VectorMargin, *wrongB.VectorMargin

	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if !result.ApplyReady || !result.SafetyMeasured {
		t.Fatalf("ApplyReady=%v SafetyMeasured=%v, want both true", result.ApplyReady, result.SafetyMeasured)
	}
	if result.ThresholdM == nil {
		t.Fatal("ThresholdM = nil, want a computed value")
	}
	if result.WrongMarginMax == nil || *result.WrongMarginMax != wrongBMargin {
		t.Fatalf("WrongMarginMax = %v, want %v (the largest of the two wrong margins)", result.WrongMarginMax, wrongBMargin)
	}
	if *result.ThresholdM <= wrongBMargin {
		t.Fatalf("ThresholdM = %v, want strictly greater than %v (the largest wrong margin)", *result.ThresholdM, wrongBMargin)
	}
	if math.Nextafter(wrongBMargin, math.Inf(1)) != *result.ThresholdM {
		t.Fatalf("ThresholdM = %v, want exactly one ULP above %v (%v)", *result.ThresholdM, wrongBMargin, math.Nextafter(wrongBMargin, math.Inf(1)))
	}
	// Every wrong margin must fall strictly below M: re-testing the sample
	// against M must commit zero wrong cases.
	for _, wrong := range []float64{wrongAMargin, wrongBMargin} {
		if wrong >= *result.ThresholdM {
			t.Fatalf("wrong margin %v clears ThresholdM %v -- M does not reject every observed wrong case", wrong, *result.ThresholdM)
		}
	}
	// Reach: only the p3 case (margin 0.35) clears M; p4 (0.15) does not.
	if result.CorrectSampleSize != 2 || result.WrongSampleSize != 2 {
		t.Fatalf("sample sizes = correct:%d wrong:%d, want 2/2", result.CorrectSampleSize, result.WrongSampleSize)
	}
	if math.Abs(result.AchievedReach-0.5) > 1e-9 {
		t.Fatalf("AchievedReach = %v, want 0.5 (1 of 2 correct cases clears M)", result.AchievedReach)
	}
}

// TestCalibrateMarginFromReport_ZeroReachIsStillApplyReady proves reach and
// safety are independent gates: M can validly reject every wrong case while
// happening to admit zero correct cases on THIS sample -- that is a real,
// reportable (if disappointing) measurement, not a failure of the M
// construction itself.
func TestCalibrateMarginFromReport_ZeroReachIsStillApplyReady(t *testing.T) {
	report := marginReport(
		eligibleCase("project", "wrong", "project", "correct-x", 0.50), // wrong, high margin
		eligibleCase("project", "p2", "project", "p2", 0.10),           // correct, low margin -- will NOT clear M
	)
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if !result.ApplyReady {
		t.Fatal("ApplyReady = false, want true -- the safety construction succeeded even though reach is zero")
	}
	if result.AchievedReach != 0 {
		t.Fatalf("AchievedReach = %v, want 0", result.AchievedReach)
	}
}

// TestCalibrateMarginFromReport_NoCorrectCasesLeavesReachUnmeasured proves
// the ReachMeasured=false branch: wrong-top1 cases exist (M IS computed) but
// zero correct-top1 cases exist to test reach against.
func TestCalibrateMarginFromReport_NoCorrectCasesLeavesReachUnmeasured(t *testing.T) {
	report := marginReport(eligibleCase("project", "wrong", "project", "correct", 0.25))
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if !result.ApplyReady {
		t.Fatal("ApplyReady = false, want true")
	}
	if result.ReachMeasured {
		t.Fatal("ReachMeasured = true, want false -- zero correct-top1 cases exist")
	}
	if result.AchievedReach != 0 {
		t.Fatalf("AchievedReach = %v, want 0 (unmeasured, not vacuously 1)", result.AchievedReach)
	}
	if result.ThresholdM == nil {
		t.Fatal("ThresholdM = nil, want a computed value -- M does not need correct samples to be constructed")
	}
}

func TestCalibrateMarginFromReport_SmallWrongSampleCaveatAppearsInNote(t *testing.T) {
	report := marginReport(eligibleCase("project", "wrong", "project", "correct", 0.25))
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if !strings.Contains(result.Note, "CAVEAT") {
		t.Fatalf("Note = %q, want a small-sample CAVEAT (only 1 wrong-top1 sample)", result.Note)
	}
}

// --- Phase 2(c): no-match CONTROL population --------------------------------

// TestCalibrateMarginFromReport_CorroboratedControlIsWrongByDefinition is
// the core Phase 2(c) pinning test: a corroborated control top-1 feeds
// WrongSampleSize (and WrongSampleSizeFromControls) WITHOUT any ExpectKind/
// ExpectID comparison -- a control has no correct subject to compare
// against at all.
func TestCalibrateMarginFromReport_CorroboratedControlIsWrongByDefinition(t *testing.T) {
	control := eligibleControlCase("project", "ghost", 0.20)
	// The wrong margin actually stored/recomputed, matching eligibleControlCase's
	// own construction exactly (not the literal 0.20 -- see that function's
	// H2 comment on why they can differ by float64 rounding).
	wrongMargin := *control.VectorMargin
	report := marginReportWithControls(nil, []CalibrationCase{control})
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if !result.SafetyMeasured || result.WrongSampleSize != 1 {
		t.Fatalf("SafetyMeasured=%v WrongSampleSize=%d, want true/1", result.SafetyMeasured, result.WrongSampleSize)
	}
	if result.WrongSampleSizeFromControls != 1 {
		t.Fatalf("WrongSampleSizeFromControls = %d, want 1", result.WrongSampleSizeFromControls)
	}
	if result.ThresholdM == nil || *result.ThresholdM <= wrongMargin {
		t.Fatalf("ThresholdM = %v, want strictly greater than the wrong margin %v", result.ThresholdM, wrongMargin)
	}
}

// TestCalibrateMarginFromReport_UncorroboratedControlIsExcluded proves a
// control is subject to the SAME corroboration precondition as a scored
// case -- an uncorroborated control top-1 never reaches the carve-out in
// production either, so it must not feed WrongSampleSize.
func TestCalibrateMarginFromReport_UncorroboratedControlIsExcluded(t *testing.T) {
	control := eligibleControlCase("project", "ghost", 0.20)
	control.VectorTop1.Corroborated = false
	report := marginReportWithControls([]CalibrationCase{eligibleCase("project", "p1", "project", "p1", 0.10)}, []CalibrationCase{control})
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.WrongSampleSize != 0 || result.ControlsCorroborated != 0 {
		t.Fatalf("WrongSampleSize=%d ControlsCorroborated=%d, want both 0", result.WrongSampleSize, result.ControlsCorroborated)
	}
	if result.ControlsWithVectorArmData != 1 {
		t.Fatalf("ControlsWithVectorArmData = %d, want 1 (data was measured, just not corroborated)", result.ControlsWithVectorArmData)
	}
}

// TestCalibrateMarginFromReport_ZeroControlsCorroboratedIsRecordedNotSilent
// pins team-lead's explicit instruction: when zero controls are
// corroborated, that must be a MEASURED, reported fact (a present zero on
// ControlsCorroborated, alongside a nonzero ControlsInReport), not an
// absent/undetectable state.
func TestCalibrateMarginFromReport_ZeroControlsCorroboratedIsRecordedNotSilent(t *testing.T) {
	uncorroboratedControl := eligibleControlCase("project", "ghost", 0.20)
	uncorroboratedControl.VectorTop1.Corroborated = false
	report := marginReportWithControls(
		[]CalibrationCase{eligibleCase("project", "wrong", "project", "correct", 0.05)},
		[]CalibrationCase{uncorroboratedControl, uncorroboratedControl},
	)
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.ControlsInReport != 2 {
		t.Fatalf("ControlsInReport = %d, want 2", result.ControlsInReport)
	}
	if result.ControlsCorroborated != 0 {
		t.Fatalf("ControlsCorroborated = %d, want 0", result.ControlsCorroborated)
	}
}

// TestCalibrateMarginFromReport_ControlCorroboratedWithoutMarginIsNotCountedAsWrong
// proves a corroborated control with only ONE distinct vector-arm subject
// (no VectorTop2 -- no competitor to measure a margin against) is tallied in
// ControlsCorroboratedWithoutMargin, NOT folded into WrongSampleSize (there
// is no margin value to test against M at all).
func TestCalibrateMarginFromReport_ControlCorroboratedWithoutMarginIsNotCountedAsWrong(t *testing.T) {
	control := eligibleControlCase("project", "ghost", 0.20)
	control.VectorTop2 = nil
	control.VectorMargin = nil
	report := marginReportWithControls([]CalibrationCase{eligibleCase("project", "p1", "project", "p1", 0.10)}, []CalibrationCase{control})
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.WrongSampleSize != 0 {
		t.Fatalf("WrongSampleSize = %d, want 0 -- no margin exists to test against M", result.WrongSampleSize)
	}
	if result.ControlsCorroborated != 1 || result.ControlsCorroboratedWithoutMargin != 1 {
		t.Fatalf("ControlsCorroborated=%d ControlsCorroboratedWithoutMargin=%d, want 1/1", result.ControlsCorroborated, result.ControlsCorroboratedWithoutMargin)
	}
}

// TestCalibrateMarginFromReport_ScoredWrongAndControlWrongUnion proves the
// UNION rule end to end: a scored wrong-top1 case AND a corroborated control
// both contribute to WrongSampleSize, and M rejects the max of BOTH.
func TestCalibrateMarginFromReport_ScoredWrongAndControlWrongUnion(t *testing.T) {
	report := marginReportWithControls(
		[]CalibrationCase{
			eligibleCase("project", "wrong-scored", "project", "correct", 0.10),
			eligibleCase("project", "p2", "project", "p2", 0.40), // correct
		},
		[]CalibrationCase{eligibleControlCase("project", "ghost", 0.25)}, // highest wrong margin
	)
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.WrongSampleSize != 2 || result.WrongSampleSizeFromControls != 1 {
		t.Fatalf("WrongSampleSize=%d WrongSampleSizeFromControls=%d, want 2/1", result.WrongSampleSize, result.WrongSampleSizeFromControls)
	}
	if result.WrongMarginMax == nil || *result.WrongMarginMax != 0.25 {
		t.Fatalf("WrongMarginMax = %v, want 0.25 (the control's margin, the larger of the two wrong margins)", result.WrongMarginMax)
	}
	if result.ThresholdM == nil || *result.ThresholdM <= 0.25 {
		t.Fatalf("ThresholdM = %v, want strictly greater than 0.25", result.ThresholdM)
	}
	// The correct case (margin 0.40) clears M; nothing else is correct.
	if result.CorrectSampleSize != 1 || result.AchievedReach != 1 {
		t.Fatalf("CorrectSampleSize=%d AchievedReach=%v, want 1/1", result.CorrectSampleSize, result.AchievedReach)
	}
}

// --- codex r3 H2: report internal consistency (stored margin vs recomputed) -

// TestCalibrateMarginFromReport_TamperedScoredMarginIsAnError is H2's core
// pinning test: a case whose stored VectorMargin does NOT equal
// Top1.Similarity-Top2.Similarity (simulating corruption, hand-editing, or
// a producer bug) must hard-fail the WHOLE calibration, not silently
// mis-measure M from the wrong number.
func TestCalibrateMarginFromReport_TamperedScoredMarginIsAnError(t *testing.T) {
	tampered := eligibleCase("project", "p1", "project", "p1", 0.30)
	wrongMargin := *tampered.VectorMargin + 1.0 // deliberately does not match top1-top2
	tampered.VectorMargin = &wrongMargin
	_, err := CalibrateMarginFromReport(marginReport(tampered), marginTestOpts)
	if !errors.Is(err, ErrMarginReportInternallyInconsistent) {
		t.Fatalf("err = %v, want ErrMarginReportInternallyInconsistent", err)
	}
}

// TestCalibrateMarginFromReport_TamperedControlMarginIsAnError is the same
// proof for a CONTROL case, pinning that checkMarginConsistency runs over
// BOTH populations.
func TestCalibrateMarginFromReport_TamperedControlMarginIsAnError(t *testing.T) {
	tampered := eligibleControlCase("project", "ghost", 0.20)
	wrongMargin := *tampered.VectorMargin + 1.0
	tampered.VectorMargin = &wrongMargin
	_, err := CalibrateMarginFromReport(marginReportWithControls(nil, []CalibrationCase{tampered}), marginTestOpts)
	if !errors.Is(err, ErrMarginReportInternallyInconsistent) {
		t.Fatalf("err = %v, want ErrMarginReportInternallyInconsistent", err)
	}
}

// TestCalibrateMarginFromReport_NonFiniteSimilarityIsAnError proves a NaN
// or infinite similarity is refused even if it happens to make the stored
// margin subtraction "balance" -- a non-finite value can never be a real
// measurement.
func TestCalibrateMarginFromReport_NonFiniteSimilarityIsAnError(t *testing.T) {
	broken := eligibleCase("project", "p1", "project", "p1", 0.30)
	broken.VectorTop1.Similarity = math.NaN()
	_, err := CalibrateMarginFromReport(marginReport(broken), marginTestOpts)
	if !errors.Is(err, ErrMarginReportInternallyInconsistent) {
		t.Fatalf("err = %v, want ErrMarginReportInternallyInconsistent for a NaN similarity", err)
	}
}

// TestCalibrateMarginFromReport_ConsistentMarginsStillProceed is the
// positive control: eligibleCase/eligibleControlCase's own H2-safe
// construction (top1.Similarity-top2.Similarity, not the raw margin
// argument) passes the consistency check cleanly -- already exercised by
// every other test in this file succeeding, pinned here explicitly as its
// own named assertion.
func TestCalibrateMarginFromReport_ConsistentMarginsStillProceed(t *testing.T) {
	report := marginReportWithControls(
		[]CalibrationCase{eligibleCase("project", "p1", "project", "p1", 0.30)},
		[]CalibrationCase{eligibleControlCase("project", "ghost", 0.20)},
	)
	if _, err := CalibrateMarginFromReport(report, marginTestOpts); err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v, want no error for internally-consistent margins", err)
	}
}

// --- codex r4 J3: two ranked subjects present but margin missing = corrupt -

// TestCalibrateMarginFromReport_MissingMarginWithBothTopsPresentIsAnError is
// J3's core pinning test: VectorTop1 AND VectorTop2 are both present (two
// ranked subjects genuinely exist), but VectorMargin is nil -- the producer
// always writes one alongside the other, so this specific combination is
// corruption, not an unmeasured case, and must hard-fail rather than
// silently exclude what could be the LARGEST wrong-top1 margin in the
// report.
func TestCalibrateMarginFromReport_MissingMarginWithBothTopsPresentIsAnError(t *testing.T) {
	broken := eligibleCase("project", "p1", "project", "correct-elsewhere", 0.30)
	broken.VectorMargin = nil // VectorTop1/VectorTop2 both still present
	_, err := CalibrateMarginFromReport(marginReport(broken), marginTestOpts)
	if !errors.Is(err, ErrMarginReportInternallyInconsistent) {
		t.Fatalf("err = %v, want ErrMarginReportInternallyInconsistent", err)
	}
}

// TestCalibrateMarginFromReport_MissingMarginWithBothTopsPresentInControlIsAnError
// is the same proof for a CONTROL case.
func TestCalibrateMarginFromReport_MissingMarginWithBothTopsPresentInControlIsAnError(t *testing.T) {
	broken := eligibleControlCase("project", "ghost", 0.20)
	broken.VectorMargin = nil
	_, err := CalibrateMarginFromReport(marginReportWithControls(nil, []CalibrationCase{broken}), marginTestOpts)
	if !errors.Is(err, ErrMarginReportInternallyInconsistent) {
		t.Fatalf("err = %v, want ErrMarginReportInternallyInconsistent", err)
	}
}

// TestCalibrateMarginFromReport_NilVectorTop2StillExemptFromJ3 proves J3 did
// NOT broaden the exemption boundary: a case with VectorTop2 itself nil (no
// second ranked subject at all -- margin is genuinely UNDEFINED, not merely
// unwritten) is still exempt, exactly as it was before this fix -- pinned
// separately from TestCalibrateMarginFromReport_ExcludesNilVectorTop2
// (which asserts the ELIGIBILITY-loop behavior) to assert the
// CONSISTENCY-check behavior specifically.
func TestCalibrateMarginFromReport_NilVectorTop2StillExemptFromJ3(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.30)
	c.VectorTop2 = nil
	c.VectorMargin = nil
	if _, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts); !errors.Is(err, ErrNoMarginSamples) {
		t.Fatalf("err = %v, want ErrNoMarginSamples (excluded by eligibility, not flagged as inconsistent)", err)
	}
}

// --- codex r6 L1: whole-question-fallback cases are excluded ---------------

// TestCalibrateMarginFromReport_FallbackWrongCaseExcludedFromWrongSampleSize
// is L1's core pinning test: a WRONG-top1 case that used the whole-question
// fallback carries the HIGHEST margin in the report -- if it were included,
// it would set ThresholdM. Excluding it must both drop WrongSampleSize AND
// lower ThresholdM to the next-highest (non-fallback) wrong margin.
func TestCalibrateMarginFromReport_FallbackWrongCaseExcludedFromWrongSampleSize(t *testing.T) {
	fallbackWrong := eligibleCase("project", "wrong-fallback", "project", "correct-a", 0.50) // highest margin, must NOT set M
	fallbackWrong.UsedTermFallback = true
	realWrong := eligibleCase("project", "wrong-real", "project", "correct-b", 0.10) // the ONLY margin that should set M
	report := marginReport(fallbackWrong, realWrong)
	realWrongMargin := *realWrong.VectorMargin

	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.WrongSampleSize != 1 {
		t.Fatalf("WrongSampleSize = %d, want 1 -- the fallback case must be excluded", result.WrongSampleSize)
	}
	if result.ExcludedFallbackCases != 1 {
		t.Fatalf("ExcludedFallbackCases = %d, want 1", result.ExcludedFallbackCases)
	}
	if result.WrongMarginMax == nil || *result.WrongMarginMax != realWrongMargin {
		t.Fatalf("WrongMarginMax = %v, want %v (the real case's margin, not the fallback's higher one)", result.WrongMarginMax, realWrongMargin)
	}
}

// TestCalibrateMarginFromReport_FallbackCorrectCaseExcludedFromCorrectSampleSize
// is the CORRECT-top1 half of L1: a fallback case that would otherwise have
// been correct must not inflate CorrectSampleSize/AchievedReach either.
func TestCalibrateMarginFromReport_FallbackCorrectCaseExcludedFromCorrectSampleSize(t *testing.T) {
	fallbackCorrect := eligibleCase("project", "p1", "project", "p1", 0.30)
	fallbackCorrect.UsedTermFallback = true
	wrong := eligibleCase("project", "wrong", "project", "correct", 0.10)
	report := marginReport(fallbackCorrect, wrong)

	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.CorrectSampleSize != 0 {
		t.Fatalf("CorrectSampleSize = %d, want 0 -- the fallback correct case must be excluded", result.CorrectSampleSize)
	}
	if result.ExcludedFallbackCases != 1 {
		t.Fatalf("ExcludedFallbackCases = %d, want 1", result.ExcludedFallbackCases)
	}
	if result.ReachMeasured {
		t.Fatal("ReachMeasured = true, want false -- the only correct-top1 case was excluded")
	}
}

// TestCalibrateMarginFromReport_FallbackControlExcluded is the CONTROL half
// of L1.
func TestCalibrateMarginFromReport_FallbackControlExcluded(t *testing.T) {
	fallbackControl := eligibleControlCase("project", "ghost", 0.20)
	fallbackControl.UsedTermFallback = true
	report := marginReportWithControls(
		[]CalibrationCase{eligibleCase("project", "p1", "project", "p1", 0.10)},
		[]CalibrationCase{fallbackControl},
	)
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.WrongSampleSizeFromControls != 0 || result.WrongSampleSize != 0 {
		t.Fatalf("WrongSampleSizeFromControls=%d WrongSampleSize=%d, want both 0 -- the fallback control must be excluded", result.WrongSampleSizeFromControls, result.WrongSampleSize)
	}
	if result.ExcludedFallbackControls != 1 {
		t.Fatalf("ExcludedFallbackControls = %d, want 1", result.ExcludedFallbackControls)
	}
	// The RAW measured facts (corroboration) are still recorded per Phase
	// 2(c)'s "unconditionally" discipline -- only the WRONG-population
	// contribution is what L1 excludes.
	if result.ControlsCorroborated != 1 {
		t.Fatalf("ControlsCorroborated = %d, want 1 -- corroboration is a measured fact independent of the fallback exclusion", result.ControlsCorroborated)
	}
}

// TestCalibrateMarginFromReport_AllFallbackEmptiesWrongPopulationVacuously
// proves team-lead's own stated consequence: excluding every wrong-top1
// case as fallback empties WrongSampleSize entirely, and the EXISTING
// ApplyReady=false vacuous-truth guard fires on its own -- no separate
// guard was added for this case.
func TestCalibrateMarginFromReport_AllFallbackEmptiesWrongPopulationVacuously(t *testing.T) {
	fallbackWrong := eligibleCase("project", "wrong", "project", "correct", 0.30)
	fallbackWrong.UsedTermFallback = true
	// A non-fallback CORRECT case keeps the OVERALL eligible population
	// non-empty (CorrectSampleSize+WrongSampleSize > 0), so this test
	// exercises the ApplyReady=false vacuous-truth guard specifically --
	// not ErrNoMarginSamples, which fires only when the population is
	// empty on BOTH sides (see TestCalibrateMarginFromReport_NoEligibleCasesIsAnError
	// for that separate, pre-existing case).
	onlyCorrect := eligibleCase("project", "p1", "project", "p1", 0.10)
	report := marginReport(fallbackWrong, onlyCorrect)
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.ApplyReady || result.SafetyMeasured {
		t.Fatalf("ApplyReady=%v SafetyMeasured=%v, want both false -- the only wrong-top1 case was excluded as fallback", result.ApplyReady, result.SafetyMeasured)
	}
	if result.ExcludedFallbackCases != 1 {
		t.Fatalf("ExcludedFallbackCases = %d, want 1", result.ExcludedFallbackCases)
	}
	if result.CorrectSampleSize != 1 {
		t.Fatalf("CorrectSampleSize = %d, want 1 -- the non-fallback correct case must still be included", result.CorrectSampleSize)
	}
}

// TestCalibrateMarginFromReport_NonFallbackCasesUnaffectedByL1 is the
// negative control: with UsedTermFallback left at its zero value (false,
// every pre-L1 fixture's implicit state), ExcludedFallbackCases/
// ExcludedFallbackControls stay 0 and every sample is included exactly as
// before this fix.
func TestCalibrateMarginFromReport_NonFallbackCasesUnaffectedByL1(t *testing.T) {
	report := marginReportWithControls(
		[]CalibrationCase{eligibleCase("project", "wrong", "project", "correct", 0.10)},
		[]CalibrationCase{eligibleControlCase("project", "ghost", 0.20)},
	)
	result, err := CalibrateMarginFromReport(report, marginTestOpts)
	if err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v", err)
	}
	if result.ExcludedFallbackCases != 0 || result.ExcludedFallbackControls != 0 {
		t.Fatalf("ExcludedFallbackCases=%d ExcludedFallbackControls=%d, want both 0", result.ExcludedFallbackCases, result.ExcludedFallbackControls)
	}
	if result.WrongSampleSize != 2 {
		t.Fatalf("WrongSampleSize = %d, want 2 -- neither case used the fallback", result.WrongSampleSize)
	}
}

// --- codex r6 L2: report.Controls must mirror len(ControlCases) ------------

// TestCalibrateMarginFromReport_ControlsCountMismatchIsAnError is L2's core
// pinning test: report.Controls disagrees with len(report.ControlCases),
// simulating a truncated report -- must hard-fail before a single control
// case is read.
func TestCalibrateMarginFromReport_ControlsCountMismatchIsAnError(t *testing.T) {
	report := marginReportWithControls(nil, []CalibrationCase{eligibleControlCase("project", "ghost", 0.20)})
	report.Controls = 5 // the harness declared 5, but only 1 survived
	_, err := CalibrateMarginFromReport(report, marginTestOpts)
	if !errors.Is(err, ErrMarginReportControlsCountMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportControlsCountMismatch", err)
	}
}

// TestCalibrateMarginFromReport_ControlsCountMismatchWhenTruncatedToZeroIsAnError
// proves the check catches a report truncated all the way to ZERO surviving
// control cases too, not merely a partial truncation.
func TestCalibrateMarginFromReport_ControlsCountMismatchWhenTruncatedToZeroIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.10))
	report.Controls = 3 // declared 3, ControlCases is empty
	_, err := CalibrateMarginFromReport(report, marginTestOpts)
	if !errors.Is(err, ErrMarginReportControlsCountMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportControlsCountMismatch", err)
	}
}

// TestCalibrateMarginFromReport_ControlsCountMatchProceeds is the positive
// control: report.Controls==len(ControlCases) (marginReportWithControls'
// own construction) must not be refused -- already exercised implicitly by
// every OTHER control-population test in this file passing; pinned here
// explicitly as its own named assertion, mirroring
// TestCalibrateMarginFromReport_ConsistentMarginsStillProceed's pattern.
func TestCalibrateMarginFromReport_ControlsCountMatchProceeds(t *testing.T) {
	report := marginReportWithControls(nil, []CalibrationCase{eligibleControlCase("project", "ghost", 0.20)})
	if _, err := CalibrateMarginFromReport(report, marginTestOpts); err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v, want no error when Controls matches len(ControlCases)", err)
	}
}

// TestCalibrateMarginFromReport_ZeroControlsDeclaredAndZeroPresentProceeds
// proves the check does not misfire on the ordinary "no controls in this
// report at all" case (report.Controls==0==len(nil)) -- marginReport's own
// construction, already exercised by most tests in this file; pinned
// explicitly so a future change to the check's zero-handling trips a named
// test.
func TestCalibrateMarginFromReport_ZeroControlsDeclaredAndZeroPresentProceeds(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.30))
	if _, err := CalibrateMarginFromReport(report, marginTestOpts); err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v, want no error for a report with no controls at all", err)
	}
}

// --- codex r7 M3: report.Scored must mirror len(Cases) -- exact L2 symmetry

// TestCalibrateMarginFromReport_ScoredCountMismatchIsAnError is M3's core
// pinning test, the EXACT symmetric counterpart of
// TestCalibrateMarginFromReport_ControlsCountMismatchIsAnError.
func TestCalibrateMarginFromReport_ScoredCountMismatchIsAnError(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.30))
	report.Scored = 5 // the harness declared 5, but only 1 survived
	_, err := CalibrateMarginFromReport(report, marginTestOpts)
	if !errors.Is(err, ErrMarginReportScoredCountMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportScoredCountMismatch", err)
	}
}

// TestCalibrateMarginFromReport_ScoredCountMismatchWhenTruncatedToZeroIsAnError
// proves the check catches a report truncated all the way to ZERO surviving
// scored cases too.
func TestCalibrateMarginFromReport_ScoredCountMismatchWhenTruncatedToZeroIsAnError(t *testing.T) {
	report := marginReportWithControls(nil, []CalibrationCase{eligibleControlCase("project", "ghost", 0.20)})
	report.Scored = 3 // declared 3, Cases is empty
	_, err := CalibrateMarginFromReport(report, marginTestOpts)
	if !errors.Is(err, ErrMarginReportScoredCountMismatch) {
		t.Fatalf("err = %v, want ErrMarginReportScoredCountMismatch", err)
	}
}

// TestCalibrateMarginFromReport_ScoredCountMatchProceeds is the positive
// control -- marginReport's own construction (Scored==len(cases)) must not
// be refused, already exercised implicitly by every other test in this file
// passing; pinned explicitly, mirroring
// TestCalibrateMarginFromReport_ControlsCountMatchProceeds.
func TestCalibrateMarginFromReport_ScoredCountMatchProceeds(t *testing.T) {
	report := marginReport(eligibleCase("project", "p1", "project", "p1", 0.30))
	if _, err := CalibrateMarginFromReport(report, marginTestOpts); err != nil {
		t.Fatalf("CalibrateMarginFromReport: %v, want no error when Scored matches len(Cases)", err)
	}
}

// --- codex r6 L3: the closed set of valid vector-arm shapes -----------------

// TestCalibrateMarginFromReport_VectorTop2WithoutVectorTop1IsAnError is L3's
// asymmetric core pinning test: VectorTop2 present, VectorTop1 nil -- an
// impossible shape (a "second place" cannot exist without a "first place").
func TestCalibrateMarginFromReport_VectorTop2WithoutVectorTop1IsAnError(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.30)
	c.VectorTop1 = nil
	// c.VectorTop2 and c.VectorMargin stay set from eligibleCase.
	_, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if !errors.Is(err, ErrMarginReportInternallyInconsistent) {
		t.Fatalf("err = %v, want ErrMarginReportInternallyInconsistent", err)
	}
}

// TestCalibrateMarginFromReport_VectorMarginWithoutVectorTop1IsAnError is
// the same proof for VectorMargin present with VectorTop1 nil (VectorTop2
// also present here, since VectorMargin cannot exist without SOME top2 to
// have been subtracted from).
func TestCalibrateMarginFromReport_VectorMarginWithoutVectorTop1IsAnError(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.30)
	c.VectorTop1 = nil
	_, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if !errors.Is(err, ErrMarginReportInternallyInconsistent) {
		t.Fatalf("err = %v, want ErrMarginReportInternallyInconsistent", err)
	}
}

// TestCalibrateMarginFromReport_VectorMarginWithoutVectorTop2IsAnError proves
// the LAST impossible cell: VectorTop1 present, VectorTop2 nil, but
// VectorMargin somehow present (a margin with no second value to have been
// computed from).
func TestCalibrateMarginFromReport_VectorMarginWithoutVectorTop2IsAnError(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.30)
	c.VectorTop2 = nil
	// c.VectorMargin stays set from eligibleCase -- the impossible cell.
	_, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if !errors.Is(err, ErrMarginReportInternallyInconsistent) {
		t.Fatalf("err = %v, want ErrMarginReportInternallyInconsistent", err)
	}
}

// TestCalibrateMarginFromReport_VectorTop1OnlyIsAValidShape is the positive
// control for L3's SECOND valid shape (one distinct vector-arm subject, no
// competitor) -- must proceed to the eligibility loop (and there be
// excluded for lack of a margin, per the PRE-EXISTING
// TestCalibrateMarginFromReport_ExcludesNilVectorTop2 behavior), not be
// flagged as an impossible shape.
func TestCalibrateMarginFromReport_VectorTop1OnlyIsAValidShape(t *testing.T) {
	c := eligibleCase("project", "p1", "project", "p1", 0.30)
	c.VectorTop2 = nil
	c.VectorMargin = nil
	_, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if !errors.Is(err, ErrNoMarginSamples) {
		t.Fatalf("err = %v, want ErrNoMarginSamples -- {VectorTop1 only} is a VALID shape, excluded by eligibility, not flagged as inconsistent", err)
	}
}

// TestCalibrateMarginFromReport_AllAbsentIsAValidShape is the positive
// control for L3's FIRST valid shape (no vector-arm data measured for this
// case at all).
func TestCalibrateMarginFromReport_AllAbsentIsAValidShape(t *testing.T) {
	c := CalibrationCase{Cause: "subject_missing", ExpectKind: "project", ExpectID: "p1"}
	_, err := CalibrateMarginFromReport(marginReport(c), marginTestOpts)
	if !errors.Is(err, ErrNoMarginSamples) {
		t.Fatalf("err = %v, want ErrNoMarginSamples -- {all absent} is a VALID shape, excluded by eligibility, not flagged as inconsistent", err)
	}
}

// --- runMarginCalibrationRunner (margin_calibration_live_test.go) ----------

// TestMarginCalibrationRunner_GateFailingReportFailsWithoutWritingAnArtifact
// is codex r8 N2's core pinning test, REVERSING the runner's ORIGINAL
// Phase-1/2 divergence (see runMarginCalibrationRunner's own doc comment for
// why): a not-apply-ready verdict now calls Fatalf and writes NOTHING to
// outputPath, exactly mirroring runCalibrationRunner's own gate-failing
// behavior (tau_calibration_live_test.go) -- a downstream consumer must
// never see a "done" exit code, or a file at outputPath, for a run that
// measured no wrong-top1 case to validate M against.
func TestMarginCalibrationRunner_GateFailingReportFailsWithoutWritingAnArtifact(t *testing.T) {
	report := marginReport(
		eligibleCase("project", "p1", "project", "p1", 0.10), // correct only -- SafetyMeasured=false
	)
	fake := &fakeRunnerT{}
	outputPath := t.TempDir() + "/margin.json"
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fakeRunnerTFatal); !ok {
					panic(r)
				}
			}
		}()
		runMarginCalibrationRunner(fake, report, marginTestOpts, "", outputPath)
	}()
	if !fake.failed {
		t.Fatal("expected Fatalf for a not-apply-ready (SafetyMeasured=false) report")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("expected NO artifact written for a not-apply-ready verdict, os.Stat err=%v", err)
	}
}

// TestMarginCalibrationRunner_ApplyReadyReportStillWritesTheArtifact is the
// positive-direction control for N2's reversal: an ApplyReady=true report
// must still complete normally and write its artifact -- the Fatalf gate
// added above must fire ONLY on ApplyReady=false, never as a side effect on
// the success path.
func TestMarginCalibrationRunner_ApplyReadyReportStillWritesTheArtifact(t *testing.T) {
	report := marginReport(
		eligibleCase("project", "wrong", "project", "correct", 0.10), // wrong -- SafetyMeasured=true
		eligibleCase("project", "p2", "project", "p2", 0.40),         // correct
	)
	fake := &fakeRunnerT{}
	outputPath := t.TempDir() + "/margin.json"
	runMarginCalibrationRunner(fake, report, marginTestOpts, "", outputPath)
	if fake.failed {
		t.Fatalf("runMarginCalibrationRunner called Fatalf, want it to complete normally for an apply-ready report: logs=%v", fake.logs)
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("expected an artifact to be written for an apply-ready verdict: %v", err)
	}
	var decoded MarginCalibrationResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode written artifact: %v", err)
	}
	if !decoded.ApplyReady {
		t.Fatal("written artifact ApplyReady = false, want true (this fixture has both a wrong-top1 and a correct-top1 case)")
	}
}

// TestMarginCalibrationRunner_ApplyReadyReportWritesTheThreshold pins the
// happy path end to end: a report with both correct and wrong eligible cases
// produces a written artifact whose ThresholdM round-trips.
func TestMarginCalibrationRunner_ApplyReadyReportWritesTheThreshold(t *testing.T) {
	report := marginReport(
		eligibleCase("project", "wrong", "project", "correct", 0.10),
		eligibleCase("project", "p2", "project", "p2", 0.40),
	)
	fake := &fakeRunnerT{}
	outputPath := t.TempDir() + "/margin.json"
	fakeReportPath := t.TempDir() + "/report.json" // codex r3 H3: need not exist on disk -- only its VALUE is stamped into the artifact
	runMarginCalibrationRunner(fake, report, marginTestOpts, fakeReportPath, outputPath)
	if fake.failed {
		t.Fatalf("runMarginCalibrationRunner unexpectedly called Fatalf: logs=%v", fake.logs)
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read written artifact: %v", err)
	}
	var decoded MarginCalibrationArtifact
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode written artifact: %v", err)
	}
	if !decoded.ApplyReady || decoded.ThresholdM == nil {
		t.Fatalf("decoded artifact = %+v, want ApplyReady=true with a non-nil ThresholdM", decoded)
	}
	if decoded.TargetEmbedIdentity != testEmbedIdentity || decoded.TargetDimension != testDimension {
		t.Fatalf("decoded target identity/dimension = %s/%d, want %s/%d", decoded.TargetEmbedIdentity, decoded.TargetDimension, testEmbedIdentity, testDimension)
	}
	// codex r3 H3: full provenance -- target tau/topK and source-report
	// path/hash must ALSO round-trip, not just identity/dimension.
	if decoded.TargetTau != testMarginTau || decoded.TargetTopK != testMarginTopK {
		t.Fatalf("decoded target tau/topK = %v/%d, want %v/%d", decoded.TargetTau, decoded.TargetTopK, testMarginTau, testMarginTopK)
	}
	if decoded.SourceReportPath != fakeReportPath {
		t.Fatalf("decoded SourceReportPath = %q, want %q", decoded.SourceReportPath, fakeReportPath)
	}
	if decoded.SourceReportSHA256 == "" {
		t.Fatal("decoded SourceReportSHA256 is empty, want the source report's content hash")
	}
}

// TestMarginCalibrationRunner_InvalidReportStillFails proves the runner DOES
// still fail loudly for a genuine hard error (identity mismatch / no
// eligible samples) -- the "never Fatalf on ApplyReady=false" divergence is
// scoped to the soft verdict only, not to CalibrateMarginFromReport's own
// hard-error contract.
func TestMarginCalibrationRunner_InvalidReportStillFails(t *testing.T) {
	fake := &fakeRunnerT{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fakeRunnerTFatal); !ok {
					panic(r)
				}
			}
		}()
		runMarginCalibrationRunner(fake, marginReport(), marginTestOpts, "", "")
	}()
	if !fake.failed {
		t.Fatal("expected Fatalf for a report with zero eligible cases (ErrNoMarginSamples)")
	}
}
