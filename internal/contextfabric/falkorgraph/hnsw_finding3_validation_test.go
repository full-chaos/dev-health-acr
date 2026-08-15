package falkorgraph

import "testing"

// TestFinding3ValidationOldStrictMetricUndercountsTheSameBoundaryTie is
// repro-or-refute evidence for Luna round-1 finding 3, run directly against
// the SAME fixture TestRecallAtKTieTolerantBoundarySwapIsNotAMiss uses: the
// OLD metric (RecallAtK, what RunHNSWSweep called before this fix) and the
// NEW metric (RecallAtKTieTolerant, what it calls now) evaluated on IDENTICAL
// reference/candidate data. This file exists only to make that comparison
// explicit for the review record; RunHNSWSweep's actual behavior is already
// covered by TestRunHNSWSweepTieToleranceAtTheBoundaryDoesNotCountAsAMiss.
func TestFinding3ValidationOldStrictMetricUndercountsTheSameBoundaryTie(t *testing.T) {
	// {a:0.1, b:0.2, c:0.2, d:0.4} -- b and c are EXACTLY tied at the k=2
	// boundary. The candidate correctly found the tied pair (a, c) but a
	// strict positional top-k reference (b came first in insertion, so the
	// "true" top-2 is read as {a,b}) marks c as a miss.
	reference := []ScoredID{{ID: "a", Score: 0.1}, {ID: "b", Score: 0.2}, {ID: "c", Score: 0.2}, {ID: "d", Score: 0.4}}
	candidate := []string{"a", "c"}

	strictReferenceTop := []string{"a", "b"} // what a strict positional cutoff would have used
	oldMetric := RecallAtK(strictReferenceTop, candidate, 2)
	newMetric := RecallAtKTieTolerant(reference, candidate, 2)

	if oldMetric != 0.5 {
		t.Fatalf("pre-fix metric (RecallAtK, strict) = %v, want 0.5 -- this IS finding 3, reproduced: "+
			"a genuinely equally-close neighbor at a tied boundary was scored as a miss", oldMetric)
	}
	if newMetric != 1.0 {
		t.Fatalf("post-fix metric (RecallAtKTieTolerant) = %v, want 1.0 -- the fix must resolve the same tie the old metric miscounted", newMetric)
	}
	t.Logf("finding 3 validated: identical input, old metric=%.1f (WRONG, undercounts a tied match) -> new metric=%.1f (correct)", oldMetric, newMetric)
}
