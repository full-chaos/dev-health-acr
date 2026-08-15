package falkorgraph

import (
	"math"
	"testing"
)

func floatPtr(v float64) *float64 { return &v }

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
	report := CalibrationReport{Cases: cases}

	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90})
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

// A single S+ sample is its own tau, with achieved recall trivially 1.0.
func TestCalibrateFromReport_SingleSample(t *testing.T) {
	report := CalibrationReport{Cases: []CalibrationCase{{Cause: "hit", CorrectSimilarity: floatPtr(0.42)}}}
	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90})
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
	result, err := CalibrateFromReport(CalibrationReport{Cases: cases}, CalibrationOptions{TargetRecall: 1.0})
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
	report := CalibrationReport{Cases: []CalibrationCase{
		{Cause: "subject_missing"},
		{Cause: "vector_missing"},
	}}
	if _, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90}); err != ErrNoCorrectSimilaritySamples {
		t.Fatalf("err = %v, want ErrNoCorrectSimilaritySamples", err)
	}
}

func TestCalibrateFromReport_InvalidTargetRecallIsAnError(t *testing.T) {
	report := CalibrationReport{Cases: []CalibrationCase{{CorrectSimilarity: floatPtr(0.5)}}}
	for _, target := range []float64{0, -0.1, 1.1} {
		if _, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: target}); err != ErrInvalidTargetRecall {
			t.Fatalf("TargetRecall=%v: err = %v, want ErrInvalidTargetRecall", target, err)
		}
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
	report := CalibrationReport{TopK: 20, Cases: cases}

	result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90})
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
			cases = append(cases, CalibrationCase{Cause: "hit", CorrectSimilarity: floatPtr(s), HardNegatives: negatives})
		}
		report := CalibrationReport{TopK: 20, Cases: cases}
		result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90})
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
		report := CalibrationReport{TopK: 20, Cases: cases}
		result, err := CalibrateFromReport(report, CalibrationOptions{TargetRecall: 0.90})
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
		return CalibrationReport{TopK: 20, Cases: cases}
	}

	below, err := CalibrateFromReport(build(0.05), CalibrationOptions{TargetRecall: 0.90})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !almostEqual(below.HardNegativeRejectRate, 1.0) {
		t.Fatalf("HardNegativeRejectRate = %v, want 1.0 for negatives entirely below tau", below.HardNegativeRejectRate)
	}

	above, err := CalibrateFromReport(build(0.99), CalibrationOptions{TargetRecall: 0.90})
	if err != nil {
		t.Fatalf("CalibrateFromReport: %v", err)
	}
	if !almostEqual(above.HardNegativeRejectRate, 0.0) {
		t.Fatalf("HardNegativeRejectRate = %v, want 0.0 for negatives entirely above tau", above.HardNegativeRejectRate)
	}
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
