package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file is CHAOS-3832 T2's sweep runner: parameterized HNSW
// (efConstruction/efRuntime) and over-fetch sweeps against a target graph,
// reporting ANN recall@K relative to a reference setting (RecallAtK,
// hnsw_recall.go). MEASUREMENT-ONLY -- nothing here is called from any
// production path, and running it changes no production default. See
// hnsw_sweep_live_test.go for the live probe and docs/design/
// context-fabric-hnsw-sweep.md for the recommendation this tooling produced.
//
// Scoping note (why this needs no oracle from lane-3831/T1): T1's harness
// measures TEXT recall against a withheld question corpus -- "does the right
// SUBJECT come back for this paraphrase". This file measures ANN-ALGORITHM
// recall -- "does the ANN index return the same neighbors a higher-fidelity
// setting of the SAME index would" -- which only needs vectors the corpus
// already has, not a question corpus or a text-relevance oracle. The two
// numbers answer different questions and neither substitutes for the other
// (spec §5 L2's own framing: "the T1 oracle quantifies exactly how many
// misses are ANN-attributable" -- this file is what makes that split
// possible, not what performs it).

// vectorSweepSeedTopK ranks the k nearest neighbors of an EXISTING node's own
// stored vector, entirely server-side: the query vector never leaves FalkorDB
// and is never decoded into Go. This sidesteps a real gap (the pinned
// falkordb-go client's decodeValue has no case for a vector-typed RETURN
// value -- verified by reading client.go; only WRITING a vector via vecf32()
// is exercised anywhere in this package today) without adding untested vector
// decoding to shared client.go for a measurement-only tool.
//
// Consequence, stated plainly: every sweep query is a LEAVE-ONE-IN self-query
// (a node's own vector against the whole index, which trivially ranks that
// node first at similarity 1). That is fine for THIS metric -- recall@K is
// computed relative to a reference SETTING's top-K for the same query, not
// against a hand-labeled ground truth, so the self-match term is identical
// across every swept setting and cancels out of the comparison. It would NOT
// be fine as a proxy for T1's text-relevance recall, which is a different
// metric this file does not compute.
//
// Returns the SCORE alongside each id (not just the id) -- Luna round-1
// finding 3: comparing bare id lists cannot tell a genuine miss from two
// settings that both correctly found an EQUALLY-close neighbor but happened
// to return different members of a tied group at the k-th boundary. See
// ScoredID / TieExpandedTop / RecallAtKTieTolerant (hnsw_recall.go).
func (a *Adapter) vectorSweepSeedTopK(ctx context.Context, key, seedCanonicalID string, k int) ([]ScoredID, time.Duration, error) {
	if k <= 0 {
		return nil, 0, fmt.Errorf("vectorSweepSeedTopK: k must be positive, got %d", k)
	}
	cypher := fmt.Sprintf(
		"MATCH (seed:%s {%s:$seed}) WITH seed.%s AS v "+
			"CALL db.idx.vector.queryNodes('%s', '%s', %d, v) YIELD node, score "+
			"RETURN node.%s AS id, score ORDER BY score ASC",
		labelSubject, propCanonicalID, propEmbedding,
		labelSubject, propEmbedding, k,
		propCanonicalID,
	)
	start := a.now()
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"seed": seedCanonicalID}, true)
	elapsed := a.now().Sub(start)
	if err != nil {
		return nil, elapsed, safeDependencyError("vector sweep seed query", err)
	}
	scored := make([]ScoredID, 0, len(rows))
	for _, r := range rows {
		id := rowString(r, "id")
		score, ok := r.get("score").(float64)
		if id == "" || !ok {
			continue
		}
		scored = append(scored, ScoredID{ID: id, Score: score})
	}
	return scored, elapsed, nil
}

// SweepBuildPoint is one HNSW index build configuration -- the parameters
// that require recreateVectorIndexWithOptions to change (spec §5 L2). M is
// fixed at the production value (16) unless overridden; a sweep normally
// varies only EfConstruction and EfRuntime, matching spec §5 L2's stated
// range.
type SweepBuildPoint struct {
	M              int
	EfConstruction int
	EfRuntime      int
}

func (p SweepBuildPoint) options() hnswIndexOptions {
	return hnswIndexOptions{M: p.M, EfConstruction: p.EfConstruction, EfRuntime: p.EfRuntime}
}

func (p SweepBuildPoint) String() string {
	return fmt.Sprintf("M=%d,efConstruction=%d,efRuntime=%d", p.M, p.EfConstruction, p.EfRuntime)
}

// SweepResult is one build point's measured outcome, relative to whichever
// build point the caller designated as the reference.
type SweepResult struct {
	Point          SweepBuildPoint
	IndexBuildTime time.Duration
	RecallAtK      float64 // relative to the reference build point's top-K, see RunHNSWSweep.
	P50Latency     time.Duration
	P95Latency     time.Duration
	Queries        int // seeds that contributed a successful query at THIS point.
	SkippedSeeds   int // seeds whose query errored at this point -- Luna round-1 finding 1: a
	// non-zero value here means the measurement is PARTIAL, and the caller
	// must be able to see that rather than infer a clean 0.853 that is
	// actually "0.853 over 40 of 58 seeds." Queries + SkippedSeeds always
	// equals the seed count RunHNSWSweep was called with.
}

// referenceOverfetch is how many extra rows beyond k the reference build's
// query asks for, so a tie GROUP spanning the k-th boundary is captured
// whole rather than arbitrarily split (Luna round-1 finding 3;
// TieExpandedTop, hnsw_recall.go). 2x is a deliberately generous, cheap
// margin -- the query itself is sub-few-ms at this corpus's scale (live-
// measured, docs/design/context-fabric-hnsw-sweep.md), so doubling it costs
// nothing material.
const referenceOverfetch = 2

// RunHNSWSweep recreates the vector index at each of points (deduplicated,
// reference point always included even if the caller omitted it), runs every
// seedCanonicalIDs query against each build, and reports each build's
// recall@K RELATIVE TO the reference point's own top-K results -- so the
// reference point always reports RecallAtK=1.0 by construction; that is the
// expected, correct output, not a bug in the metric.
//
// dimension must match the embedder identity already stamped on the graph's
// vector index (AC-3778-7) -- RunHNSWSweep does not verify this itself; the
// caller is expected to have read it via vectorIndexDimension first, exactly
// as ensureVectorIndex does before ANY vector-index write.
//
// The FIRST build recreates the index at the reference point and captures
// every seed's SCORED top-K (overfetched, see referenceOverfetch) as the
// comparison baseline; every subsequent build is measured against that
// baseline via RecallAtKTieTolerant, not the strict RecallAtK, so a swap
// between two equally-close (tied-score) neighbors at the boundary is never
// misread as a miss. Recreation order after the reference follows points'
// given order.
//
// A seed whose query errors at a point is EXCLUDED from that point's
// Queries/recall contribution and counted in SkippedSeeds instead -- a query
// failure must never read as "perfect disagreement" or "perfect agreement".
//
// FAIL-CLOSED ON ZERO COVERAGE (Luna round-1 finding 1): if EVERY seed's
// query errors at a point (Queries==0), that point's diagnostic result is
// still appended and delivered to onResult (so the caller can see WHAT
// failed), but RunHNSWSweep then returns a non-nil error -- a green sweep
// over zero real queries (e.g. every query erroring because dimension is
// wrong) is a false-fine measurement-fails-toward-fine failure, not a valid
// 0.0 recall. This mirrors the mid-sweep-failure contract below: results
// already computed are never discarded, but the caller cannot mistake a
// zero-coverage point for a real one.
//
// onResult, when non-nil, is called synchronously right after each point's
// result is computed -- BEFORE the whole sweep finishes. A multi-point live
// sweep runs for minutes; if a later point's index rebuild times out (a real
// possibility under host/Docker contention, not merely theoretical -- see
// docs/design/context-fabric-hnsw-sweep.md's live run), earlier points'
// results must not be lost along with it. RunHNSWSweep also returns every
// result computed so far ALONGSIDE a non-nil error on a mid-sweep failure,
// for a caller that does not pass onResult.
func (a *Adapter) RunHNSWSweep(
	ctx context.Context, key string, dimension int,
	seedCanonicalIDs []string, k int, reference SweepBuildPoint, points []SweepBuildPoint,
	onResult func(SweepResult),
) ([]SweepResult, error) {
	ordered := dedupeSweepPoints(reference, points)

	referenceTop := make(map[string][]ScoredID, len(seedCanonicalIDs))
	results := make([]SweepResult, 0, len(ordered))
	for _, point := range ordered {
		buildStart := a.now()
		if err := a.recreateVectorIndexWithOptions(ctx, key, dimension, point.options()); err != nil {
			return results, fmt.Errorf("recreate vector index at %s: %w", point, err)
		}
		buildTime := a.now().Sub(buildStart)

		isReference := point == reference
		fetchK := k
		if isReference {
			fetchK = k * referenceOverfetch
		}

		latencies := make([]time.Duration, 0, len(seedCanonicalIDs))
		var recallSum float64
		contributing := 0
		skipped := 0
		for _, seed := range seedCanonicalIDs {
			top, latency, err := a.vectorSweepSeedTopK(ctx, key, seed, fetchK)
			if err != nil {
				skipped++
				continue
			}
			latencies = append(latencies, latency)
			if isReference {
				referenceTop[seed] = top
				recallSum += 1 // a setting is perfectly recalled against itself, by definition.
				contributing++
				continue
			}
			base, ok := referenceTop[seed]
			if !ok {
				// The reference query for this exact seed also failed --
				// counted as skipped here too, there is nothing to compare
				// against, and this must not silently read as a match OR a
				// miss.
				skipped++
				continue
			}
			candidateIDs := make([]string, len(top))
			for i, s := range top {
				candidateIDs[i] = s.ID
			}
			recallSum += RecallAtKTieTolerant(base, candidateIDs, k)
			contributing++
		}

		p50, p95 := latencyPercentiles(latencies)
		recall := 0.0
		if contributing > 0 {
			recall = recallSum / float64(contributing)
		}
		result := SweepResult{
			Point: point, IndexBuildTime: buildTime, RecallAtK: recall,
			P50Latency: p50, P95Latency: p95, Queries: contributing, SkippedSeeds: skipped,
		}
		results = append(results, result)
		if onResult != nil {
			onResult(result)
		}
		if contributing == 0 {
			return results, fmt.Errorf(
				"point %s: 0 of %d seed queries succeeded -- a zero-coverage sweep is not a valid measurement",
				point, len(seedCanonicalIDs))
		}
	}
	return results, nil
}

// dedupeSweepPoints puts reference first (so its top-K is captured before any
// comparison needs it) and appends every distinct remaining point in the
// caller's order.
func dedupeSweepPoints(reference SweepBuildPoint, points []SweepBuildPoint) []SweepBuildPoint {
	seen := map[SweepBuildPoint]bool{reference: true}
	ordered := []SweepBuildPoint{reference}
	for _, point := range points {
		if seen[point] {
			continue
		}
		seen[point] = true
		ordered = append(ordered, point)
	}
	return ordered
}

// latencyPercentiles reports p50/p95 over samples. Nearest-rank, ascending
// sort -- consistent with how the rest of this codebase has no existing
// percentile helper to share (verified: no other package in this repository
// computes a latency percentile), so this is a small, self-contained one
// scoped to the sweep report rather than a new shared dependency.
func latencyPercentiles(samples []time.Duration) (p50, p95 time.Duration) {
	if len(samples) == 0 {
		return 0, 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := func(percentile float64) time.Duration {
		idx := int(percentile*float64(len(sorted))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return rank(0.50), rank(0.95)
}

// protectedSweepGraphKeys is a hardcoded, EXACT-match denylist of graph keys
// RunHNSWSweep's destructive drop/recreate cycle must never target, no matter
// what an operator's environment variables claim (Luna round-1 finding 2).
//
// The earlier gate (isSweepTargetSafe's only check before this) was a
// substring heuristic -- "does the key contain \"copy\"" -- and Luna showed
// it precisely: a PRODUCTION key can itself contain "copy" (an org named
// with it, or a key that started life as a copy and was later promoted), so
// the heuristic would ACCEPT the one key it exists to reject. A substring
// match can never be a proof; an exact-equality denylist against a KNOWN key
// can. This entry is the live organization graph this lane was explicitly
// told is precious and must never be mutated (team-lead's live-graph-safety
// directive, CHAOS-3832 task brief) -- kept as one place so any future org
// this tooling might run near can be added here rather than trusted to an
// operator's env var alone.
var protectedSweepGraphKeys = []string{
	"acr-cf-fa7030e2106de7411bfbf8ebce74c620",
}

// isSweepTargetSafe reports whether key may be targeted by RunHNSWSweep's
// destructive drop/recreate cycle, and if not, why.
//
// TWO INDEPENDENT conditions, both required:
//  1. key must not EXACTLY match any entry in protectedSweepGraphKeys or
//     additionalProtected -- this is what makes the destructive path
//     provably unreachable for a KNOWN production key, not merely unlikely.
//  2. key must still carry the operator's declared "this is scratch" marker
//     (the "copy" substring) -- kept as a SECOND, independent condition
//     rather than the sole gate. It does not replace condition 1 (Luna's
//     point exactly), but it still catches a plain typo/copy-paste of some
//     OTHER unrelated key that never made it onto the denylist.
func isSweepTargetSafe(key string, additionalProtected []string) (bool, string) {
	for _, protected := range protectedSweepGraphKeys {
		if key == protected {
			return false, fmt.Sprintf("key %q exactly matches a hardcoded protected production graph key", key)
		}
	}
	for _, protected := range additionalProtected {
		protected = strings.TrimSpace(protected)
		if protected == "" {
			continue
		}
		if key == protected {
			return false, fmt.Sprintf("key %q exactly matches an operator-declared protected graph key", key)
		}
	}
	if !strings.Contains(strings.ToLower(key), "copy") {
		return false, fmt.Sprintf("key %q does not contain \"copy\" -- refusing to assume it is a scratch copy", key)
	}
	return true, ""
}
