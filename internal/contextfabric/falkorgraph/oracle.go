package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// oracle.go implements the CHAOS-3831 exact-search retrieval baseline: a
// brute-force TRUE COSINE ranking over the SAME org-scoped,
// embedder-fence-passing subject corpus the ANN index serves.
//
// This is measurement infrastructure, not a retrieval mechanism the
// production read path calls. It lives beside vector.go (rather than in a
// _test.go file) so it gets ordinary unit-test coverage against fakeConn like
// every other adapter method, while oracle_live_test.go is what actually
// invokes it against a real corpus.
//
// TRUE COSINE, NOT DOT PRODUCT. embedprovider writes the embedding provider's
// response values onto Subject.embedding VERBATIM -- there is no
// L2-normalization anywhere on the write path (provider.go:151-158, verified
// per the embed-text spec's §8 review log). A bare dot product over
// unnormalized vectors is only proportional to cosine similarity when every
// vector shares the same norm, which is not guaranteed across rows -- so an
// oracle computing a dot product would rank differently from the cosine
// metric FalkorDB's own vector index uses (vectorSimilarityCosine, vector.go)
// wherever norms differ. trueCosineSimilarity always divides by both norms.

// oracleVector is one fence-passing subject's canonical identity plus its
// stored, decoded embedding.
type oracleVector struct {
	Kind        string
	CanonicalID string
	Label       string
	Vector      []float64
}

// oracleMatch is one scored row of a brute-force ranking.
type oracleMatch struct {
	oracleVector
	Similarity float64
}

// trueCosineSimilarity computes dot(a,b) / (norm(a) * norm(b)). Either vector
// having zero norm makes the angle undefined; that is reported as similarity
// 0 (the same "no signal" convention vectorRelevanceFloor and
// embedprovider.CosineFromDistance's clamp use elsewhere) rather than NaN or
// a panic, since a zero vector cannot be a genuine embedding output.
//
// Mismatched lengths are also reported as 0 -- comparing across dimensions is
// meaningless, and this function has no config to consult for what the
// correct dimension is (that is the AC-3778-7 fence's job, upstream of this
// call).
func trueCosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	cosine := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	// Defensive clamp against floating-point drift pushing a near-parallel
	// pair fractionally outside [-1, 1], the same posture
	// embedprovider.CosineFromDistance takes on its own output.
	if cosine > 1 {
		return 1
	}
	if cosine < -1 {
		return -1
	}
	return cosine
}

// decodeVectorProperty converts a decoded graph property value into a
// []float64, accepting every numeric list shape the client boundary can
// plausibly hand back (client.go's decodeValue turns a compact-protocol
// array into []interface{} of float64; a fakeConn-based unit test or a future
// codec change may hand back a typed []float32/[]float64 slice directly).
// ok=false means the property is absent, not a vector, or carries a
// non-numeric element -- the caller must treat that node as having no usable
// embedding rather than guess.
func decodeVectorProperty(value interface{}) ([]float64, bool) {
	switch v := value.(type) {
	case []float64:
		return append([]float64(nil), v...), true
	case []float32:
		out := make([]float64, len(v))
		for i, f := range v {
			out[i] = float64(f)
		}
		return out, true
	case []interface{}:
		out := make([]float64, len(v))
		for i, item := range v {
			switch n := item.(type) {
			case float64:
				out[i] = n
			case float32:
				out[i] = float64(n)
			case int64:
				out[i] = float64(n)
			case int:
				out[i] = float64(n)
			default:
				return nil, false
			}
		}
		return out, true
	default:
		return nil, false
	}
}

// errOracleEmbedderRequired is returned when the corpus fetch is asked for
// without an embedder attached -- there is no embedder-identity fence to
// scope the corpus to, and no query vector to rank it against either.
var errOracleEmbedderRequired = errors.New("exact-search oracle requires an embedder to define the embedder-fence-passing corpus")

// oracleFetchBatchSize bounds each page of fetchEmbedderFenceCorpus's
// row-returning query, well under FalkorDB's server-config RESULTSET_SIZE
// (dev default 10,000; CHAOS-3849). A single unbounded `RETURN n` over a
// large org is SILENTLY truncated at that limit -- no error, just fewer rows
// than exist -- so every page must stay comfortably below it regardless of
// what a given deployment's RESULTSET_SIZE happens to be set to.
const oracleFetchBatchSize = 5000

// fetchEmbedderFenceCorpus reads every Subject node in this organization that
// carries an embedding written under the CURRENTLY configured embedder
// identity -- exactly the corpus db.idx.vector.queryNodes searches once the
// AC-3778-7 read fence (ensureVectorReadable) has passed. See the embed-text
// spec §5 L1: the oracle must rank the same candidate universe the ANN path
// can return, or an oracle-vs-ANN delta could be confounded with a
// fence/authorization difference instead of measuring ANN loss.
//
// The org predicate is applied in the WHERE clause, not as a post-filter
// (contrast vectorSearchNodes, which must over-fetch past a k-NN's own top-k
// because the org check runs after the server picks the k nearest rows).
// Brute force has no top-k to filter after, so there is nothing to
// over-fetch: the corpus IS the full org- and identity-scoped result set.
//
// CHAOS-3849: a single `MATCH ... RETURN n` with no LIMIT is silently capped
// by FalkorDB's server-config RESULTSET_SIZE (dev default 10,000) -- observed
// live, 10,000 of 35,986 rows returned with no error at all. That silently
// shrinks the oracle's own contract ("the corpus IS the full org- and
// identity-scoped result set", above) into an arbitrary prefix of it, which
// would read as a mass ANN loss the ANN path never actually has. Two
// independent guards close this:
//
//  1. Pagination: rows are fetched oracleFetchBatchSize at a time (well under
//     RESULTSET_SIZE) behind a stable ORDER BY n.kind, n.canonical_id --
//     stable because (org, kind, canonical_id) is the Subject uniqueness
//     constraint (identity.go's bootstrap), so no two Subject rows in one
//     org's graph can tie on it and no page boundary can duplicate or skip a
//     row. Fetching continues until a short page (fewer than
//     oracleFetchBatchSize rows) is seen.
//  2. Verification: count(n) -- an AGGREGATE, and so NOT subject to
//     RESULTSET_SIZE (a scalar row, not a bulk result set) -- is run under
//     the identical predicate, and the assembled corpus size plus the
//     skipped-malformed count from step 1 must equal it exactly, or the
//     fetch hard-fails via errOracleCorpusSizeMismatch. This is the closure
//     guarantee: ANY future silent cap (RESULTSET_SIZE config drift, a
//     driver change that reintroduces an implicit limit, ...) becomes a loud
//     failure here instead of a quietly shrunken universe.
//
// Parameterized $skip/$limit is exercised live against real FalkorDB, not
// merely against fakeConn: the 2026-08-15 06:04 run paginated a real
// 35,986-vector org to exactly that count across 8 pages of
// oracleFetchBatchSize, and the count(n) closure check passed -- a server
// that rejected parameterized SKIP/LIMIT would have errored the fetch, not
// silently returned the right total, so this is not a "fix it back to a
// literal" candidate.
//
// Both this query AND countEmbedderFenceCorpus's also require
// n.<kind>/n.<canonical-id> IS NOT NULL (CHAOS-3849 round 3): FalkorDB's own
// uniqueness constraint on (org, kind, canonical_id) does not apply to a row
// where kind or canonical_id is NULL, so a NULL-keyed row is not covered by
// the ORDER BY's totality guarantee in point 1 above and could duplicate or
// vanish across a page boundary while the row COUNT stayed accidentally
// balanced -- a corruption the count(n) check alone cannot catch, because it
// only verifies the total, not that the total was assembled correctly.
// Excluding NULL-keyed rows at the predicate level, identically on both
// queries, removes them from consideration on both sides of the closure
// check instead of relying on pagination to order what the constraint does
// not cover.
//
// Malformed rows (a present-but-undecodable kind/canonical-id/embedding --
// NULL-keyed rows are now excluded by the predicate above, not encountered
// here) are still skipped rather than failing the whole fetch -- one corrupt
// node must not blank out a 36k-vector baseline the rest of the corpus could
// still support -- but the skipped count is now load-bearing: it is part of
// what step 2 reconciles against count(n), and is logged (slog, Warn)
// whenever it is nonzero.
func (a *Adapter) fetchEmbedderFenceCorpus(ctx context.Context, key, orgID string) ([]oracleVector, error) {
	if a.embedder == nil {
		return nil, errOracleEmbedderRequired
	}
	// The TAGGED identity (CHAOS-3833): writeNodeVector stamps and the read
	// fence verifies identity.String()+"#"+composition-tag, so the corpus
	// predicate must compare the same string -- the bare Identity().String()
	// matches nothing the write path ever stamped, which would blank the
	// oracle corpus and read as "ANN lost everything".
	identity := a.stampedEmbedderIdentity(a.embedder.Identity())
	predicateParams := map[string]interface{}{"org": orgID, "identity": identity}

	expected, err := a.countEmbedderFenceCorpus(ctx, key, predicateParams)
	if err != nil {
		return nil, err
	}

	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org}) WHERE n.%s IS NOT NULL AND n.%s = $identity "+
			"AND n.%s IS NOT NULL AND n.%s IS NOT NULL "+
			"RETURN n ORDER BY n.%s, n.%s SKIP $skip LIMIT $limit",
		labelSubject, propOrgID, propEmbedding, propEmbedderIdentity, propKind, propCanonicalID, propKind, propCanonicalID,
	)

	corpus := make([]oracleVector, 0, expected)
	skippedMalformed := 0
	for skip := 0; ; skip += oracleFetchBatchSize {
		pageParams := map[string]interface{}{
			"org": orgID, "identity": identity, "skip": skip, "limit": oracleFetchBatchSize,
		}
		rows, err := a.api.query(ctx, key, cypher, pageParams, true)
		if err != nil {
			return nil, safeDependencyError("fetch exact-search oracle corpus", err)
		}
		for _, r := range rows {
			n, ok := r["n"].(*node)
			if !ok || n == nil {
				skippedMalformed++
				continue
			}
			// The predicate's IS NOT NULL guards keep a NULL-keyed row out of
			// rows entirely (round 3); this still catches the narrower case
			// of a present-but-non-string or empty-string kind/canonical-id,
			// which decodes to "" here without having been NULL server-side.
			kind := propStringValue(n.Properties[propKind])
			canonicalID := propStringValue(n.Properties[propCanonicalID])
			if kind == "" || canonicalID == "" {
				skippedMalformed++
				continue
			}
			vector, ok := decodeVectorProperty(n.Properties[propEmbedding])
			if !ok || len(vector) == 0 {
				skippedMalformed++
				continue
			}
			corpus = append(corpus, oracleVector{
				Kind: kind, CanonicalID: canonicalID,
				Label: propStringValue(n.Properties[propLabel]), Vector: vector,
			})
		}
		if len(rows) < oracleFetchBatchSize {
			break
		}
	}

	if skippedMalformed > 0 {
		slog.Default().Warn("context_fabric: exact-search oracle corpus fetch skipped malformed Subject row(s)",
			"org", orgID, "skipped_malformed", skippedMalformed, "usable", len(corpus))
	}

	if assembled := len(corpus) + skippedMalformed; assembled != expected {
		return nil, fmt.Errorf("%w: assembled %d usable + %d skipped-malformed = %d rows, but count(n) reports %d for org %s (a FalkorDB RESULTSET_SIZE truncation or a fetch/count race is the likely cause)",
			errOracleCorpusSizeMismatch, len(corpus), skippedMalformed, assembled, expected, orgID)
	}

	return corpus, nil
}

// errOracleCorpusSizeMismatch is the CHAOS-3849 closure guarantee:
// fetchEmbedderFenceCorpus hard-fails with this (wrapped with counts) rather
// than silently returning a truncated corpus whenever the assembled row
// count disagrees with the independent count(n) check. See
// fetchEmbedderFenceCorpus's doc comment.
var errOracleCorpusSizeMismatch = errors.New("exact-search oracle corpus assembly does not match its own count(*) verification")

// countEmbedderFenceCorpus runs the aggregate count(*) counterpart of
// fetchEmbedderFenceCorpus's row-returning query, under the IDENTICAL
// predicate -- including the CHAOS-3849 round-3 NULL-key guards on kind and
// canonical-id, which must match the paginated query exactly or the two
// sides of the closure check would be counting different row sets, not
// verifying the same one. An aggregate result is a single scalar row, not a
// bulk result set, so it is NOT subject to FalkorDB's RESULTSET_SIZE cap --
// this is what makes it a trustworthy verification oracle for the paginated
// fetch rather than just another query that could itself be silently
// truncated.
func (a *Adapter) countEmbedderFenceCorpus(ctx context.Context, key string, predicateParams map[string]interface{}) (int, error) {
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org}) WHERE n.%s IS NOT NULL AND n.%s = $identity "+
			"AND n.%s IS NOT NULL AND n.%s IS NOT NULL "+
			"RETURN count(n) AS total",
		labelSubject, propOrgID, propEmbedding, propEmbedderIdentity, propKind, propCanonicalID,
	)
	rows, err := a.api.query(ctx, key, cypher, predicateParams, true)
	if err != nil {
		return 0, safeDependencyError("count exact-search oracle corpus", err)
	}
	if len(rows) != 1 {
		return 0, fmt.Errorf("exact-search oracle corpus count query returned %d rows, want exactly 1", len(rows))
	}
	total, ok := intFromCount(rows[0]["total"])
	if !ok {
		return 0, fmt.Errorf("exact-search oracle corpus count query returned a non-numeric total: %#v", rows[0]["total"])
	}
	return total, nil
}

// intFromCount converts a decoded count(*) scalar into an int, accepting
// every numeric shape the client boundary can plausibly hand back for an
// aggregate -- the same defensive posture indexStatus.embeddingOption
// already takes for a different aggregate-shaped value (that one verified
// live to vary between int64/int/float64), rather than assuming count(n)
// always decodes as exactly one of them.
//
// codex R2 (Medium, confirmed, CHAOS-4311): a count(n) is a cardinality --
// negative, fractional, or non-finite is never a legitimate value, only a
// malformed response. The float64 case previously ran a bare int(v)
// conversion: a NaN/Inf produces platform-defined garbage, and a fractional
// value like 1.9 silently truncates to 1, which the census's own
// count-closure check (confirmedKindVectorCensus: enumeratedCount+
// malformedCount != populationCount) could then satisfy against a single
// genuinely-fetched row -- reading as a sound Complete census when the
// count query actually returned nonsense. Once CHAOS-4311 makes Complete
// decision-bearing, this helper's own permissiveness became outcome-
// affecting rather than merely a measurement-oracle quirk, so every shape
// here now requires a non-negative, finite, exact integer value -- ok=false
// otherwise, which every caller already treats as a hard query-format
// error (countKindEmbedderFenceCorpus/countEmbedderFenceCorpus), failing
// the census/oracle closed rather than proceeding on a garbage count.
func intFromCount(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int64:
		if v < 0 {
			return 0, false
		}
		return int(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return v, true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}

// bruteForceRank scores every member of corpus against query by true cosine
// similarity and returns all of them sorted by DESCENDING similarity (ties
// broken by kind then canonical ID, for deterministic output). Callers slice
// the prefix they need -- top-K for a recall check, or the whole thing for a
// hard-negative harvest -- rather than this function baking in a K.
func bruteForceRank(query []float64, corpus []oracleVector) []oracleMatch {
	matches := make([]oracleMatch, len(corpus))
	for i, candidate := range corpus {
		matches[i] = oracleMatch{oracleVector: candidate, Similarity: trueCosineSimilarity(query, candidate.Vector)}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Similarity != matches[j].Similarity {
			return matches[i].Similarity > matches[j].Similarity
		}
		if matches[i].Kind != matches[j].Kind {
			return matches[i].Kind < matches[j].Kind
		}
		return matches[i].CanonicalID < matches[j].CanonicalID
	})
	return matches
}

// findVector returns the corpus entry for (kind, canonicalID), if the
// corresponding subject was embeddable and fence-passing. ok=false covers
// both "never existed" and "existed but has no usable vector under the
// current identity" -- callers that need to tell those apart (the
// oracle_live_test.go driver's subject_missing vs vector_missing causes) use
// subjectExistence instead, because neither is a statement about embedding
// SEMANTICS the way text_loss/ann_loss are.
func findVector(corpus []oracleVector, kind, canonicalID string) (oracleVector, bool) {
	for _, candidate := range corpus {
		if candidate.Kind == kind && candidate.CanonicalID == canonicalID {
			return candidate, true
		}
	}
	return oracleVector{}, false
}

// containsSubject reports whether matches (an ANN or oracle result list)
// contains (kind, canonicalID) anywhere in it -- used for a recall@K check
// where the caller has already sliced matches to length K.
func containsSubject(matches []oracleMatch, kind, canonicalID string) bool {
	for _, m := range matches {
		if m.Kind == kind && m.CanonicalID == canonicalID {
			return true
		}
	}
	return false
}

// containsANNCandidate reports whether an ANN result list (as returned by
// vectorSearchNodes) contains (kind, canonicalID) -- the ANN-side half of the
// recall@K check oracle_live_test.go's decomposition runs alongside
// containsSubject's oracle-side check. Reading kind/canonical-id back out of
// CandidateNode.Attributes rather than UUID keeps this independent of
// subjectUUID's own encoding.
func containsANNCandidate(candidates []graphrank.CandidateNode, kind, canonicalID string) bool {
	for _, c := range candidates {
		if propStringValue(c.Attributes[propKind]) == kind && propStringValue(c.Attributes[propCanonicalID]) == canonicalID {
			return true
		}
	}
	return false
}

// aboveFloor mirrors vectorSearchNodes' AC-3778-4 drop rule EXACTLY -- a
// similarity AT OR BELOW tau is dropped, never scored (vector.go's
// `if similarity <= tau { continue }`) -- so a recall@K check run over this
// filtered slice answers the same question the real floor asks, rather than
// "what would recall be with no floor at all" (codex round-1 finding 2).
// ranked is assumed already sorted descending; filtering preserves that
// order.
//
// S+/S- reporting deliberately does NOT go through this filter -- L4's tau
// calibration needs the RAW similarity of the correct pair, sub-floor
// included, to see how many correct pairs the current floor is discarding.
// Only the retrievability check (oracleHit) applies the floor.
func aboveFloor(ranked []oracleMatch, tau float64) []oracleMatch {
	out := make([]oracleMatch, 0, len(ranked))
	for _, m := range ranked {
		if m.Similarity > tau {
			out = append(out, m)
		}
	}
	return out
}

// topKInclusive returns the prefix of a DESCENDING-sorted ranking needed to
// answer "is this row within the top K" without being sensitive to how ties
// AT the K-th boundary happen to be ordered (codex round-1 finding 3).
//
// bruteForceRank's own tie-break is deterministic (kind then canonical ID)
// but that ordering has no reason to agree with how a real ANN server orders
// an exact tie, and duplicate/near-duplicate search text (the corpus has
// plenty -- see the embed-text spec §1's "1,121 near-clones" for
// pull_request_review) produces genuine floating-point ties routinely. A
// plain ranked[:k] slice would silently read a tied correct answer as a miss
// purely because of which side of the tie bruteForceRank's arbitrary
// tie-break put it on. Every row whose similarity EQUALS the K-th row's
// similarity is included instead, so a boundary tie can never read as a
// miss -- this can only ever WIDEN the set a recall check passes, never
// narrow it.
func topKInclusive(ranked []oracleMatch, k int) []oracleMatch {
	if k <= 0 {
		return nil
	}
	if k >= len(ranked) {
		return ranked
	}
	boundary := ranked[k-1].Similarity
	end := k
	for end < len(ranked) && ranked[end].Similarity == boundary {
		end++
	}
	return ranked[:end]
}

// subjectExistence distinguishes a corpus-authoring error (the expected
// subject never existed in the graph at all) from a genuine projection
// coverage gap (the subject exists but carries no usable embedding) --
// codex round-1 finding 4. findVector alone cannot tell these apart: it only
// reports "not in the fence-passing corpus", which is true of both.
//
// This must only be called once the caller has already confirmed the
// ORG-LEVEL AC-3778-7 fence passes (ensureVectorReadable). Under a passed
// fence, any node that DOES carry an embedding is guaranteed to carry the
// CURRENT identity (that is what the fence verifies), so "exists, has an
// embedding property, but is absent from fetchEmbedderFenceCorpus's result"
// cannot happen -- reported as embedded=false defensively rather than
// assumed impossible, since a defensive false negative here is far cheaper
// than a wrong panic.
//
// temporalFilter{} (the zero value) is deliberately inactive: this is an
// existence check against the current graph, not a historical-axis query --
// the oracle has no time axis of its own to bind.
func (a *Adapter) subjectExistence(ctx context.Context, key, orgID, kind, canonicalID string) (exists, embedded bool, err error) {
	n, err := a.nodeByKindID(ctx, key, orgID, kind, canonicalID, temporalFilter{})
	if err != nil {
		return false, false, err
	}
	if n == nil {
		return false, false, nil
	}
	_, hasVector := decodeVectorProperty(n.Properties[propEmbedding])
	return true, hasVector, nil
}

// bestWrongNeighbor returns the highest-similarity entry in a
// (already-sorted-descending) ranking whose identity is NOT (kind,
// canonicalID) -- the S- "best imposter" the embed-text spec's L4 hard-negative
// mining input needs. ok=false only when ranked is empty or every entry IS
// the correct answer (a one-node corpus).
func bestWrongNeighbor(ranked []oracleMatch, kind, canonicalID string) (oracleMatch, bool) {
	for _, m := range ranked {
		if m.Kind == kind && m.CanonicalID == canonicalID {
			continue
		}
		return m, true
	}
	return oracleMatch{}, false
}
