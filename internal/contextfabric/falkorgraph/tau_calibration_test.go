package falkorgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }
func boolPtr(v bool) *bool        { return &v }

// testEmbedIdentity/testDimension are the SHARED target identity/dimension
// every synthetic-fixture test in this file uses (codex round-4 FIX A):
// CalibrateFromReport now requires the report's own EmbedIdentity/
// EmbedDimension to match CalibrationOptions.TargetEmbedIdentity/
// TargetDimension before doing anything else, so every fixture below
// stamps both to the same pair -- these tests exercise the calibration
// MATH, not the identity gate (which has its own dedicated tests, see
// TestCalibrateFromReport_MismatchedEmbedIdentityIsAnError and siblings).
const (
	testEmbedIdentity = "test/embed#t2:r2000:b0:pnone"
	testDimension     = 8
)

// TestCalibrateFromReport_MismatchedEmbedIdentityIsAnError is the codex
// round-4 FIX A negative pinning test: a report measured against ONE
// embedding identity, handed to CalibrateFromReport with a DIFFERENT target
// identity, must refuse -- not silently mint a recommendation from the
// wrong embedding space.
func TestCalibrateFromReport_MismatchedEmbedIdentityIsAnError(t *testing.T) {
	report := CalibrationReport{
		EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		Cases: []CalibrationCase{{Cause: "hit", CorrectSimilarity: floatPtr(0.5)}},
	}
	_, err := CalibrateFromReport(report, CalibrationOptions{
		TargetRecall: 0.90, TargetEmbedIdentity: "lmstudio/nomic-embed-text#t2:r2000:b0:pnomic", TargetDimension: 768,
	})
	if err != ErrEmbeddingIdentityMismatch {
		t.Fatalf("err = %v, want ErrEmbeddingIdentityMismatch (identity string differs)", err)
	}
}

// TestCalibrateFromReport_MismatchedDimensionIsAnError is the sibling case:
// the SAME identity string, but a different measured width -- must refuse
// exactly like a differing identity string does (round-3's dimension pin,
// enforced here on the artifact side too).
func TestCalibrateFromReport_MismatchedDimensionIsAnError(t *testing.T) {
	report := CalibrationReport{
		EmbedIdentity: testEmbedIdentity, EmbedDimension: 1536,
		Cases: []CalibrationCase{{Cause: "hit", CorrectSimilarity: floatPtr(0.5)}},
	}
	_, err := CalibrateFromReport(report, CalibrationOptions{
		TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension,
	})
	if err != ErrEmbeddingIdentityMismatch {
		t.Fatalf("err = %v, want ErrEmbeddingIdentityMismatch (dimension differs: 1536 vs %d)", err, testDimension)
	}
}

// TestCalibrateFromReport_AbsentReportEmbedIdentityIsAnError proves ABSENCE
// is not innocence: a report with no identity stamp at all (a pre-CHAOS-3834
// report, or a harness variant that omits the field) must refuse the SAME
// way a mismatch does, not "proceed since we can't prove it's wrong".
func TestCalibrateFromReport_AbsentReportEmbedIdentityIsAnError(t *testing.T) {
	report := CalibrationReport{
		// EmbedIdentity/EmbedDimension intentionally left zero.
		Cases: []CalibrationCase{{Cause: "hit", CorrectSimilarity: floatPtr(0.5)}},
	}
	_, err := CalibrateFromReport(report, CalibrationOptions{
		TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension,
	})
	if err != ErrEmbeddingIdentityMismatch {
		t.Fatalf("err = %v, want ErrEmbeddingIdentityMismatch (report carries no identity stamp)", err)
	}
}

// TestCalibrateFromReport_AbsentTargetEmbedIdentityIsAnError proves the
// caller side of the same rule: calibrating with no stated target identity
// is exactly the hazard this fix closes, not a supported "don't care" mode.
func TestCalibrateFromReport_AbsentTargetEmbedIdentityIsAnError(t *testing.T) {
	report := CalibrationReport{
		EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		Cases: []CalibrationCase{{Cause: "hit", CorrectSimilarity: floatPtr(0.5)}},
	}
	_, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90})
	if err != ErrEmbeddingIdentityMismatch {
		t.Fatalf("err = %v, want ErrEmbeddingIdentityMismatch (caller supplied no target identity)", err)
	}
}

// TestCalibrateFromReport_MatchingEmbedIdentityProceeds is the positive
// companion: identity AND dimension both agree, so CalibrateFromReport
// proceeds to a normal recommendation instead of refusing.
func TestCalibrateFromReport_MatchingEmbedIdentityProceeds(t *testing.T) {
	report := CalibrationReport{
		EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		Cases: []CalibrationCase{{Cause: "hit", CorrectSimilarity: floatPtr(0.5)}},
	}
	result, err := CalibrateFromReport(report, CalibrationOptions{
		TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension,
	})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v, want a normal recommendation (identity/dimension both match)", err)
	}
	if result.Policy.SimilarityFloor <= 0 {
		t.Fatalf("SimilarityFloor = %v, want a computed tau", result.Policy.SimilarityFloor)
	}
}

// TestCalibrateFromReport_JSONWithoutTruncatedFieldAssumesTruncated is the
// codex round-4 FIX B pinning test, at the JSON boundary specifically (the
// ruling's own framing: "JSON without the field"): a report JSON with NO
// hard_negatives_truncated key at all -- exactly what a pre-CHAOS-3834
// report (or this morning's v2 baseline) looks like -- must decode
// HardNegativesTruncated as nil, and CalibrateFromReport must treat nil as
// "assume truncated" for a saturated case, refusing K rather than silently
// trusting a capped list as complete (the pre-fix hazard a plain bool's
// false zero-value caused).
func TestCalibrateFromReport_JSONWithoutTruncatedFieldAssumesTruncated(t *testing.T) {
	raw := fmt.Sprintf(`{
		"embed_identity": %q, "embed_dimension": %d, "top_k": 5,
		"cases": [{"cause":"hit","correct_similarity":0.90,
			"hard_negatives":[
				{"kind":"k","canonical_id":"n1","similarity":0.95},
				{"kind":"k","canonical_id":"n2","similarity":0.92}
			]
		}]
	}`, testEmbedIdentity, testDimension)
	var report CalibrationReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if report.Cases[0].HardNegativesTruncated != nil {
		t.Fatalf("HardNegativesTruncated = %v, want nil -- the JSON carries no hard_negatives_truncated key at all", *report.Cases[0].HardNegativesTruncated)
	}
	result, err := CalibrateFromReport(report, CalibrationOptions{
		TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension,
	})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if result.KApplyReady {
		t.Fatal("KApplyReady = true, want false -- no completeness signal at all must be treated as truncated (worst case), not silently trusted as complete")
	}
	if result.KInsufficientDataNote == "" {
		t.Fatal("KInsufficientDataNote is empty, want a re-run explanation")
	}
}

// TestCalibrateFromReport_ExplicitFalseTruncatedIsTrustedAsComplete is the
// companion positive case: an EXPLICIT false (present, not absent) must
// still be trusted, exactly as round-2 P2 always intended -- FIX B changes
// only what absence means, not what an explicit value means.
func TestCalibrateFromReport_ExplicitFalseTruncatedIsTrustedAsComplete(t *testing.T) {
	report := CalibrationReport{
		EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 5,
		Cases: []CalibrationCase{{
			Cause: "hit", CorrectSimilarity: floatPtr(0.90),
			HardNegatives:          []CalibrationHardNegative{{Kind: "k", CanonicalID: "n1", Similarity: 0.95}, {Kind: "k", CanonicalID: "n2", Similarity: 0.92}},
			HardNegativesTruncated: boolPtr(false),
		}},
	}
	result, err := CalibrateFromReport(report, CalibrationOptions{
		TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension,
	})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !result.KApplyReady {
		t.Fatalf("KApplyReady = false, want true -- an EXPLICIT false completeness signal must be trusted even though every serialized entry clears tau")
	}
	if result.NearDuplicateP90 != 2 {
		t.Fatalf("NearDuplicateP90 = %d, want 2 (the explicitly-complete count)", result.NearDuplicateP90)
	}
}

// TestCalibrateFromReport_TruncatedSaturatedCaseWithMatchingTauSizesKFromTotal
// is the codex round-2 P2 fix's positive pinning test, TIGHTENED by codex
// round-3 P2 (see TestCalibrateFromReport_TruncatedSaturatedCaseWithMismatchedTauRefusesToSizeK
// for the sibling that proves the tightening): "truncated + total measured
// at the SAME tau this run recommends -> size K from the total". report.Tau
// is set to the EXACT tau recallGateThreshold computes for this fixture
// (single S+ sample 0.90, TargetRecall 0.90 -> tau = Nextafter(0.90, -Inf)),
// simulating a harness re-run AT the previously recommended floor.
func TestCalibrateFromReport_TruncatedSaturatedCaseWithMatchingTauSizesKFromTotal(t *testing.T) {
	wantTau, _ := recallGateThreshold([]float64{0.90}, 0.90)
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		TopK: 5,
		Tau:  wantTau,
		Cases: []CalibrationCase{{
			Cause:                     "hit",
			CorrectSimilarity:         floatPtr(0.90),
			HardNegatives:             []CalibrationHardNegative{{Kind: "k", CanonicalID: "n1", Similarity: 0.95}, {Kind: "k", CanonicalID: "n2", Similarity: 0.92}},
			HardNegativeAboveTauCount: intPtr(20),
			HardNegativesTruncated:    boolPtr(true),
		}},
	}
	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !result.KApplyReady {
		t.Fatalf("KApplyReady = false, want true -- report.Tau exactly matches the recommended tau, the total should be trusted")
	}
	if result.NearDuplicateP90 != 20 {
		t.Fatalf("NearDuplicateP90 = %d, want 20 (sized from HardNegativeAboveTauCount, not len(HardNegatives)=2)", result.NearDuplicateP90)
	}
	// multiplier = ceil((TopK + P90) / TopK) = ceil((5+20)/5) = 5.
	if result.Policy.OverFetchMultiplier != 5 {
		t.Fatalf("OverFetchMultiplier = %d, want 5 (sized from the total, not the capped list)", result.Policy.OverFetchMultiplier)
	}
}

// TestCalibrateFromReport_TruncatedSaturatedCaseWithMismatchedTauRefusesToSizeK
// is the codex round-3 P2 fix's pinning test: a total measured at a
// DIFFERENT tau than this run recommends must NOT be trusted, even though
// it is present -- ruling's exact scenario (report tau 0.55, recommended
// ~0.30-ish band). Negatives sitting between the two floors are invisible
// to a count taken at the higher report.Tau, so using it anyway would
// silently repeat the under-sizing bug this whole fix chain closes.
func TestCalibrateFromReport_TruncatedSaturatedCaseWithMismatchedTauRefusesToSizeK(t *testing.T) {
	sPlusValues := []float64{0.50, 0.10, 0.90, 0.30, 0.70, 0.20, 1.00, 0.40, 0.80, 0.60} // tau resolves to ~0.20 at 0.90 target
	var cases []CalibrationCase
	for i, s := range sPlusValues {
		c := CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s)}
		if i == 0 {
			c.HardNegatives = []CalibrationHardNegative{{Kind: "k", CanonicalID: "n1", Similarity: 0.95}, {Kind: "k", CanonicalID: "n2", Similarity: 0.92}}
			c.HardNegativeAboveTauCount = intPtr(20)
			c.HardNegativesTruncated = boolPtr(true)
		}
		cases = append(cases, c)
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 5, Tau: 0.55, Cases: cases} // report.Tau (0.55) != the ~0.20 tau this run recommends
	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if result.KApplyReady {
		t.Fatal("KApplyReady = true, want false -- the total was measured at report.Tau=0.55, which does not match the recommended tau (~0.20); trusting it anyway risks under-sizing K")
	}
	if result.Policy.OverFetchMultiplier != 0 {
		t.Fatalf("OverFetchMultiplier = %d, want 0 (refused, not silently sized from a mismatched-tau total)", result.Policy.OverFetchMultiplier)
	}
}

// TestCalibrateFromReport_TruncatedSaturatedCaseWithoutTotalRefusesToSizeK
// is the fix's negative pinning test: "truncated without a total -> refuse".
// Identical fixture to the positive test above, except
// HardNegativeAboveTauCount is nil (the harness did not provide a total --
// an older report, or a harness that skipped it). CalibrateFromReport must
// NOT silently size K from the saturated, capped list (that is exactly the
// pre-fix bug): it must report KApplyReady=false and force
// OverFetchMultiplier to 0 rather than a confident-looking but potentially
// under-sized number.
func TestCalibrateFromReport_TruncatedSaturatedCaseWithoutTotalRefusesToSizeK(t *testing.T) {
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		TopK: 5,
		Cases: []CalibrationCase{{
			Cause:                  "hit",
			CorrectSimilarity:      floatPtr(0.90),
			HardNegatives:          []CalibrationHardNegative{{Kind: "k", CanonicalID: "n1", Similarity: 0.95}, {Kind: "k", CanonicalID: "n2", Similarity: 0.92}},
			HardNegativesTruncated: boolPtr(true),
			// HardNegativeAboveTauCount intentionally nil.
		}},
	}
	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if result.KApplyReady {
		t.Fatal("KApplyReady = true, want false -- the harness reported truncation but no total; sizing K from the capped, saturated list would silently under-size it")
	}
	if result.Policy.OverFetchMultiplier != 0 {
		t.Fatalf("OverFetchMultiplier = %d, want 0 (forced to \"unchanged\" -- insufficient data, not a silent guess)", result.Policy.OverFetchMultiplier)
	}
	if result.KInsufficientDataNote == "" {
		t.Fatal("KInsufficientDataNote is empty, want a human-facing explanation when K sizing is refused")
	}
	// tau (the OTHER half of the policy) must be entirely unaffected by K's
	// refusal -- this gate is scoped to K alone.
	if result.Policy.SimilarityFloor <= 0 {
		t.Fatalf("SimilarityFloor = %v, want the recall-gate tau still computed normally", result.Policy.SimilarityFloor)
	}
}

// TestCalibrateFromReport_TruncatedUnsaturatedCaseNeedsNoTotal proves the
// "else" branch: a case can be truncated (the harness capped its serialized
// list) WITHOUT being saturated (at least one serialized entry already
// falls below tau) -- and dedupeHardNegatives sorts descending before
// capping, so everything beyond the cap is <= the smallest serialized
// entry. A not-saturated capped count is therefore ALREADY the exact total,
// truncated or not, and needs no HardNegativeAboveTauCount to be trusted.
func TestCalibrateFromReport_TruncatedUnsaturatedCaseNeedsNoTotal(t *testing.T) {
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		TopK: 5,
		Cases: []CalibrationCase{{
			Cause:             "hit",
			CorrectSimilarity: floatPtr(0.90),
			HardNegatives:     []CalibrationHardNegative{{Kind: "k", CanonicalID: "n1", Similarity: 0.95}, {Kind: "k", CanonicalID: "n2", Similarity: 0.10}},
			// n2 (0.10) falls below the ~0.90 tau this single-sample report
			// resolves to -- the capped list is NOT saturated.
			HardNegativesTruncated: boolPtr(true),
			// HardNegativeAboveTauCount intentionally nil -- must not be needed.
		}},
	}
	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !result.KApplyReady {
		t.Fatalf("KApplyReady = false, want true -- an unsaturated capped list is already exact, no total needed")
	}
	if result.NearDuplicateP90 != 1 {
		t.Fatalf("NearDuplicateP90 = %d, want 1 (only n1 clears tau; n2 is below it and provably nothing beyond the cap could clear tau either)", result.NearDuplicateP90)
	}
}

// TestCalibrateFromReport_DeterministicRecallGate locks in the exact
// nearest-rank arithmetic recallGateThreshold implements, against a small
// hand-computed example: S+ = {0.10..1.00 step 0.10} (given out of order,
// to prove the function sorts). At targetRecall=0.90, n=10,
// excludeCount=floor(0.10*10)=1, so tau must be the 2nd-smallest sample
// (0.20) and exactly 9/10 samples survive it.
func TestCalibrateFromReport_DeterministicRecallGate(t *testing.T) {
	cases := []CalibrationCase{}
	for _, s := range []float64{0.50, 0.10, 0.90, 0.30, 0.70, 0.20, 1.00, 0.40, 0.80, 0.60} {
		cases = append(cases, CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s)})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: cases}

	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !almostEqual(result.Policy.SimilarityFloor, 0.20) {
		t.Fatalf("SimilarityFloor = %v, want 0.20", result.Policy.SimilarityFloor)
	}
	if !almostEqual(result.AchievedRecall, 0.90) {
		t.Fatalf("AchievedRecall = %v, want 0.90", result.AchievedRecall)
	}
	if result.SPlusSampleSize != 10 {
		t.Fatalf("SPlusSampleSize = %d, want 10", result.SPlusSampleSize)
	}
}

// TestCalibrateFromReport_TargetRecallJustAboveExactBoundaryStillPreservesTheInvariant
// is the codex round-6 P3 pinning test: the SAME 10-sample fixture as
// TestCalibrateFromReport_DeterministicRecallGate above (unchanged, proving
// the fix does not disturb the exact-0.90 case), but TargetRecall is
// 0.90000000001 -- a hair above the clean boundary, by construction, not by
// float64 rounding noise. The predecessor's fixed quantileEpsilon (1e-9,
// far coarser than genuine ~1e-16 float64 noise at this magnitude) treated
// that hair as noise and rounded it away, excluding one MORE sample than
// requested and returning AchievedRecall=0.90 < TargetRecall=0.90000000001
// -- a real violation of the function's own documented invariant
// (AchievedRecall >= TargetRecall, always). This pins that the invariant
// now holds.
func TestCalibrateFromReport_TargetRecallJustAboveExactBoundaryStillPreservesTheInvariant(t *testing.T) {
	cases := []CalibrationCase{}
	for _, s := range []float64{0.50, 0.10, 0.90, 0.30, 0.70, 0.20, 1.00, 0.40, 0.80, 0.60} {
		cases = append(cases, CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s)})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: cases}
	const targetRecall = 0.90000000001

	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: targetRecall, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if result.AchievedRecall < targetRecall {
		t.Fatalf("AchievedRecall = %v, want >= TargetRecall %v -- the documented invariant must hold even when the target sits a hair above a clean decimal boundary", result.AchievedRecall, targetRecall)
	}
}

// TestCalibrateFromReport_TargetRecallOneULPAboveExactBoundaryStillPreservesTheInvariant
// is the codex round-7 P2 pinning test -- the SAME 10-sample fixture again,
// TargetRecall = math.Nextafter(0.9, +Inf) this time (one float64 ULP above
// 0.9, not the round-6 test's ~1e-11-scale gap). The round-6 fix
// (snapNearIntegerBoundary, an 8-ULP tolerance window) OVERCORRECTED for
// exactly this shape: (1-target)*10 lands a few ULPs BELOW 1.0 for
// perfectly legitimate reasons (target is genuinely, if infinitesimally,
// more demanding than 0.9), but the snap's tolerance window treated that as
// noise and rounded it UP to 1.0 anyway, excluding one sample it should not
// have and reproducing the exact same class of invariant violation from a
// different direction. The round-7 fix removed all epsilon/ULP prediction
// in favor of post-hoc measurement, which this pins.
func TestCalibrateFromReport_TargetRecallOneULPAboveExactBoundaryStillPreservesTheInvariant(t *testing.T) {
	cases := []CalibrationCase{}
	for _, s := range []float64{0.50, 0.10, 0.90, 0.30, 0.70, 0.20, 1.00, 0.40, 0.80, 0.60} {
		cases = append(cases, CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s)})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: cases}
	targetRecall := math.Nextafter(0.9, math.Inf(1))

	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: targetRecall, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if result.AchievedRecall < targetRecall {
		t.Fatalf("AchievedRecall = %v, want >= TargetRecall %v -- the documented invariant must hold even when the target sits one ULP above a clean decimal boundary", result.AchievedRecall, targetRecall)
	}
}

// TestRecallGateThreshold_InvariantHoldsAcrossRandomizedTargets is the
// codex round-7 P2 property-style trio member: dozens of randomized
// (sample-count, TargetRecall) pairs, asserting recallGateThreshold's
// documented invariant (AchievedRecall >= TargetRecall) holds for every
// one -- not just the three specific boundary values the other tests in
// this trio pin by hand. A fixed seed keeps the run deterministic.
func TestRecallGateThreshold_InvariantHoldsAcrossRandomizedTargets(t *testing.T) {
	rng := rand.New(rand.NewSource(20260815))
	for trial := 0; trial < 50; trial++ {
		n := 1 + rng.Intn(30)
		samples := make([]float64, n)
		for i := range samples {
			samples[i] = rng.Float64()
		}
		target := rng.Float64()
		if target <= 0 {
			target += 1e-9
		}
		_, achievedRecall := recallGateThreshold(samples, target)
		if achievedRecall < target {
			t.Fatalf("trial %d (n=%d, target=%v): achievedRecall=%v -- AchievedRecall must always be >= TargetRecall", trial, n, target, achievedRecall)
		}
	}
}

// A single S+ sample is its own tau, with achieved recall trivially 1.0.
func TestCalibrateFromReport_SingleSample(t *testing.T) {
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: []CalibrationCase{{Cause: "hit", CorrectSimilarity: floatPtr(0.42)}}}
	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !almostEqual(result.Policy.SimilarityFloor, 0.42) {
		t.Fatalf("SimilarityFloor = %v, want 0.42", result.Policy.SimilarityFloor)
	}
	if !almostEqual(result.AchievedRecall, 1.0) {
		t.Fatalf("AchievedRecall = %v, want 1.0", result.AchievedRecall)
	}
}

// TestCalibrateFromReport_HundredPercentTargetIsActuallyAchievable pins the
// codex round-1 P1 fix directly: a 100% TargetRecall must produce a tau that
// production's STRICT aboveSimilarityFloor predicate (similarity > tau, not
// >=) actually retrieves every sample against, not one that claims 100% while
// silently dropping the boundary sample. Before the fix, tau was set to the
// exact minimum S+ sample; that sample could never clear its own floor in
// production (X > X is always false), so a "100%" recommendation was
// provably unachievable -- the bug's own description ("9/10 claimed vs 8/10
// actual"). This asserts both halves of the fix: the reported AchievedRecall
// is honest (every sample genuinely clears the returned tau under the SAME
// predicate production uses), and the recall guarantee still holds (100%
// target really does deliver 100%, not "100% claimed, 90% delivered").
func TestCalibrateFromReport_HundredPercentTargetIsActuallyAchievable(t *testing.T) {
	samples := []float64{0.10, 0.20, 0.30, 0.40, 0.50, 0.60, 0.70, 0.80, 0.90, 1.00}
	cases := make([]CalibrationCase, len(samples))
	for i, s := range samples {
		cases[i] = CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s)}
	}
	result, err := CalibrateFromReport(CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: cases}, CalibrationOptions{TargetRecall: 1.0, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !almostEqual(result.AchievedRecall, 1.0) {
		t.Fatalf("AchievedRecall = %v, want 1.0 (100%% target must actually deliver 100%%)", result.AchievedRecall)
	}
	// The pinned boundary check: the minimum sample (0.10, this report's tau
	// candidate) must genuinely clear the returned tau under production's
	// OWN predicate, not merely be numerically close to it.
	min := samples[0]
	if !aboveSimilarityFloor(min, result.Policy.SimilarityFloor) {
		t.Fatalf("aboveSimilarityFloor(%v, tau=%v) = false, want true -- the sample AchievedRecall counts as recalled must actually be retrievable in production, not just claimed", min, result.Policy.SimilarityFloor)
	}
	// And the boundary sample itself, handed straight to tau, must NOT read
	// as recalled -- the exact codex round-1 P1 regression this test exists
	// to catch (a tau equal to a real sample can never retrieve that sample).
	if aboveSimilarityFloor(min, min) {
		t.Fatal("aboveSimilarityFloor(min, min) = true, want false -- a sample exactly at tau is never counted as recalled")
	}
}

func TestCalibrateFromReport_NoCorrectSimilaritySamplesIsAnError(t *testing.T) {
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: []CalibrationCase{
		{Cause: "subject_missing"},
		{Cause: "vector_missing"},
	}}
	if _, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}); err != ErrNoCorrectSimilaritySamples {
		t.Fatalf("err = %v, want ErrNoCorrectSimilaritySamples", err)
	}
}

// TestCalibrateFromReport_NonPositiveFloorIsAnError is the codex round-3 P2
// fix's pinning test: a 100% TargetRecall against a genuinely low/negative
// S+ sample (the oracle harness's trueCosineSimilarity is UNCLAMPED, unlike
// production's CosineFromDistance -- see ErrNoFeasibleFloor's doc comment)
// forces tau to or below 0, a value floorApplicable -- the SAME predicate
// EmbedderFromEnv gates a calibrated SimilarityFloor on -- would silently
// refuse to ever apply. CalibrateFromReport must return ErrNoFeasibleFloor
// here, never a CalibrationResult that looks like a usable recommendation.
func TestCalibrateFromReport_NonPositiveFloorIsAnError(t *testing.T) {
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: []CalibrationCase{
		{Cause: "hit", CorrectSimilarity: floatPtr(-0.05)},
		{Cause: "hit", CorrectSimilarity: floatPtr(0.50)},
	}}
	_, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 1.0, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != ErrNoFeasibleFloor {
		t.Fatalf("err = %v, want ErrNoFeasibleFloor (S+ sample -0.05 with a 100%% recall target forces tau <= 0)", err)
	}
}

// TestCalibrateFromReport_ZeroFloorIsAnError is the boundary case: an S+
// sample of EXACTLY 0.0 also forces tau <= 0 via the Nextafter nudge
// (tau ends up a tiny negative value strictly below the sample), so this
// must fail the SAME way a negative sample does, not silently round up to
// something floorApplicable would accept.
func TestCalibrateFromReport_ZeroFloorIsAnError(t *testing.T) {
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: []CalibrationCase{
		{Cause: "hit", CorrectSimilarity: floatPtr(0.0)},
	}}
	_, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 1.0, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != ErrNoFeasibleFloor {
		t.Fatalf("err = %v, want ErrNoFeasibleFloor (a single S+ sample of exactly 0.0 forces tau <= 0)", err)
	}
}

// TestCalibrateFromReport_InvalidTargetRecallIsAnError is the single
// validation-site test for TargetRecall (codex round-7 P2 FIX C extends this
// existing table rather than adding a parallel test, per the ruling's
// "single validation site" -- one check, one test covering everything that
// check must reject). NaN and the two infinities are the round-7 additions:
// strconv.ParseFloat accepts the literal string "NaN"/"Inf"/"-Inf" from an
// env var, and NaN in particular fails BOTH a bare `<= 0` and `> 1` range
// check (every comparison against NaN is false), so a range-only guard would
// have silently let it through into rank arithmetic downstream.
func TestCalibrateFromReport_InvalidTargetRecallIsAnError(t *testing.T) {
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: []CalibrationCase{{CorrectSimilarity: floatPtr(0.5)}}}
	for _, target := range []float64{0, -0.1, 1.1, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: target, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}); err != ErrInvalidTargetRecall {
			t.Fatalf("TargetRecall=%v: err = %v, want ErrInvalidTargetRecall", target, err)
		}
	}
}

// TestCalibrateFromReport_EmptyNegativePoolIsUnmeasuredNotApplyReady is the
// codex round-6 P2 pinning test: a report with valid S+ samples but ZERO
// negative measurements anywhere (no case sets BestWrongSimilarity, no case
// carries HardNegatives) must NOT vacuously pass the negative gate.
// rejectRate's own vacuous-truth default (1.0, "nothing to reject")
// unguarded would clear NegativeGateRejectThreshold trivially -- this pins
// that ApplyReady is false and the note explicitly says UNMEASURED, distinct
// from a gate that ran and failed.
func TestCalibrateFromReport_EmptyNegativePoolIsUnmeasuredNotApplyReady(t *testing.T) {
	var cases []CalibrationCase
	for _, s := range []float64{0.50, 0.10, 0.90, 0.30, 0.70, 0.20, 1.00, 0.40, 0.80, 0.60} {
		cases = append(cases, CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s)})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, Cases: cases}
	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if result.ApplyReady {
		t.Fatal("ApplyReady = true, want false -- an empty negative pool means the gate was never measured, not that it passed")
	}
	if !almostEqual(result.HardNegativeRejectRate, 1.0) {
		t.Fatalf("HardNegativeRejectRate = %v, want the vacuous 1.0 (unchanged numeric default; ApplyReady, not this field, is what must reflect UNMEASURED)", result.HardNegativeRejectRate)
	}
	if !strings.Contains(result.NegativeGateNote, "UNMEASURED") {
		t.Fatalf("NegativeGateNote = %q, want it to explicitly say UNMEASURED (distinct from a measured-and-failed gate)", result.NegativeGateNote)
	}
}

// TestCalibrateFromReport_SyntheticOverlappingDistributions shapes its
// fixture after the CHAOS-3834 measurement aggregates this lane was given
// (first full-universe oracle baseline, identity
// openai/text-embedding-3-large#t2:r2000:b0:pnone, top-20, 30 scored
// cases): S+ and S- OVERLAP throughout the [0.20, 0.65] band rather than
// separating at any tau -- floor=0.55 rejected the correct subject in the
// large majority of cases, and tau=0.30 passed roughly 24/30 correct
// pairs. This test proves CalibrateFromReport, run against a
// similarly-shaped synthetic distribution, lands in the SAME low
// recall-gate band the measurement's own aggregates support, rather than
// reproducing the old precision-cliff default.
func TestCalibrateFromReport_SyntheticOverlappingDistributions(t *testing.T) {
	// 30 cases. S+ ranges roughly 0.20-0.65 (mirrors the measured S+
	// medians of 0.39-0.53 across kinds); S- is drawn from an OVERLAPPING
	// range, 0.23-0.68, so no tau cleanly separates the two -- the
	// defining property of the real measurement this fixture is shaped
	// after.
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		sPlus := 0.20 + float64(i)*(0.45/29.0)
		sMinus := 0.23 + float64(i)*(0.45/29.0)
		cases = append(cases, CalibrationCase{
			Cause:               "floor_loss",
			CorrectSimilarity:   floatPtr(sPlus),
			BestWrongSimilarity: floatPtr(sMinus),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}

	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	t.Logf("synthetic overlapping-distribution recommendation: tau=%.4f K=%d achievedRecall=%.4f hardNegRejectRate=%.4f",
		result.Policy.SimilarityFloor, result.Policy.OverFetchMultiplier, result.AchievedRecall, result.HardNegativeRejectRate)

	// The measurement's own aggregates support a tau in roughly the 0.20-0.40
	// band (tau=0.30 passed 24/30 correct pairs in the real run) -- assert
	// the recommendation lands in that band, strictly below the old
	// precision-cliff default (0.55), and never at/above it.
	if result.Policy.SimilarityFloor <= 0 || result.Policy.SimilarityFloor >= 0.55 {
		t.Fatalf("SimilarityFloor = %v, want a low recall-gate value strictly below the old 0.55 default", result.Policy.SimilarityFloor)
	}
	if result.AchievedRecall < 0.90 {
		t.Fatalf("AchievedRecall = %v, want >= the 0.90 target", result.AchievedRecall)
	}
	if result.SMinusSampleSize != 30 {
		t.Fatalf("SMinusSampleSize = %d, want 30", result.SMinusSampleSize)
	}
	// codex round-2 P1 pinning: this fixture's S+/S- ranges OVERLAP by
	// construction (the defining property it is shaped after), so the
	// recall-gate tau above admits most impostors too -- the shipped
	// openai/text-embedding-3-large entry's own real measurement has a
	// reject rate of ~0.067, far below NegativeGateRejectThreshold. The
	// tool must NOT silently bless this as an apply-ready policy.
	if result.ApplyReady {
		t.Fatalf("ApplyReady = true, want false: an overlapping S+/S- distribution's reject rate (%.4f) must fail the negative gate, not silently pass", result.HardNegativeRejectRate)
	}
	if result.NegativeGateNote == "" {
		t.Fatal("NegativeGateNote is empty, want a human-facing explanation when the gate fails")
	}
}

// TestCalibrateFromReport_WellSeparatedDistributionPassesTheNegativeGate is
// the codex round-2 P1 fix's companion positive case: an S+/S- distribution
// that DOES cleanly separate at the recommended tau (no overlap at all --
// every S- sits well below every S+) must report ApplyReady=true. This is
// NOT a claim that CHAOS-3834's real measured identity looks like this --
// it proves the gate itself is a genuine two-sided check, not a constant
// false dressed up as fail-closed.
func TestCalibrateFromReport_WellSeparatedDistributionPassesTheNegativeGate(t *testing.T) {
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		// S+ high (0.80-0.95), S- low (0.05-0.20) -- no overlap at any tau.
		sPlus := 0.80 + float64(i)*(0.15/29.0)
		sMinus := 0.05 + float64(i)*(0.15/29.0)
		cases = append(cases, CalibrationCase{
			Cause: "hit", CorrectSimilarity: floatPtr(sPlus), BestWrongSimilarity: floatPtr(sMinus),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}

	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	t.Logf("well-separated recommendation: tau=%.4f achievedRecall=%.4f hardNegRejectRate=%.4f",
		result.Policy.SimilarityFloor, result.AchievedRecall, result.HardNegativeRejectRate)
	if !result.ApplyReady {
		t.Fatalf("ApplyReady = false, want true: reject rate %.4f should clear the %.4f threshold on a cleanly-separated distribution", result.HardNegativeRejectRate, NegativeGateRejectThreshold)
	}
	if result.NegativeGateNote == "" {
		t.Fatal("NegativeGateNote is empty, want a human-facing explanation even when the gate passes")
	}
}

// TestCalibrateFromReport_OverFetchRespondsToNearDuplicateDensity proves K
// tracks the hard-negative density AT THE RECOMMENDED TAU: a report whose
// cases carry many hard negatives clustered above tau recommends a
// multiplier > 1, while a report with no such crowding recommends 0
// ("unchanged").
func TestCalibrateFromReport_OverFetchRespondsToNearDuplicateDensity(t *testing.T) {
	sPlusValues := []float64{0.50, 0.10, 0.90, 0.30, 0.70, 0.20, 1.00, 0.40, 0.80, 0.60}

	t.Run("dense near-duplicates recommend a wider multiplier", func(t *testing.T) {
		var cases []CalibrationCase
		for i, s := range sPlusValues {
			var negatives []CalibrationHardNegative
			// Case i carries i hard negatives, all comfortably above the
			// tau this fixture resolves to (0.20 -- see the deterministic
			// test above for the derivation).
			for n := 0; n < i; n++ {
				negatives = append(negatives, CalibrationHardNegative{Kind: "work_item", CanonicalID: "wi", Similarity: 0.25})
			}
			// HardNegativesTruncated explicitly false (codex round-4 FIX B):
			// this fixture's per-case negatives ARE the complete harvest by
			// construction, and the density math this test pins is
			// orthogonal to the completeness gate -- an explicit false, not
			// an absent field, is what "known complete" looks like now that
			// nil means "assume truncated".
			cases = append(cases, CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s), HardNegatives: negatives, HardNegativesTruncated: boolPtr(false)})
		}
		report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}
		result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
		if err != nil {
			t.Fatalf("CalibrateFromReport: %v", err)
		}
		// nearDupCounts sorted ascending = 0..9; conventional 90th
		// percentile (rank=ceil(0.9*10)=9, index 8) = 8. multiplier =
		// ceil((20+8)/20) = 2.
		if result.NearDuplicateP90 != 8 {
			t.Fatalf("NearDuplicateP90 = %d, want 8", result.NearDuplicateP90)
		}
		if result.Policy.OverFetchMultiplier != 2 {
			t.Fatalf("OverFetchMultiplier = %d, want 2", result.Policy.OverFetchMultiplier)
		}
	})

	t.Run("no near-duplicates recommend unchanged (0)", func(t *testing.T) {
		var cases []CalibrationCase
		for _, s := range sPlusValues {
			cases = append(cases, CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s)})
		}
		report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}
		result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
		if err != nil {
			t.Fatalf("CalibrateFromReport: %v", err)
		}
		if result.Policy.OverFetchMultiplier != 0 {
			t.Fatalf("OverFetchMultiplier = %d, want 0 (unchanged)", result.Policy.OverFetchMultiplier)
		}
	})
}

// TestCalibrateFromReport_HardNegativeRejectRate proves the L4 test
// criterion's other half ("hard negatives fall below tau") is actually
// computed: hard negatives placed entirely below the resolved tau reject
// at 100%; hard negatives placed entirely above it reject at 0%.
func TestCalibrateFromReport_HardNegativeRejectRate(t *testing.T) {
	sPlusValues := []float64{0.50, 0.10, 0.90, 0.30, 0.70, 0.20, 1.00, 0.40, 0.80, 0.60} // tau resolves to 0.20 at 0.90 target

	build := func(negativeSimilarity float64) CalibrationReport {
		var cases []CalibrationCase
		for _, s := range sPlusValues {
			cases = append(cases, CalibrationCase{
				Cause: "hit", CorrectSimilarity: floatPtr(s),
				HardNegatives: []CalibrationHardNegative{{Kind: "k", CanonicalID: "id", Similarity: negativeSimilarity}},
			})
		}
		return CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}
	}

	below, err := CalibrateFromReport(build(0.05), CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !almostEqual(below.HardNegativeRejectRate, 1.0) {
		t.Fatalf("HardNegativeRejectRate = %v, want 1.0 for negatives entirely below tau", below.HardNegativeRejectRate)
	}

	above, err := CalibrateFromReport(build(0.99), CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !almostEqual(above.HardNegativeRejectRate, 0.0) {
		t.Fatalf("HardNegativeRejectRate = %v, want 0.0 for negatives entirely above tau", above.HardNegativeRejectRate)
	}
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// fakeRunnerT is a MINIMAL runnerT double: it records whether Fatalf was
// called and panics with fakeRunnerTFatal instead of calling
// runtime.Goexit() the way the real *testing.T does -- runFakeCalibrationRunner
// below recovers exactly that sentinel, so execution stops at the Fatalf
// call site (matching real behavior: the artifact-writing code after it
// never runs) WITHOUT the failure propagating to the actual *testing.T
// running this test (see runnerT's doc comment for why a real t.Run
// subtest can't be used to observe this).
type fakeRunnerT struct {
	failed bool
	logs   []string
}

func (f *fakeRunnerT) Helper() {}
func (f *fakeRunnerT) Logf(format string, args ...interface{}) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

type fakeRunnerTFatal struct{}

func (f *fakeRunnerT) Fatalf(format string, args ...interface{}) {
	f.failed = true
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
	panic(fakeRunnerTFatal{})
}

// runFakeCalibrationRunner runs runCalibrationRunner against a fakeRunnerT
// and returns whether it failed (called Fatalf) -- true if the run halted
// at a Fatalf the way a real failing *testing.T would. reportPath is empty
// for every EXISTING caller (they build `report` directly in memory, no
// source file to collide with outputPath) -- see
// runFakeCalibrationRunnerWithReportPath for the codex round-7 P2 tests
// that specifically need a real reportPath.
func runFakeCalibrationRunner(t *testing.T, report CalibrationReport, opts CalibrationOptions, outputPath string) *fakeRunnerT {
	t.Helper()
	return runFakeCalibrationRunnerWithReportPath(t, report, opts, "", outputPath)
}

// runFakeCalibrationRunnerWithReportPath is runFakeCalibrationRunner with an
// explicit reportPath, for tests that need to exercise the
// input-path/output-path collision check (codex round-7 P2). reportBytes is
// nil -- see runFakeCalibrationRunnerWithReportPathAndBytes for the
// CHAOS-3852 test that specifically needs real on-disk bytes.
func runFakeCalibrationRunnerWithReportPath(t *testing.T, report CalibrationReport, opts CalibrationOptions, reportPath, outputPath string) *fakeRunnerT {
	t.Helper()
	return runFakeCalibrationRunnerWithReportPathAndBytes(t, report, opts, reportPath, nil, outputPath)
}

// runFakeCalibrationRunnerWithReportPathAndBytes is
// runFakeCalibrationRunnerWithReportPath with an explicit reportBytes,
// mirroring margin_calibration_test.go's identical need to hand
// runMarginCalibrationRunner the real on-disk bytes (CHAOS-3852, porting
// codex r8 O3's fix to this tool).
func runFakeCalibrationRunnerWithReportPathAndBytes(t *testing.T, report CalibrationReport, opts CalibrationOptions, reportPath string, reportBytes []byte, outputPath string) *fakeRunnerT {
	t.Helper()
	fake := &fakeRunnerT{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fakeRunnerTFatal); !ok {
					panic(r) // not ours -- a genuine bug, must not be swallowed
				}
			}
		}()
		runCalibrationRunner(fake, report, opts, reportPath, reportBytes, outputPath)
	}()
	return fake
}

// TestCalibrationRunner_GateFailingReportFailsAndWritesNoArtifact is the
// codex round-4 FIX C pinning test: a report that fails a readiness gate
// must make runCalibrationRunner call Fatalf (not merely log a warning and
// continue), and must NOT write anything to the requested output path.
// Fixture: the SyntheticOverlappingDistributions shape, whose reject rate
// (~0.067) is known to be far below NegativeGateRejectThreshold
// (ApplyReady=false).
func TestCalibrationRunner_GateFailingReportFailsAndWritesNoArtifact(t *testing.T) {
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		sPlus := 0.20 + float64(i)*(0.45/29.0)
		sMinus := 0.23 + float64(i)*(0.45/29.0)
		cases = append(cases, CalibrationCase{
			Cause: "floor_loss", CorrectSimilarity: floatPtr(sPlus), BestWrongSimilarity: floatPtr(sMinus),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}
	opts := CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}
	outputPath := filepath.Join(t.TempDir(), "policy.json")

	fake := runFakeCalibrationRunner(t, report, opts, outputPath)
	if !fake.failed {
		t.Fatalf("runCalibrationRunner did not call Fatalf, want it to -- this fixture's negative gate is known to fail (ApplyReady=false); logs: %v", fake.logs)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output file exists at %s (err=%v), want NONE written on a failing gate", outputPath, err)
	}
}

// TestCalibrationRunner_GatePassingReportWritesArtifact is the companion
// positive case: a report that clears BOTH readiness gates makes
// runCalibrationRunner complete WITHOUT calling Fatalf, and writes the
// artifact to the requested path. Fixture: the WellSeparatedDistribution
// shape (reject rate 1.0, clears NegativeGateRejectThreshold).
func TestCalibrationRunner_GatePassingReportWritesArtifact(t *testing.T) {
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		sPlus := 0.80 + float64(i)*(0.15/29.0)
		sMinus := 0.05 + float64(i)*(0.15/29.0)
		cases = append(cases, CalibrationCase{
			Cause: "hit", CorrectSimilarity: floatPtr(sPlus), BestWrongSimilarity: floatPtr(sMinus),
			// HardNegativesTruncated explicitly false (codex round-5 FIX B's
			// exhaustive table): a real harness ALWAYS sets this via
			// summarizeHardNegatives, so a case with zero hard negatives is
			// stamped complete, not left absent. An absent field here would
			// now correctly resolve to "unsafe, unknown completeness" under
			// the round-5 table -- this fixture wants "clears both gates",
			// so it must look like real harness output, not a legacy report.
			HardNegativesTruncated: boolPtr(false),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}
	opts := CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}
	outputPath := filepath.Join(t.TempDir(), "policy.json")

	// Pre-place a stale marker (codex round-6 P2): a PASSING run must
	// overwrite it with fresh content, not merely leave SOME file present.
	const staleMarker = `{"stale": true}`
	if err := os.WriteFile(outputPath, []byte(staleMarker), 0o600); err != nil {
		t.Fatalf("pre-place stale artifact: %v", err)
	}

	fake := runFakeCalibrationRunner(t, report, opts, outputPath)
	if fake.failed {
		t.Fatalf("runCalibrationRunner called Fatalf, want it to complete -- this fixture clears both readiness gates; logs: %v", fake.logs)
	}
	written, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("output file missing at %s, want it written when both gates pass: %v", outputPath, err)
	}
	if string(written) == staleMarker {
		t.Fatalf("output file at %s still holds the pre-placed stale marker, want it overwritten with a fresh result", outputPath)
	}
}

// TestCalibrationRunner_StaleArtifactIsRemovedOnAFailingRun is the codex
// round-6 P2 pinning test: a PRE-EXISTING artifact at outputPath (left
// behind by an earlier, unrelated successful run) must be removed before a
// later gate-failing run's Fatalf -- otherwise a downstream consumer
// reading outputPath would see that STALE file and mistake it for the
// current (failed) run's output, defeating the round-4 FIX C file-presence
// contract ("never a blessed-looking artifact for a run this tool itself
// would not bless") entirely.
func TestCalibrationRunner_StaleArtifactIsRemovedOnAFailingRun(t *testing.T) {
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		sPlus := 0.20 + float64(i)*(0.45/29.0)
		sMinus := 0.23 + float64(i)*(0.45/29.0)
		cases = append(cases, CalibrationCase{
			Cause: "floor_loss", CorrectSimilarity: floatPtr(sPlus), BestWrongSimilarity: floatPtr(sMinus),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}
	opts := CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}
	outputPath := filepath.Join(t.TempDir(), "policy.json")

	// Pre-place an artifact, as if left behind by an earlier, successful
	// run at this same path.
	if err := os.WriteFile(outputPath, []byte(`{"policy": "from an earlier successful run"}`), 0o600); err != nil {
		t.Fatalf("pre-place stale artifact: %v", err)
	}

	fake := runFakeCalibrationRunner(t, report, opts, outputPath)
	if !fake.failed {
		t.Fatalf("runCalibrationRunner did not call Fatalf, want it to -- this fixture's negative gate is known to fail; logs: %v", fake.logs)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("stale artifact still exists at %s (err=%v) after a failing run, want it removed -- a downstream reader must never see a prior run's file as if it were this run's output", outputPath, err)
	}
}

// TestCalibrationRunner_SameReportAndOutputPathIsRefused is the codex
// round-7 P2 pinning test: ACR_TEST_CALIBRATION_REPORT and the output path
// resolving to the SAME file must refuse to run at all -- round-6's
// stale-artifact removal would otherwise DELETE THE SOURCE REPORT before
// ever using it, and a gate-passing run would silently overwrite it with
// the policy JSON either way. Fixture deliberately clears BOTH readiness
// gates (it would otherwise succeed and overwrite the file), so this test
// is specifically about the collision check firing, not incidentally
// riding along on an unrelated gate failure. The report file's content
// must be BYTE-IDENTICAL afterward -- "refused" means untouched, not
// "removed then not replaced".
func TestCalibrationRunner_SameReportAndOutputPathIsRefused(t *testing.T) {
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		sPlus := 0.80 + float64(i)*(0.15/29.0)
		sMinus := 0.05 + float64(i)*(0.15/29.0)
		cases = append(cases, CalibrationCase{
			Cause: "hit", CorrectSimilarity: floatPtr(sPlus), BestWrongSimilarity: floatPtr(sMinus),
			HardNegativesTruncated: boolPtr(false),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}
	opts := CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}

	sharedPath := filepath.Join(t.TempDir(), "report.json")
	originalContent, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report): %v", err)
	}
	if err := os.WriteFile(sharedPath, originalContent, 0o600); err != nil {
		t.Fatalf("write the source report file: %v", err)
	}

	fake := runFakeCalibrationRunnerWithReportPath(t, report, opts, sharedPath, sharedPath)
	if !fake.failed {
		t.Fatalf("runCalibrationRunner did not call Fatalf, want it to refuse when report path == output path; logs: %v", fake.logs)
	}
	after, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatalf("source report file missing at %s after a refused run, want it UNTOUCHED: %v", sharedPath, err)
	}
	if string(after) != string(originalContent) {
		t.Fatalf("source report file at %s was modified by a refused run, want it byte-identical to what was written before the call", sharedPath)
	}
}

// TestCalibrationRunner_DifferentReportAndOutputPathStillWorks is the
// companion negative-of-the-negative case: a NORMAL run (report path and
// output path genuinely different, the common case every other test in
// this file already exercises) must not be affected by the round-7 P2
// collision check at all -- it only fires on an actual match.
func TestCalibrationRunner_DifferentReportAndOutputPathStillWorks(t *testing.T) {
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		sPlus := 0.80 + float64(i)*(0.15/29.0)
		sMinus := 0.05 + float64(i)*(0.15/29.0)
		cases = append(cases, CalibrationCase{
			Cause: "hit", CorrectSimilarity: floatPtr(sPlus), BestWrongSimilarity: floatPtr(sMinus),
			HardNegativesTruncated: boolPtr(false),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Cases: cases}
	opts := CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}

	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	outputPath := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(reportPath, []byte(`{"irrelevant": "source content, never re-read"}`), 0o600); err != nil {
		t.Fatalf("write the source report file: %v", err)
	}

	fake := runFakeCalibrationRunnerWithReportPath(t, report, opts, reportPath, outputPath)
	if fake.failed {
		t.Fatalf("runCalibrationRunner called Fatalf, want it to complete -- report path and output path are genuinely different; logs: %v", fake.logs)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("output file missing at %s: %v", outputPath, err)
	}
}

// TestCalibrationRunner_WrittenArtifactRoundTripsProvenance is the codex
// round-7 P2 FIX D pinning test: the artifact runCalibrationRunner writes to
// outputPath must be a CalibrationArtifact carrying full provenance
// (identity, dimension, target recall, the source report's own tau, and the
// source report's path/content hash) that survives a JSON round-trip --
// not a bare CalibrationResult, which carries none of it and would let a
// file from one embedding space or TargetRecall silently pass as another's.
func TestCalibrationRunner_WrittenArtifactRoundTripsProvenance(t *testing.T) {
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		sPlus := 0.80 + float64(i)*(0.15/29.0)
		sMinus := 0.05 + float64(i)*(0.15/29.0)
		cases = append(cases, CalibrationCase{
			Cause: "hit", CorrectSimilarity: floatPtr(sPlus), BestWrongSimilarity: floatPtr(sMinus),
			HardNegativesTruncated: boolPtr(false),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Tau: 0.55, Cases: cases}
	opts := CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}

	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	outputPath := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(reportPath, []byte(`{"irrelevant": "source content, never re-read"}`), 0o600); err != nil {
		t.Fatalf("write the source report file: %v", err)
	}

	fake := runFakeCalibrationRunnerWithReportPath(t, report, opts, reportPath, outputPath)
	if fake.failed {
		t.Fatalf("runCalibrationRunner called Fatalf, want it to complete; logs: %v", fake.logs)
	}

	written, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read written artifact at %s: %v", outputPath, err)
	}
	var artifact CalibrationArtifact
	if err := json.Unmarshal(written, &artifact); err != nil {
		t.Fatalf("json.Unmarshal(written artifact): %v", err)
	}

	if artifact.TargetEmbedIdentity != testEmbedIdentity {
		t.Errorf("TargetEmbedIdentity = %q, want %q", artifact.TargetEmbedIdentity, testEmbedIdentity)
	}
	if artifact.TargetDimension != testDimension {
		t.Errorf("TargetDimension = %d, want %d", artifact.TargetDimension, testDimension)
	}
	if artifact.TargetRecall != 0.90 {
		t.Errorf("TargetRecall = %v, want 0.90", artifact.TargetRecall)
	}
	if artifact.ReportTau != report.Tau {
		t.Errorf("ReportTau = %v, want the source report's own tau %v", artifact.ReportTau, report.Tau)
	}
	if artifact.SourceReportPath != reportPath {
		t.Errorf("SourceReportPath = %q, want %q", artifact.SourceReportPath, reportPath)
	}
	wantEncoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report): %v", err)
	}
	wantSum := sha256.Sum256(wantEncoded)
	wantHash := hex.EncodeToString(wantSum[:])
	if artifact.SourceReportSHA256 != wantHash {
		t.Errorf("SourceReportSHA256 = %q, want %q (sha256 of the source report's own JSON encoding)", artifact.SourceReportSHA256, wantHash)
	}
	// The underlying CalibrationResult (embedded) must also have survived --
	// provenance is ADDED, not a replacement for the recommendation itself.
	if !artifact.ApplyReady {
		t.Errorf("ApplyReady = false in the round-tripped artifact, want true (this fixture's negative gate should pass)")
	}
	if artifact.Policy.SimilarityFloor == 0 {
		t.Errorf("Policy.SimilarityFloor = 0 in the round-tripped artifact, want the recommended tau")
	}
}

// TestCalibrationRunner_SourceReportSHA256HashesTheActualFileBytes is
// CHAOS-3852's core pinning test, porting
// TestMarginCalibrationRunner_SourceReportSHA256HashesTheActualFileBytes
// (margin_calibration_test.go, codex r8 O3) to this tool: the written
// artifact's SourceReportSHA256 must equal an INDEPENDENTLY computed sha256
// of the EXACT bytes on disk at reportPath -- not a hash of a re-marshalled
// CalibrationReport, which (being a deliberately REDUCED struct, per its
// own doc comment) silently drops every field a real oracle report carries
// that CalibrationReport does not declare.
//
// The on-disk fixture below is built as a SUPERSET of CalibrationReport
// (embeds it, adds fields CalibrationReport has no field for at all) --
// exactly the real "extra fields json.Unmarshal silently drops" shape a
// genuine oracle report has. If this test built the on-disk bytes by
// marshalling the SAME reduced report value the runner receives, the two
// byte sequences would be IDENTICAL by construction and the test would
// pass whether or not the fix actually hashes the right bytes -- vacuous in
// exactly the way this fix exists to close. This shape makes the two byte
// sequences genuinely DIFFER, so only the FIXED code path (hashing
// reportBytes directly) can pass.
func TestCalibrationRunner_SourceReportSHA256HashesTheActualFileBytes(t *testing.T) {
	var cases []CalibrationCase
	for i := 0; i < 30; i++ {
		sPlus := 0.80 + float64(i)*(0.15/29.0)
		sMinus := 0.05 + float64(i)*(0.15/29.0)
		cases = append(cases, CalibrationCase{
			Cause: "hit", CorrectSimilarity: floatPtr(sPlus), BestWrongSimilarity: floatPtr(sMinus),
			HardNegativesTruncated: boolPtr(false),
		})
	}
	report := CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: 20, Tau: 0.55, Cases: cases}
	opts := CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}

	type onDiskReport struct {
		CalibrationReport
		// Fields a REAL oracle report carries that CalibrationReport has NO
		// field for at all -- json.Unmarshal into CalibrationReport
		// silently drops these, which is exactly what makes re-marshalling
		// report diverge from these actual on-disk bytes.
		Total    int `json:"total"`
		Hits     int `json:"hits"`
		TextLoss int `json:"text_loss"`
	}
	encoded, err := json.Marshal(onDiskReport{CalibrationReport: report, Total: 50, Hits: 12, TextLoss: 3})
	if err != nil {
		t.Fatalf("marshal report fixture: %v", err)
	}
	reportPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(reportPath, encoded, 0o600); err != nil {
		t.Fatalf("write report fixture: %v", err)
	}
	// Read it back exactly like TestCalibrateRetrievalPolicyFromReportFile
	// does -- the SAME bytes runCalibrationRunner is handed below.
	onDiskBytes, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report fixture: %v", err)
	}
	// Sanity: a re-marshal of the REDUCED report value (what the OLD, buggy
	// code hashed) must NOT already equal the real on-disk bytes -- if it
	// did, this fixture would be as vacuous as the rejected draft above.
	if reducedEncoded, err := json.Marshal(report); err == nil && string(reducedEncoded) == string(onDiskBytes) {
		t.Fatal("test fixture bug: re-marshalling the reduced report produced the SAME bytes as the on-disk fixture -- this test would not distinguish the fix from the defect it exists to catch")
	}
	wantSum := sha256.Sum256(onDiskBytes)
	wantHex := hex.EncodeToString(wantSum[:])

	outputPath := filepath.Join(t.TempDir(), "policy.json")
	fake := runFakeCalibrationRunnerWithReportPathAndBytes(t, report, opts, reportPath, onDiskBytes, outputPath)
	if fake.failed {
		t.Fatalf("runCalibrationRunner unexpectedly called Fatalf: logs=%v", fake.logs)
	}
	written, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read written artifact: %v", err)
	}
	var decoded CalibrationArtifact
	if err := json.Unmarshal(written, &decoded); err != nil {
		t.Fatalf("decode written artifact: %v", err)
	}
	if decoded.SourceReportSHA256 != wantHex {
		t.Fatalf("decoded.SourceReportSHA256 = %q, want %q (sha256 of the actual on-disk report bytes, independently computed)", decoded.SourceReportSHA256, wantHex)
	}
}

// TestRunCalibrationRunner_TauIsPrintedRoundTrippable is the codex round-5
// FIX C pinning test: the runner's re-run instruction line must print tau
// in a form that parses back to the EXACT same float64 as the
// recommendation, not %.4f's lossy rounding. Fixture: S+={0.3},
// TargetRecall=1.0 -> tau = Nextafter(0.3, -Inf), a value with far more
// than 4 decimal digits of precision, so this test actually distinguishes
// round-trippable formatting from %.4f rather than coincidentally passing
// either way.
func TestRunCalibrationRunner_TauIsPrintedRoundTrippable(t *testing.T) {
	report := CalibrationReport{
		EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension,
		Cases: []CalibrationCase{{Cause: "hit", CorrectSimilarity: floatPtr(0.3)}},
	}
	opts := CalibrationOptions{TargetRecall: 1.0, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}

	wantResult, err := CalibrateFromReport(report, opts)
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}

	fake := runFakeCalibrationRunner(t, report, opts, "")
	const marker = "precisely "
	var printedTau string
	for _, line := range fake.logs {
		if idx := strings.Index(line, marker); idx >= 0 {
			printedTau = line[idx+len(marker):]
		}
	}
	if printedTau == "" {
		t.Fatalf("no re-run tau instruction line found in logs: %v", fake.logs)
	}

	// Sanity: this fixture's tau must NOT already equal its own %.4f
	// rendering, or this test would pass regardless of which format the
	// runner actually used.
	if printedTau == fmt.Sprintf("%.4f", wantResult.Policy.SimilarityFloor) {
		t.Fatalf("printed tau %q equals its own %%.4f rendering -- fixture must produce a tau with more precision to make this test meaningful", printedTau)
	}

	parsed, err := strconv.ParseFloat(printedTau, 64)
	if err != nil {
		t.Fatalf("strconv.ParseFloat(%q): %v", printedTau, err)
	}
	if parsed != wantResult.Policy.SimilarityFloor {
		t.Fatalf("printed tau %q parses back to %v, want EXACTLY %v (bit-identical to the recommendation) -- a lossy format would silently break the round-2/round-3 exact re-run workflow", printedTau, parsed, wantResult.Policy.SimilarityFloor)
	}
}

// TestHardNegativeCaseCount_ExhaustiveDecisionTable is the codex round-5
// FIX B pinning test: every REACHABLE (truncated, list, saturated, total)
// combination in hardNegativeCaseCount's decision table, enumerated
// explicitly -- see that function's doc comment for the table this mirrors.
// tau=0.5 throughout; "above" means strictly > 0.5, "below" means <= 0.5.
//
// This specifically pins the round-5 regression cell (truncated=true,
// EMPTY list): round-4's len(similarities)>0 guard silently bypassed BOTH
// the total-consumption and the refusal branch for this exact shape
// (ACR_TEST_ORACLE_HARD_NEGATIVES=0), returning count=0/sufficient=true
// when the true answer depended entirely on whether a total was present.
func TestHardNegativeCaseCount_ExhaustiveDecisionTable(t *testing.T) {
	const tau = 0.5
	unsaturated := []float64{0.6, 0.4} // one above tau, one at-or-below -- NOT saturated
	saturated := []float64{0.6, 0.7}   // both above tau -- saturated

	cases := []struct {
		name          string
		similarities  []float64
		truncated     bool
		aboveTauCount *int
		reportTau     float64
		wantCount     int
		wantSuff      bool
	}{
		// Row 1: truncated=false, list=empty -> SAFE, count=0.
		{"not-truncated/empty", nil, false, nil, 0, 0, true},

		// Row 2: truncated=false, list=nonempty, unsaturated -> SAFE, count=local.
		{"not-truncated/nonempty/unsaturated", unsaturated, false, nil, 0, 1, true},

		// Row 3: truncated=false, list=nonempty, saturated -> SAFE, count=local
		// (trusted even though it LOOKS saturated -- the harness said complete).
		{"not-truncated/nonempty/saturated", saturated, false, nil, 0, 2, true},

		// Row 4-6: truncated=true, list=EMPTY -- the round-5 regression cell.
		// No local information at all; resolution depends entirely on total.
		{"truncated/empty/no-total", nil, true, nil, 0, 0, false},
		{"truncated/empty/total-matching-tau", nil, true, intPtr(30), tau, 30, true},
		{"truncated/empty/total-mismatched-tau", nil, true, intPtr(30), 0.9, 0, false},

		// Row 7: truncated=true, list=nonempty, UNSATURATED -> SAFE via the
		// sort-order-exactness shortcut, regardless of total (present or not).
		{"truncated/nonempty/unsaturated/no-total", unsaturated, true, nil, 0, 1, true},
		{"truncated/nonempty/unsaturated/total-present-but-unneeded", unsaturated, true, intPtr(99), tau, 1, true},

		// Row 8-10: truncated=true, list=nonempty, SATURATED -- round-2/3's
		// original case. Resolution depends entirely on total.
		{"truncated/nonempty/saturated/no-total", saturated, true, nil, 0, 0, false},
		{"truncated/nonempty/saturated/total-matching-tau", saturated, true, intPtr(50), tau, 50, true},
		{"truncated/nonempty/saturated/total-mismatched-tau", saturated, true, intPtr(50), 0.9, 0, false},

		// codex round-8 P2: an IMPOSSIBLE total -- present AND tau-matching,
		// which would otherwise be trusted -- must still refuse. A negative
		// total is nonsensical on its face; a total below `local` (the count
		// of serialized entries this function independently verified clear
		// tau -- 0 for the empty-list row, 2 for `saturated`) is equally
		// impossible, since the full-harvest total can never be smaller than
		// a count taken from a PREFIX of it (dedupeHardNegatives sorts
		// descending before capping). Both refuse exactly like "no matching
		// total at all" (count=0, sufficient=false), never sizing K off a
		// number that cannot be correct.
		{"truncated/empty/total-negative-matching-tau", nil, true, intPtr(-5), tau, 0, false},
		{"truncated/nonempty/saturated/total-negative-matching-tau", saturated, true, intPtr(-1), tau, 0, false},
		{"truncated/nonempty/saturated/total-below-local-matching-tau", saturated, true, intPtr(1), tau, 0, false},
		// Boundary: total == local exactly (2, matching `saturated`'s two
		// entries) is PLAUSIBLE, not impossible -- must still be trusted.
		{"truncated/nonempty/saturated/total-equals-local-matching-tau", saturated, true, intPtr(2), tau, 2, true},
	}

	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		c := c
		if seen[c.name] {
			t.Fatalf("duplicate case name %q in the table", c.name)
		}
		seen[c.name] = true
		t.Run(c.name, func(t *testing.T) {
			count, sufficient := hardNegativeCaseCount(c.similarities, tau, c.truncated, c.aboveTauCount, c.reportTau)
			if count != c.wantCount || sufficient != c.wantSuff {
				t.Fatalf("hardNegativeCaseCount(...) = (%d, %t), want (%d, %t)", count, sufficient, c.wantCount, c.wantSuff)
			}
		})
	}
}

// TestCalibrateFromReport_OverFetchMultiplierGate_ExhaustiveCells tabulates
// the OTHER branchy decision surface in this file (codex round-5's closing
// instruction): the conjunction gating OverFetchMultiplier's computed value,
// `!kInsufficientData && report.TopK > 0 && p90 > 0`. Three independent
// booleans, each capable of forcing the "unchanged" (1, rendered 0) default
// on its own -- exactly the shape that made hardNegativeCaseCount's
// untabulated version hide a reachable bug. This table covers every
// (TopK>0, p90>0) cell with kInsufficientData=false; the kInsufficientData=true
// cell (forces "unchanged" regardless of TopK/p90) is already covered by
// TestCalibrateFromReport_TruncatedSaturatedCaseWithoutTotalRefusesToSizeK
// (TopK=5, a case whose near-dup count would be nonzero if computed) rather
// than duplicated here.
func TestCalibrateFromReport_OverFetchMultiplierGate_ExhaustiveCells(t *testing.T) {
	sPlusValues := []float64{0.50, 0.10, 0.90, 0.30, 0.70, 0.20, 1.00, 0.40, 0.80, 0.60} // tau resolves to 0.20 at 0.90 target

	buildReport := func(topK int, withHardNegatives bool) CalibrationReport {
		var cases []CalibrationCase
		for _, s := range sPlusValues {
			c := CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s), HardNegativesTruncated: boolPtr(false)}
			if withHardNegatives {
				// One hard negative per case, comfortably above the ~0.20
				// tau this fixture resolves to -- guarantees p90 > 0.
				c.HardNegatives = []CalibrationHardNegative{{Kind: "k", CanonicalID: "id", Similarity: 0.25}}
			}
			cases = append(cases, c)
		}
		return CalibrationReport{EmbedIdentity: testEmbedIdentity, EmbedDimension: testDimension, TopK: topK, Cases: cases}
	}
	opts := CalibrationOptions{TargetRecall: 0.90, TargetEmbedIdentity: testEmbedIdentity, TargetDimension: testDimension}

	cases := []struct {
		name              string
		topK              int
		withHardNegatives bool
		wantMultiplier    int
	}{
		{"TopK<=0, p90>0 -> unchanged (spec: TopK<=0 always recommends 1, regardless of density)", 0, true, 0},
		{"TopK<=0, p90==0 -> unchanged", 0, false, 0},
		{"TopK>0, p90==0 -> unchanged (no density signal to widen for)", 20, false, 0},
		{"TopK>0, p90>0 -> computed from the density formula", 20, true, 2}, // ceil((20+1)/20) = 2
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := CalibrateFromReport(buildReport(c.topK, c.withHardNegatives), opts)
			if err != nil {
				t.Fatalf("CalibrateFromReport: %v", err)
			}
			if !result.KApplyReady {
				t.Fatalf("KApplyReady = false, want true -- this fixture's cases are all explicitly complete (HardNegativesTruncated=false)")
			}
			if result.Policy.OverFetchMultiplier != c.wantMultiplier {
				t.Fatalf("OverFetchMultiplier = %d, want %d (TopK=%d, hard negatives=%t)", result.Policy.OverFetchMultiplier, c.wantMultiplier, c.topK, c.withHardNegatives)
			}
		})
	}
}
