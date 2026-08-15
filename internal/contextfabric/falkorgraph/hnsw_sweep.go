package falkorgraph

import (
	"context"
	"fmt"
	"sort"
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
func (a *Adapter) vectorSweepSeedTopK(ctx context.Context, key, seedCanonicalID string, k int) ([]string, time.Duration, error) {
	if k <= 0 {
		return nil, 0, fmt.Errorf("vectorSweepSeedTopK: k must be positive, got %d", k)
	}
	cypher := fmt.Sprintf(
		"MATCH (seed:%s {%s:$seed}) WITH seed.%s AS v "+
			"CALL db.idx.vector.queryNodes('%s', '%s', %d, v) YIELD node, score "+
			"RETURN node.%s AS id ORDER BY score ASC",
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
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if id := rowString(r, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, elapsed, nil
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
	Queries        int
}

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
// every seed's top-K as the comparison baseline; every subsequent build is
// measured against that baseline; recreation order after that follows points'
// given order. Recall is undefined (reported 0) for a seed whose query
// errored at either the reference build or the point under test -- a query
// failure must never read as "perfect disagreement" or "perfect agreement",
// so it is excluded from that seed's contribution and the Queries count
// reflects how many seeds actually contributed.
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

	referenceTop := make(map[string][]string, len(seedCanonicalIDs))
	results := make([]SweepResult, 0, len(ordered))
	for _, point := range ordered {
		buildStart := a.now()
		if err := a.recreateVectorIndexWithOptions(ctx, key, dimension, point.options()); err != nil {
			return results, fmt.Errorf("recreate vector index at %s: %w", point, err)
		}
		buildTime := a.now().Sub(buildStart)

		latencies := make([]time.Duration, 0, len(seedCanonicalIDs))
		var recallSum float64
		contributing := 0
		isReference := point == reference
		for _, seed := range seedCanonicalIDs {
			top, latency, err := a.vectorSweepSeedTopK(ctx, key, seed, k)
			if err != nil {
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
				continue
			}
			recallSum += RecallAtK(base, top, k)
			contributing++
		}

		p50, p95 := latencyPercentiles(latencies)
		recall := 0.0
		if contributing > 0 {
			recall = recallSum / float64(contributing)
		}
		result := SweepResult{
			Point: point, IndexBuildTime: buildTime, RecallAtK: recall,
			P50Latency: p50, P95Latency: p95, Queries: contributing,
		}
		results = append(results, result)
		if onResult != nil {
			onResult(result)
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
