package falkorgraph

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

// marginTestOpts is the shared target identity/dimension every synthetic
// fixture in this file uses -- mirrors tau_calibration_test.go's
// testEmbedIdentity/testDimension convention (these tests exercise the
// margin MATH, not the identity gate, which has its own dedicated tests
// below).
var marginTestOpts = MarginCalibrationOptions{TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}

// eligibleCase builds one CalibrationCase already inside CalibrateMarginFromReport's
// eligible population (corroborated top-1, measurable margin --
// vectorSearchComplete is NOT part of eligibility as of Phase 2(c)) --
// top1Kind/top1ID is the vector arm's top-1 pick, expectKind/expectID is the
// case's OWN expected subject (equal for a CORRECT case, different for a
// WRONG one), margin is VectorMargin.
func eligibleCase(top1Kind, top1ID, expectKind, expectID string, margin float64) CalibrationCase {
	top2Similarity := 0.5 // arbitrary; only top1's identity/margin matter to the math
	return CalibrationCase{
		ExpectKind:            expectKind,
		ExpectID:              expectID,
		VectorSearchTruncated: boolPtr(false),
		VectorTop1:            &CalibrationVectorArmSubject{Kind: top1Kind, CanonicalID: top1ID, Similarity: top2Similarity + margin, Corroborated: true},
		VectorTop2:            &CalibrationVectorArmSubject{Kind: "project", CanonicalID: "top2", Similarity: top2Similarity},
		VectorMargin:          floatPtr(margin),
	}
}

func marginReport(cases ...CalibrationCase) CalibrationReport {
	return CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: cases}
}

// eligibleControlCase builds one no-match CONTROL CalibrationCase (Phase
// 2(c)) already inside the eligible population -- ExpectKind/ExpectID are
// deliberately left empty (a control has no correct subject), and
// corroborated defaults true unless the caller flips it via the returned
// value.
func eligibleControlCase(top1Kind, top1ID string, margin float64) CalibrationCase {
	top2Similarity := 0.5
	return CalibrationCase{
		VectorSearchTruncated: boolPtr(false),
		VectorTop1:            &CalibrationVectorArmSubject{Kind: top1Kind, CanonicalID: top1ID, Similarity: top2Similarity + margin, Corroborated: true},
		VectorTop2:            &CalibrationVectorArmSubject{Kind: "project", CanonicalID: "control-top2", Similarity: top2Similarity},
		VectorMargin:          floatPtr(margin),
	}
}

func marginReportWithControls(cases []CalibrationCase, controls []CalibrationCase) CalibrationReport {
	return CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: cases, ControlCases: controls}
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
	c.VectorTop2 = nil
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
	report := marginReport(
		eligibleCase("project", "wrong-a", "project", "correct-a", 0.05), // wrong, low margin
		eligibleCase("project", "wrong-b", "project", "correct-b", 0.20), // wrong, HIGHEST margin -- must set M
		eligibleCase("project", "p3", "project", "p3", 0.35),             // correct, clears the resulting M
		eligibleCase("project", "p4", "project", "p4", 0.15),             // correct, does NOT clear it
	)
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
	if result.WrongMarginMax == nil || *result.WrongMarginMax != 0.20 {
		t.Fatalf("WrongMarginMax = %v, want 0.20 (the largest of the two wrong margins)", result.WrongMarginMax)
	}
	if *result.ThresholdM <= 0.20 {
		t.Fatalf("ThresholdM = %v, want strictly greater than 0.20 (the largest wrong margin)", *result.ThresholdM)
	}
	if math.Nextafter(0.20, math.Inf(1)) != *result.ThresholdM {
		t.Fatalf("ThresholdM = %v, want exactly one ULP above 0.20 (%v)", *result.ThresholdM, math.Nextafter(0.20, math.Inf(1)))
	}
	// Every wrong margin (0.05, 0.20) must fall strictly below M: re-testing
	// the sample against M must commit zero wrong cases.
	for _, wrong := range []float64{0.05, 0.20} {
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
	report := marginReportWithControls(nil, []CalibrationCase{eligibleControlCase("project", "ghost", 0.20)})
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
	if result.ThresholdM == nil || *result.ThresholdM <= 0.20 {
		t.Fatalf("ThresholdM = %v, want strictly greater than 0.20", result.ThresholdM)
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

// --- runMarginCalibrationRunner (margin_calibration_live_test.go) ----------

// TestMarginCalibrationRunner_GateFailingReportStillWritesAnArtifact is the
// deliberate DIVERGENCE from runCalibrationRunner's own gate-failing
// behavior: this runner never calls Fatalf on ApplyReady=false -- an
// insufficient-data margin verdict is itself the phase-2 deliverable, to be
// read by a human (see runMarginCalibrationRunner's doc comment), not a
// reason to withhold output the way a not-apply-ready RETRIEVAL policy is.
func TestMarginCalibrationRunner_GateFailingReportStillWritesAnArtifact(t *testing.T) {
	report := marginReport(
		eligibleCase("project", "p1", "project", "p1", 0.10), // correct only -- SafetyMeasured=false
	)
	fake := &fakeRunnerT{}
	outputPath := t.TempDir() + "/margin.json"
	runMarginCalibrationRunner(fake, report, marginTestOpts, outputPath)
	if fake.failed {
		t.Fatalf("runMarginCalibrationRunner called Fatalf, want it to complete and write the diagnostic artifact regardless: logs=%v", fake.logs)
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("expected an artifact to be written even on a not-apply-ready verdict: %v", err)
	}
	var decoded MarginCalibrationResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode written artifact: %v", err)
	}
	if decoded.ApplyReady {
		t.Fatal("written artifact ApplyReady = true, want false (this fixture has no wrong-top1 case)")
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
	runMarginCalibrationRunner(fake, report, marginTestOpts, outputPath)
	if fake.failed {
		t.Fatalf("runMarginCalibrationRunner unexpectedly called Fatalf: logs=%v", fake.logs)
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read written artifact: %v", err)
	}
	var decoded struct {
		MarginCalibrationResult
		TargetEmbedIdentity string `json:"target_embed_identity"`
		TargetDimension     int    `json:"target_dimension"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode written artifact: %v", err)
	}
	if !decoded.ApplyReady || decoded.ThresholdM == nil {
		t.Fatalf("decoded artifact = %+v, want ApplyReady=true with a non-nil ThresholdM", decoded)
	}
	if decoded.TargetEmbedIdentity != testEmbedIdentity || decoded.TargetDimension != testDimension {
		t.Fatalf("decoded target identity/dimension = %s/%d, want %s/%d", decoded.TargetEmbedIdentity, decoded.TargetDimension, testEmbedIdentity, testDimension)
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
		runMarginCalibrationRunner(fake, marginReport(), marginTestOpts, "")
	}()
	if !fake.failed {
		t.Fatal("expected Fatalf for a report with zero eligible cases (ErrNoMarginSamples)")
	}
}
