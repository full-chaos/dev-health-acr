package falkorgraph

import "math"

// RecallAtK is the pure ANN-quality metric CHAOS-3832 T2's sweep is built
// around: what fraction of a reference ranking's top K entries also appear in
// a candidate ranking's top K.
//
// This is deliberately a DIFFERENT recall than T1's harness-level "recall@20"
// (correct-subject-found rate over held-out paraphrase questions, spec §5 L1).
// T1 measures TEXT relevance against a real question corpus withheld from this
// lane; this measures ANN-ALGORITHM fidelity -- how much of the true nearest-
// neighbor set a given (efConstruction, efRuntime) setting actually returns --
// against a reference ranking that can be produced from the corpus alone, with
// no question corpus involved. The two compose (spec §5 L2: "the T1 oracle
// quantifies exactly how many misses are ANN-attributable") but neither one
// substitutes for the other, and T2 does not need T1's withheld corpus to run.
//
// Both slices are assumed already ordered best-first (ascending distance /
// descending similarity), matching what vectorSearchNodesWithOverFetch and the
// live probe's ORDER BY score ASC both produce. Only the first k of each is
// considered; a candidate ranking shorter than k is compared as-is (a short
// candidate list can only ever cost recall, never inflate it).
//
// k <= 0 or an empty reference reports 0 rather than dividing by zero or by a
// meaningless k -- an empty answer is a clean signal, not a NaN one call site
// has to remember to guard against.
func RecallAtK(reference, candidate []string, k int) float64 {
	if k <= 0 || len(reference) == 0 {
		return 0
	}
	if k > len(reference) {
		k = len(reference)
	}
	top := candidate
	if len(top) > k {
		top = top[:k]
	}
	present := make(map[string]struct{}, len(top))
	for _, id := range top {
		present[id] = struct{}{}
	}
	hits := 0
	for _, id := range reference[:k] {
		if _, ok := present[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// cosineSimilarity is a plain, unnormalized-vector cosine -- distinct from
// embedprovider.CosineFromDistance (which converts FalkorDB's returned
// DISTANCE score into a similarity) and from the live sweep's server-side
// comparison (which never brings a vector into Go at all, see hnsw_sweep.go).
// It exists for BruteForceTopK and this package's own unit tests, where an
// in-memory reference ranking is the whole point.
//
// Vectors are NOT assumed L2-normalized (CHAOS-3742 spec §5 L1: production
// vectors are stored unnormalized, embedprovider/provider.go:151-158). Two
// zero-length or mismatched-length vectors report 0 rather than panicking or
// dividing by zero -- the honest "no relationship" answer, matching
// vectorRelevanceFromSimilarity's own defensive-toward-less-confidence
// posture elsewhere in this package.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// BruteForceTopK ranks every entry of corpus by cosine similarity to query and
// returns the k highest IDs, best first -- the textbook exact-search oracle,
// for use where the FULL corpus already lives in Go memory (this package's own
// tests; a future extension once vector-typed RETURN values are decoded
// client-side, see hnsw_sweep.go's doc comment on why the live T2 probe uses a
// server-side reference ranking instead today).
//
// A tie in similarity breaks toward the LOWER id, so ranking is deterministic
// across runs on the same input -- an oracle whose own order was unstable
// would make every recall number measured against it unstable too.
func BruteForceTopK(query []float32, corpus map[string][]float32, k int) []string {
	if k <= 0 || len(corpus) == 0 {
		return nil
	}
	type scored struct {
		id         string
		similarity float64
	}
	ranked := make([]scored, 0, len(corpus))
	for id, vector := range corpus {
		ranked = append(ranked, scored{id: id, similarity: cosineSimilarity(query, vector)})
	}
	// Insertion sort is fine here: BruteForceTopK's corpora are test-fixture
	// sized (hundreds of entries at most), never the live 36k-vector index.
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0; j-- {
			swap := ranked[j].similarity > ranked[j-1].similarity ||
				(ranked[j].similarity == ranked[j-1].similarity && ranked[j].id < ranked[j-1].id)
			if !swap {
				break
			}
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
	if k > len(ranked) {
		k = len(ranked)
	}
	out := make([]string, k)
	for i := 0; i < k; i++ {
		out[i] = ranked[i].id
	}
	return out
}
