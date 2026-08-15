package falkorgraph

import "math"

// ScoredID pairs a subject's canonical ID with the distance/score it was
// retrieved at, ascending-best-first order -- what vectorSweepSeedTopK now
// returns (CHAOS-3832 Luna round-1 finding 3), so a caller CAN tell whether
// two ids at the top-K boundary are genuinely distinct or tied.
type ScoredID struct {
	ID    string
	Score float64
}

// TieExpandedTop returns every id from ranked (ascending score, best first)
// whose score is <= the k-th ranked entry's score -- deliberately NOT a
// strict top-k cutoff.
//
// Luna round-1 finding 3: a strict "first k entries" cutoff attributes
// ANN-internal tie-breaking noise to a recall difference that is not real.
// This corpus has a concrete, common case where that noise bites: 78% of it
// is near-duplicate ci_pipeline_run text (spec §1), which projects to
// near-identical or exactly-identical embeddings, so exact score ties at the
// K-th boundary are not a pathological edge case here, they are routine. Two
// settings that both correctly rank a tied group at the boundary, but happen
// to include DIFFERENT members of that tie in their literal first-k rows,
// must not be scored as if one "missed" a neighbor the other "found" --
// both found an equally-close neighbor, at the SAME distance.
//
// Can return MORE than k ids when several are tied at the boundary; that is
// the point. Returns fewer than k only when ranked itself has fewer than k
// entries.
func TieExpandedTop(ranked []ScoredID, k int) map[string]bool {
	set := make(map[string]bool)
	if k <= 0 || len(ranked) == 0 {
		return set
	}
	if k > len(ranked) {
		k = len(ranked)
	}
	boundary := ranked[k-1].Score
	for _, entry := range ranked {
		if entry.Score > boundary {
			break // ranked is sorted ascending, so nothing further can tie.
		}
		set[entry.ID] = true
	}
	return set
}

// RecallAtKTieTolerant reports what fraction of candidateTopK's first k
// entries fall within the REFERENCE's tie-expanded top-k set (TieExpandedTop)
// -- the tie-tolerant counterpart to RecallAtK. The denominator stays k (not
// the possibly-larger tie-expanded set size) so results remain comparable
// across sweep points the same way RecallAtK's are.
//
// referenceRanked should be fetched with some overfetch beyond k (see
// RunHNSWSweep) so a tie GROUP spanning the k-th boundary is not itself cut
// off before it can be detected; candidateTopK is the candidate's own literal
// top-k, exactly as returned -- tie-tolerance only applies to what counts as
// "correct" (the reference side), not to what the candidate is judged on.
func RecallAtKTieTolerant(referenceRanked []ScoredID, candidateTopK []string, k int) float64 {
	if k <= 0 || len(referenceRanked) == 0 {
		return 0
	}
	if k > len(referenceRanked) {
		k = len(referenceRanked)
	}
	tieSet := TieExpandedTop(referenceRanked, k)
	top := candidateTopK
	if len(top) > k {
		top = top[:k]
	}
	hits := 0
	for _, id := range top {
		if tieSet[id] {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

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
