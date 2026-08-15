package falkorgraph

import "testing"

func TestRecallAtKPerfectAgreementIsOne(t *testing.T) {
	reference := []string{"a", "b", "c", "d"}
	candidate := []string{"a", "b", "c", "d"}
	if got := RecallAtK(reference, candidate, 4); got != 1.0 {
		t.Fatalf("RecallAtK() = %v, want 1.0", got)
	}
}

func TestRecallAtKPartialOverlap(t *testing.T) {
	reference := []string{"a", "b", "c", "d"}
	candidate := []string{"a", "x", "c", "y"} // 2 of 4 present
	if got := RecallAtK(reference, candidate, 4); got != 0.5 {
		t.Fatalf("RecallAtK() = %v, want 0.5", got)
	}
}

func TestRecallAtKIgnoresOrderWithinTopK(t *testing.T) {
	reference := []string{"a", "b", "c"}
	candidate := []string{"c", "a", "b"} // same set, different order
	if got := RecallAtK(reference, candidate, 3); got != 1.0 {
		t.Fatalf("RecallAtK() = %v, want 1.0 (order-independent within top-K)", got)
	}
}

func TestRecallAtKOnlyConsidersTopKOfEachSide(t *testing.T) {
	reference := []string{"a", "b", "c", "d", "e"}
	// "a" only appears past k=2 in candidate -- must not count.
	candidate := []string{"x", "y", "a", "b"}
	if got := RecallAtK(reference, candidate, 2); got != 0.0 {
		t.Fatalf("RecallAtK(k=2) = %v, want 0.0 (candidate's true top-2 shares nothing with reference's top-2)", got)
	}
}

func TestRecallAtKClampsKToReferenceLength(t *testing.T) {
	reference := []string{"a", "b"}
	candidate := []string{"a", "b", "c", "d", "e"}
	// Asking for k=10 against a 2-element reference must not panic or divide
	// by 10 -- it clamps to what the reference can actually offer.
	if got := RecallAtK(reference, candidate, 10); got != 1.0 {
		t.Fatalf("RecallAtK(k=10, len(reference)=2) = %v, want 1.0", got)
	}
}

func TestRecallAtKShortCandidateCostsRecallNotError(t *testing.T) {
	reference := []string{"a", "b", "c"}
	candidate := []string{"a"} // fewer than k results
	if got := RecallAtK(reference, candidate, 3); got != 1.0/3.0 {
		t.Fatalf("RecallAtK() = %v, want %v", got, 1.0/3.0)
	}
}

func TestRecallAtKZeroKOrEmptyReferenceIsZeroNotNaN(t *testing.T) {
	if got := RecallAtK([]string{"a"}, []string{"a"}, 0); got != 0 {
		t.Fatalf("RecallAtK(k=0) = %v, want 0", got)
	}
	if got := RecallAtK(nil, []string{"a"}, 5); got != 0 {
		t.Fatalf("RecallAtK(empty reference) = %v, want 0", got)
	}
}

func TestCosineSimilarityKnownAngles(t *testing.T) {
	cases := []struct {
		name    string
		a, b    []float32
		want    float64
		epsilon float64
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0, 1e-9},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0.0, 1e-9},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1.0, 1e-9},
		{"unnormalized scale must not matter", []float32{2, 0, 0}, []float32{5, 0, 0}, 1.0, 1e-9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cosineSimilarity(c.a, c.b)
			if diff := got - c.want; diff > c.epsilon || diff < -c.epsilon {
				t.Fatalf("cosineSimilarity(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestCosineSimilarityMismatchedOrZeroLengthIsZeroNotPanic(t *testing.T) {
	if got := cosineSimilarity([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Fatalf("mismatched length = %v, want 0", got)
	}
	if got := cosineSimilarity(nil, nil); got != 0 {
		t.Fatalf("empty vectors = %v, want 0", got)
	}
	if got := cosineSimilarity([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Fatalf("zero-norm vector = %v, want 0 (not NaN)", got)
	}
}

func TestBruteForceTopKRanksBySimilarityDescending(t *testing.T) {
	query := []float32{1, 0, 0}
	corpus := map[string][]float32{
		"exact":      {1, 0, 0},
		"near":       {0.9, 0.1, 0},
		"orthogonal": {0, 1, 0},
		"opposite":   {-1, 0, 0},
	}
	got := BruteForceTopK(query, corpus, 2)
	if len(got) != 2 || got[0] != "exact" || got[1] != "near" {
		t.Fatalf("BruteForceTopK() = %v, want [exact near]", got)
	}
}

func TestBruteForceTopKTiesBreakByLowerID(t *testing.T) {
	query := []float32{1, 0}
	corpus := map[string][]float32{
		"b": {2, 0}, // same direction, different magnitude -- cosine tie with "a"
		"a": {1, 0},
	}
	got := BruteForceTopK(query, corpus, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("BruteForceTopK() tie-break = %v, want [a b] (deterministic lower-id-first)", got)
	}
}

func TestBruteForceTopKEmptyInputsReportNil(t *testing.T) {
	if got := BruteForceTopK([]float32{1}, map[string][]float32{}, 5); got != nil {
		t.Fatalf("empty corpus = %v, want nil", got)
	}
	if got := BruteForceTopK([]float32{1}, map[string][]float32{"a": {1}}, 0); got != nil {
		t.Fatalf("k=0 = %v, want nil", got)
	}
}

func TestTieExpandedTopIncludesEveryEntryTiedAtTheBoundary(t *testing.T) {
	ranked := []ScoredID{
		{ID: "a", Score: 0.1}, {ID: "b", Score: 0.2}, {ID: "c", Score: 0.2}, {ID: "d", Score: 0.4},
	}
	got := TieExpandedTop(ranked, 2)
	want := map[string]bool{"a": true, "b": true, "c": true}
	if len(got) != len(want) {
		t.Fatalf("TieExpandedTop() = %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("TieExpandedTop() = %v, missing %q", got, id)
		}
	}
	if got["d"] {
		t.Fatal("TieExpandedTop() must not include an entry strictly past the boundary score")
	}
}

func TestTieExpandedTopNoTieIsExactlyK(t *testing.T) {
	ranked := []ScoredID{{ID: "a", Score: 0.1}, {ID: "b", Score: 0.2}, {ID: "c", Score: 0.3}}
	got := TieExpandedTop(ranked, 2)
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("TieExpandedTop() with no boundary tie = %v, want {a,b}", got)
	}
}

func TestTieExpandedTopEmptyOrZeroKIsEmpty(t *testing.T) {
	if got := TieExpandedTop(nil, 5); len(got) != 0 {
		t.Fatalf("TieExpandedTop(nil) = %v, want empty", got)
	}
	if got := TieExpandedTop([]ScoredID{{ID: "a", Score: 0.1}}, 0); len(got) != 0 {
		t.Fatalf("TieExpandedTop(k=0) = %v, want empty", got)
	}
}

func TestTieExpandedTopClampsKToRankedLength(t *testing.T) {
	ranked := []ScoredID{{ID: "a", Score: 0.1}, {ID: "b", Score: 0.2}}
	got := TieExpandedTop(ranked, 10)
	if len(got) != 2 {
		t.Fatalf("TieExpandedTop(k=10) over 2 entries = %v, want both included", got)
	}
}

func TestRecallAtKTieTolerantBoundarySwapIsNotAMiss(t *testing.T) {
	reference := []ScoredID{
		{ID: "a", Score: 0.1}, {ID: "b", Score: 0.2}, {ID: "c", Score: 0.2}, {ID: "d", Score: 0.4},
	}
	// The candidate's own top-2 picked "c" instead of "b" -- both tied at
	// the same score in the reference, so this must read as a perfect match.
	candidate := []string{"a", "c"}
	if got := RecallAtKTieTolerant(reference, candidate, 2); got != 1.0 {
		t.Fatalf("RecallAtKTieTolerant() = %v, want 1.0", got)
	}
}

func TestRecallAtKTieTolerantGenuineMissStillCounts(t *testing.T) {
	reference := []ScoredID{
		{ID: "a", Score: 0.1}, {ID: "b", Score: 0.2}, {ID: "c", Score: 0.5}, {ID: "d", Score: 0.6},
	}
	candidate := []string{"a", "z"} // "z" is nowhere near the tie-expanded set.
	if got := RecallAtKTieTolerant(reference, candidate, 2); got != 0.5 {
		t.Fatalf("RecallAtKTieTolerant() = %v, want 0.5 -- a genuine miss must still count as one", got)
	}
}

func TestRecallAtKTieTolerantEmptyOrZeroKIsZero(t *testing.T) {
	if got := RecallAtKTieTolerant(nil, []string{"a"}, 2); got != 0 {
		t.Fatalf("RecallAtKTieTolerant(empty reference) = %v, want 0", got)
	}
	if got := RecallAtKTieTolerant([]ScoredID{{ID: "a", Score: 0.1}}, []string{"a"}, 0); got != 0 {
		t.Fatalf("RecallAtKTieTolerant(k=0) = %v, want 0", got)
	}
}

func TestBruteForceTopKClampsKToCorpusSize(t *testing.T) {
	corpus := map[string][]float32{"a": {1, 0}, "b": {0, 1}}
	got := BruteForceTopK([]float32{1, 0}, corpus, 10)
	if len(got) != 2 {
		t.Fatalf("BruteForceTopK(k=10) over a 2-entry corpus returned %d entries, want 2", len(got))
	}
}
