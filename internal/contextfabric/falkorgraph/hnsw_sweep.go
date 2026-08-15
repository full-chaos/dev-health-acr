package falkorgraph

import (
	"context"
	"errors"
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

// referenceOverfetch is the STARTING multiplier for the reference build's
// tie-completion fetch (referenceTopKTieComplete) -- the first, cheapest
// guess at how far past k a tie group might extend, doubled from there only
// if the window's own edge is still tied (Luna round-2 finding 3: a FIXED
// 2x window is not enough on its own -- with more than 2*k duplicates, real
// on this 78%-near-duplicate corpus, ids beyond the window scored as misses
// even though they were equally valid neighbors).
const referenceOverfetch = 2

// maxReferenceTieMultiplier bounds how many times referenceTopKTieComplete
// will double its fetch window chasing a tie group before giving up (Luna
// round-2 finding 3's required hard bound). 64x (1,280 rows at k=20) is far
// past any plausible tie group even on this corpus's worst known case (the
// 27,902-row ci_pipeline_run block, spec §1) while staying bounded; a tie
// group that survives 64 doublings is treated as a genuine measurement
// failure for that seed, never silently truncated.
const maxReferenceTieMultiplier = 64

// errReferenceTieUnbounded classifies referenceTopKTieComplete hitting
// maxReferenceTieMultiplier while the k-th boundary score is STILL tied with
// the last fetched row -- the tie group could not be proven complete.
var errReferenceTieUnbounded = errors.New("context fabric hnsw sweep: reference tie group did not resolve within the bounded overfetch window")

// referenceTopKTieComplete fetches a reference build's top-K for seed,
// growing the raw fetch window (doubling from referenceOverfetch) until the
// k-th boundary score is DEFINITIVELY not tied with the window's last row --
// i.e. the tie group spanning the boundary is proven complete, however large
// it turns out to be -- or maxReferenceTieMultiplier is reached, in which
// case it returns errReferenceTieUnbounded rather than silently comparing
// candidates against a truncated, possibly-incomplete tie class (Luna
// round-2 finding 3).
//
// Only the REFERENCE build needs this: a candidate build's own top-k is
// compared AS RETURNED (RecallAtKTieTolerant is asymmetric on purpose, see
// hnsw_recall.go) -- tie-completeness only matters for the side being used
// as "what counts as correct."
func (a *Adapter) referenceTopKTieComplete(ctx context.Context, key, seed string, k int) ([]ScoredID, time.Duration, error) {
	var totalLatency time.Duration
	multiplier := referenceOverfetch
	for {
		top, latency, err := a.vectorSweepSeedTopK(ctx, key, seed, k*multiplier)
		totalLatency += latency
		if err != nil {
			return nil, totalLatency, err
		}
		if k > len(top) {
			// Either fewer rows exist for this key than k (nothing to fetch
			// further) or the server returned short for another reason --
			// either way, there is no larger window to chase a tie into.
			return top, totalLatency, nil
		}
		if len(top) < k*multiplier {
			// The server returned FEWER rows than asked for -- this key's
			// entire corpus was exhausted, so no tie group can extend past
			// what was just fetched.
			return top, totalLatency, nil
		}
		boundary := top[k-1].Score
		if top[len(top)-1].Score > boundary {
			// The window's own last row is already strictly worse than the
			// boundary -- the tie group ends inside this window, proven.
			return top, totalLatency, nil
		}
		if multiplier >= maxReferenceTieMultiplier {
			return nil, totalLatency, fmt.Errorf("%w: seed %q's tie group at k=%d did not resolve within %dx overfetch (%d rows)",
				errReferenceTieUnbounded, seed, k, multiplier, k*multiplier)
		}
		multiplier *= 2
	}
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
// every seed's SCORED top-K (tie-complete, see referenceTopKTieComplete) as
// the comparison baseline; every subsequent build is measured against that
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

		latencies := make([]time.Duration, 0, len(seedCanonicalIDs))
		var recallSum float64
		contributing := 0
		skipped := 0
		for _, seed := range seedCanonicalIDs {
			if isReference {
				// referenceTopKTieComplete grows its fetch until the k-th
				// boundary's tie group is PROVEN complete (Luna round-2
				// finding 3) rather than a fixed 2x window. Its own
				// unresolved-tie failure (errReferenceTieUnbounded) is
				// routed through the SAME skipped-seed accounting as any
				// other query error -- fail-closed, same principle as round-
				// 1 finding 1: an unresolved tie is not a clean answer this
				// seed can contribute, so it must not silently score as
				// either a hit or a miss, and if it dominates the seed set
				// the existing zero-coverage check (below) already surfaces
				// that loudly rather than needing a second error path.
				top, latency, err := a.referenceTopKTieComplete(ctx, key, seed, k)
				if err != nil {
					skipped++
					continue
				}
				latencies = append(latencies, latency)
				referenceTop[seed] = top
				recallSum += 1 // a setting is perfectly recalled against itself, by definition.
				contributing++
				continue
			}
			top, latency, err := a.vectorSweepSeedTopK(ctx, key, seed, k)
			if err != nil {
				skipped++
				continue
			}
			latencies = append(latencies, latency)
			base, ok := referenceTop[seed]
			if !ok {
				// The reference query (or its tie-completion) for this exact
				// seed also failed -- counted as skipped here too, there is
				// nothing to compare against, and this must not silently
				// read as a match OR a miss.
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

// isSweepTargetSafe reports whether targetKey may be targeted by
// RunHNSWSweep's destructive drop/recreate cycle, and if not, why.
//
// FAIL-CLOSED, no list to maintain (Luna round-2 finding 1 -- round-1's fix
// was a hardcoded, EXACT-match denylist of ONE known key, which Luna showed
// still fails OPEN: a changed graph prefix, a different organization, or
// simply an org this lane's author did not happen to hardcode all sail
// straight through a denylist that only ever covered one literal string.
// A list of known-bad keys can never be a proof of safety for a key NOT on
// it; only a comparison against what the key SHOULD be can.
//
// The fix: derive the org's actual production graph key at runtime, via
// graphKey -- the EXACT SAME derivation identity.go uses for every real
// projection write and query, never a copy of the logic that could drift
// from it -- and refuse outright if targetKey equals that derived value.
// There is nothing to keep in sync, because there is nothing hardcoded.
//
// TWO conditions, BOTH required, in this order:
//  1. targetKey must EXACTLY equal expectedCopyKey -- an operator-declared
//     value, independently typed, that must match the key this run is
//     ACTUALLY about to hit. This is a "state your intent twice" check, not
//     a naming heuristic: it catches a copy-paste/env-var mismatch between
//     what the operator meant to target and what ACR_TEST_HNSW_SWEEP_GRAPH_KEY
//     actually holds, with no assumption at all about what a safe name looks
//     like. The substring ("contains copy") heuristic from round 1 is
//     REMOVED entirely -- once a derivation-based comparison exists it adds
//     nothing and Luna named it as dead weight.
//  2. targetKey must NOT equal graphKey(graphPrefix, orgID) -- the derived
//     production key for the org this run declares it is sweeping.
//
// UNDERIVABLE = REFUSE. An empty graphPrefix, orgID, or expectedCopyKey
// means condition 2 (or 1) cannot be evaluated at all, and an unevaluable
// safety check is refused, never silently skipped or treated as passing.
func isSweepTargetSafe(targetKey, expectedCopyKey, graphPrefix, orgID string) (bool, string) {
	if strings.TrimSpace(graphPrefix) == "" || strings.TrimSpace(orgID) == "" {
		return false, "graph prefix or organization id is empty -- cannot derive the production graph key to compare against, refusing rather than allowing an unevaluable check to pass"
	}
	if strings.TrimSpace(expectedCopyKey) == "" {
		return false, "no expected copy key was declared -- refusing rather than trusting the target key alone"
	}
	if targetKey != expectedCopyKey {
		return false, fmt.Sprintf("target key %q does not exactly match the independently declared expected copy key %q", targetKey, expectedCopyKey)
	}
	production := graphKey(graphPrefix, orgID)
	if targetKey == production {
		return false, fmt.Sprintf("target key %q IS the derived production graph key for organization %q -- refusing", targetKey, orgID)
	}
	return true, ""
}
