package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// Vector retrieval property names, written at projection time and read back
// by the search below. Kept beside the other property literals in identity.go
// in spirit, but grouped here so the whole vector concern reads in one place.
const (
	propEmbedding          = "embedding"
	propEmbedderIdentity   = "embedder_identity"
	propEmbedderDimension  = "embedder_dimension"
	vectorSimilarityCosine = "cosine"
)

// vectorRelevanceFloor and vectorRelevanceCeiling bound the confidence band a
// vector hit can ever normalize into (CHAOS-3778, the vector analogue of
// AC-3778-0's lexical [0.50, 0.75] band).
//
// The CEILING is the load-bearing constant. At 0.70 it sits strictly below
// graphrank.ResolveFromMergedCandidates' 0.72 lone-candidate gate, which makes
// AC-3778-3 -- "a vector hit alone never commits a subject" -- true by
// ARITHMETIC rather than by a rule someone could later special-case away.
// There is no similarity, however perfect, that lets a vector-only candidate
// auto-commit. Raising this constant to 0.72 or above would silently repeal
// AC-3778-3; graphrank's own TestD11Class_NoVectorOnlyConfidenceCanReachTheCommitGate
// and TestAC_3778_3_VectorOnlyCandidateCannotReachTheLoneCommitGate both fail
// if it is.
//
// The FLOOR matches the lexical band's floor for the same reason that one was
// chosen: a genuine hit must never read as "no signal" (0). A vector hit that
// only just clears the similarity floor is weak evidence, not absent evidence.
//
// A vector hit reaches a committing confidence only by CORROBORATION -- some
// second, distinct mechanism proposing the same subject, which lifts it into
// graphrank's [0.72, 0.86] corroborated band. See
// graphrank.CorroboratedConfidence.
const (
	vectorRelevanceFloor   = 0.50
	vectorRelevanceCeiling = 0.70
)

// vectorRelevanceFromSimilarity maps ONE candidate's cosine similarity into
// the documented [vectorRelevanceFloor, vectorRelevanceCeiling] band, given
// the configured absolute similarity floor tau.
//
// Like fulltextRelevanceFromMatchedTerms, this is an ABSOLUTE, per-candidate,
// EXACT function: it depends only on this candidate's own similarity, never on
// what else came back in the same query's result set, and never on which query
// produced it. That is the property Codex rounds 2 and 3 forced onto the
// lexical arm (a per-call-relative normalization produced confidences that
// were only comparable within one result set, while ResolveSubjects' merge
// compares across calls), and it is honored here from the start rather than
// rediscovered.
//
// A similarity at or below tau maps to the floor rather than below it. The
// caller is expected to have DROPPED such a candidate already (see
// vectorSearchNodes) -- this is the same defensive clamping posture the
// lexical arm uses, where every failure mode is a failure toward LESS
// confidence, never more.
func vectorRelevanceFromSimilarity(similarity, tau float64) float64 {
	if tau >= 1 {
		return vectorRelevanceFloor
	}
	if similarity <= tau {
		return vectorRelevanceFloor
	}
	if similarity > 1 {
		similarity = 1
	}
	proportion := (similarity - tau) / (1 - tau)
	return vectorRelevanceFloor + (vectorRelevanceCeiling-vectorRelevanceFloor)*proportion
}

// hnswIndexOptions carries the HNSW build parameters FalkorDB's vector index
// accepts beyond dimension/similarityFunction (CHAOS-3832, measurement-only:
// see hnsw_sweep.go). A zero field is OMITTED from the OPTIONS clause rather
// than sent as a literal 0, so the server's own default applies -- verified
// live that the production index (created with a bare hnswIndexOptions{})
// reports the server defaults back via db.indexes() (M:16, efConstruction:200,
// efRuntime:10 on the pinned graph module 42002). Production bootstrap
// (ensureVectorIndex) always passes the zero value; nothing here changes what
// createVectorIndex has always sent.
type hnswIndexOptions struct {
	M              int
	EfConstruction int
	EfRuntime      int
}

func (o hnswIndexOptions) String() string {
	return fmt.Sprintf("M=%d,efConstruction=%d,efRuntime=%d", o.M, o.EfConstruction, o.EfRuntime)
}

// createVectorIndex creates the per-organization vector index on
// Subject.embedding with the server's default HNSW parameters. It is the
// single production call site (ensureVectorIndex) and is unchanged by
// CHAOS-3832 -- a thin wrapper over createVectorIndexWithOptions passing the
// zero hnswIndexOptions, which was verified to render the IDENTICAL OPTIONS
// clause this function always sent (TestCreateVectorIndexZeroOptionsCypherUnchanged).
//
// Verified live (graph module 42002) that the Cypher form below works and that
// creation is NOT idempotent -- a repeat call errors "Attribute 'embedding' is
// already indexed", exactly like a range index -- so an already-exists error is
// treated as success the same way createIndex and createFulltextIndex do.
//
// Also verified live: this index coexists with the fulltext index on
// Subject.search_text, and both keep working. The two are independent
// retrieval mechanisms over the same nodes, which is precisely the design.
func (a *Adapter) createVectorIndex(ctx context.Context, key string, dimension int) error {
	return a.createVectorIndexWithOptions(ctx, key, dimension, hnswIndexOptions{})
}

// createVectorIndexWithOptions is createVectorIndex generalized to accept
// explicit HNSW parameters (CHAOS-3832 T2, measurement-only: no production
// caller passes a non-zero hnswIndexOptions today). Every non-zero field is
// appended to the OPTIONS clause; a zero field is left out so the server picks
// its own default rather than this code silently pinning one.
func (a *Adapter) createVectorIndexWithOptions(ctx context.Context, key string, dimension int, opts hnswIndexOptions) error {
	clause := fmt.Sprintf("dimension:%d, similarityFunction:'%s'", dimension, vectorSimilarityCosine)
	if opts.M > 0 {
		clause += fmt.Sprintf(", M:%d", opts.M)
	}
	if opts.EfConstruction > 0 {
		clause += fmt.Sprintf(", efConstruction:%d", opts.EfConstruction)
	}
	if opts.EfRuntime > 0 {
		clause += fmt.Sprintf(", efRuntime:%d", opts.EfRuntime)
	}
	cypher := fmt.Sprintf(
		"CREATE VECTOR INDEX FOR (n:%s) ON (n.%s) OPTIONS {%s}",
		labelSubject, propEmbedding, clause,
	)
	_, err := a.api.query(ctx, key, cypher, nil, false)
	if err == nil {
		return nil
	}
	if isAlreadyExists(err) {
		return nil
	}
	return safeDependencyError("bootstrap vector index", err)
}

// dropVectorIndex removes the vector index on Subject.embedding, leaving the
// embedding/embedder_identity/embedder_dimension NODE PROPERTIES untouched --
// verified live (CHAOS-3832 §7 D3 probe): dropping the index does not clear a
// single node's stored vector, only the index structure over it. Recreating
// the index afterward (createVectorIndexWithOptions) re-indexes those SAME
// property values with no re-embedding involved.
//
// Dropping an already-absent index is treated as success -- errIndexNotFound
// classifies FalkorDB's "no such index" rejection -- mirroring
// createVectorIndex's already-exists tolerance in the opposite direction, so a
// caller can call dropVectorIndex unconditionally before recreating.
//
// Never called from any production path today: ensureVectorIndex only
// creates, it never drops. This exists for the CHAOS-3832 sweep tooling
// (hnsw_sweep.go) and its live probe.
func (a *Adapter) dropVectorIndex(ctx context.Context, key string) error {
	cypher := fmt.Sprintf("DROP VECTOR INDEX FOR (n:%s) ON (n.%s)", labelSubject, propEmbedding)
	_, err := a.api.query(ctx, key, cypher, nil, false)
	if err == nil {
		return nil
	}
	if errors.Is(err, errIndexNotFound) {
		return nil
	}
	return safeDependencyError("drop vector index", err)
}

// currentVectorIndexHNSWOptions reads the M/efConstruction/efRuntime the
// TARGET key's vector index currently reports, best-effort: any index row
// found on Subject.embedding is read regardless of its Status (unlike
// vectorIndexDimension's strict OPERATIONAL allowlist), because this is used
// only to capture "whatever was there before" for a possible restore, not to
// make a production trust decision. ok=false means no vector index exists on
// this key at all (nothing to restore to).
func (a *Adapter) currentVectorIndexHNSWOptions(ctx context.Context, key string) (hnswIndexOptions, bool, error) {
	indexes, err := a.api.indexes(ctx, key)
	if err != nil {
		return hnswIndexOptions{}, false, safeDependencyError("inspect vector index before recreate", err)
	}
	for _, index := range indexes {
		if index.Label != labelSubject {
			continue
		}
		types, ok := index.Types[propEmbedding]
		if !ok {
			continue
		}
		for _, indexType := range types {
			if strings.EqualFold(indexType, "VECTOR") {
				return index.HNSWOptions(), true, nil
			}
		}
	}
	return hnswIndexOptions{}, false, nil
}

// recreateVectorIndexWithOptions drops (tolerating absence) and recreates the
// vector index with the given HNSW parameters, then polls to OPERATIONAL --
// the CHAOS-3832 measurement primitive behind the T2 sweep. It never touches
// node properties, so it is safe to run repeatedly over the SAME stored
// vectors while sweeping efConstruction/efRuntime/M combinations (§7 D3: live
// probe confirmed a node's post-recreate nearest-neighbor result is byte-for-
// byte identical to its pre-recreate result).
//
// RESTORE ON FAILURE (Luna round-1 finding 2b, extended round-2 finding 2):
// the pre-drop options are read FIRST, before anything destructive happens.
// ANY failure AFTER the drop -- the CREATE erroring, OR create succeeding but
// pollVectorIndexOperational timing out/erroring -- takes the restore path.
// Round-1's fix only checked the create error and returned bare on a poll
// failure, which left the ORIGINAL config unrecovered exactly when the new
// index never confirmed operational: a dropped-and-never-restored (or
// restored-to-the-WRONG-config) index is a silent retrieval regression on
// top of whatever caused the failure. A failed restore is reported LOUDLY
// (wrapped into the returned error, never swallowed) rather than merely
// logged, because at that point the index is genuinely absent or in an
// unknown state and needs an operator's attention.
//
// The restore itself re-drops before recreating with the original options
// -- necessary, not decorative: if the new-options CREATE actually
// succeeded and only the POLL failed, an index already exists on the key,
// and createVectorIndexWithOptions('...',  original) would hit FalkorDB's
// "already indexed" rejection and be silently treated as success (the same
// idempotent tolerance createVectorIndex relies on) WITHOUT ever changing
// the index's options back to original -- a restore that reports success
// while doing nothing. Dropping first removes that stale new-options index
// so the restore's create is the one that actually takes effect.
//
// Not called from any production path. The only callers are the sweep runner
// and its tests/live probe.
func (a *Adapter) recreateVectorIndexWithOptions(ctx context.Context, key string, dimension int, opts hnswIndexOptions) error {
	original, hadOriginal, err := a.currentVectorIndexHNSWOptions(ctx, key)
	if err != nil {
		return fmt.Errorf("read current vector index options before recreate: %w", err)
	}
	if err := a.dropVectorIndex(ctx, key); err != nil {
		return err
	}

	createAndPoll := func(target hnswIndexOptions) error {
		if err := a.createVectorIndexWithOptions(ctx, key, dimension, target); err != nil {
			return err
		}
		return a.pollVectorIndexOperational(ctx, key)
	}

	failure := createAndPoll(opts)
	if failure == nil {
		return nil
	}
	if !hadOriginal {
		return fmt.Errorf("recreate vector index with new options (%s) failed: %w, and no prior index existed to restore", opts, failure)
	}
	if dropErr := a.dropVectorIndex(ctx, key); dropErr != nil {
		return fmt.Errorf("recreate vector index with new options (%s) failed (%v), AND could not drop it to attempt a restore (%v) -- "+
			"the vector index on key %q is in an UNKNOWN state, manual intervention required", opts, failure, dropErr, key)
	}
	if restoreErr := createAndPoll(original); restoreErr != nil {
		return fmt.Errorf("recreate vector index with new options (%s) failed (%v) AND restoring the original options (%s) ALSO failed (%v) -- "+
			"the vector index on key %q is now ABSENT, manual intervention required", opts, failure, original, restoreErr, key)
	}
	return fmt.Errorf("recreate vector index with new options (%s) failed: %w -- restored the original options (%s) successfully", opts, failure, original)
}

// vectorParam converts a float32 vector into the ONLY list shape
// falkordb-go's BuildParamsHeader/ToString accepts without panicking:
// []interface{} of float64 (see client.go's safeParams -- ToString was
// verified live to panic on float32 and on typed numeric slices). The pinned
// v2.1.0 codec is therefore untouched by vector support; this is one more
// ordinary parameter.
func vectorParam(vector []float32) []interface{} {
	values := make([]interface{}, len(vector))
	for i, value := range vector {
		values[i] = float64(value)
	}
	return values
}

// vectorSearchNodes runs an embedding-similarity search over Subject nodes'
// stored vectors, returning matches as CandidateNode with a normalized
// Relevance and contextfabric.MatchVector recorded as the mechanism.
//
// THE SCORE IS A DISTANCE. Verified live against graph module 42002:
// db.idx.vector.queryNodes yields a cosine DISTANCE in [0, 2] where 0 means
// identical, not a similarity. An identical vector scored 0.0 and an unrelated
// one scored 0.699398 (= 1 - cos for a cosine of 0.3007). Handing that number
// to graphrank.ResultConfidence unchanged would take its
// `score >= 0 && score <= 1 -> return score` arm and award the BEST match a
// confidence of 0.0 -- the D11 inversion again, in a new place. Every score is
// therefore converted by embedprovider.CosineFromDistance and written into
// Relevance; Score is deliberately left NIL rather than carrying the raw
// distance for diagnostics, because a distance sitting in Score is a loaded
// gun for any future caller that reads it as a confidence.
//
// THE ORG PREDICATE IS A POST-FILTER. Verified live: `WHERE node.org_id = $org`
// after the YIELD filters the k-NN result, it does not constrain the search.
// It is kept anyway -- it is the standing defence-in-depth rule for every read
// (Codex P2b), and the graph key already scopes the whole database to one
// organization -- but it means k must be over-fetched, exactly like the
// lexical path's limit+1.
//
// THE SIMILARITY FLOOR IS THE AC-3778-4 GUARD. A k-NN query always returns k
// rows when k rows exist; it has no notion of "nothing is close enough". A
// question about a subject that does not exist would therefore come back with
// k confident-looking neighbors, turning an honest no-match into a confident
// wrong subject -- the highest-severity failure named in this issue. Anything
// at or below tau is DROPPED here, before it can become a candidate at all.
//
// TRUNCATION follows the lexical path's contract exactly: one more row than the
// caller's budget is requested purely to detect a corpus with more matches than
// the budget can show, the extra row is discarded, and truncation is reported
// to graphrank as a property of the whole resolution (see
// ResolveFromMergedCandidates' searchTruncated). Note the interaction with the
// floor: rows dropped for being below tau do NOT make a result set truncated,
// because they were never competitors -- truncation asks "could a genuinely
// competing candidate have been cut off", and a sub-floor neighbor is by
// definition not one.
func (a *Adapter) vectorSearchNodes(ctx context.Context, key, orgID string, vector []float32, tau float64, limit int) ([]graphrank.CandidateNode, bool, error) {
	return a.vectorSearchNodesWithOverFetch(ctx, key, orgID, vector, tau, limit, 1)
}

// vectorSearchNodesWithOverFetch generalizes vectorSearchNodes with an
// explicit over-fetch multiplier (CHAOS-3832 T2 / spec §5 L3). vectorSearchNodes
// calls this with multiplier=1, which renders the IDENTICAL `limit+1` raw
// fetch size AND the identical `limit`-sized returned candidate set it always
// requested -- production behavior is unchanged byte for byte
// (TestVectorSearchNodesDelegatesToOverFetchMultiplierOne).
//
// L3's formula, exactly: the raw ANN fetch size is `(multiplier * limit) + 1`.
// codex round-2 P2: the multiplier must widen the RETURNED candidate pool too,
// not just the raw fetch. The earlier version fetched a wider pool but then
// unconditionally sliced the tau-surviving result back down to `limit` BEFORE
// returning -- since db.idx.vector.queryNodes' rows are already ORDER BY
// score ASC (closest first), that slice ALWAYS keeps exactly the `limit`
// closest-by-raw-cosine-distance survivors, regardless of multiplier. A
// subject that is genuinely farther than `limit` other DISTINCT subjects by
// raw vector distance could therefore never reach a caller's ranking --
// including graphrank's ResolveFromMergedCandidates, which is DESIGNED to
// rank the full merged candidate set (corroboration across mechanisms and
// terms) and truncate LAST (see that function's doc comment) -- because this
// function discarded it first, before graphrank ever saw it existed. No
// multiplier could ever fix that: over-fetching the RAW query is pointless if
// the RETURNED set is clipped back to the same size regardless.
//
// So the returned-candidate cap widens WITH the multiplier: `multiplier *
// limit`, not `limit`. At multiplier=1 that is exactly `limit` (unchanged).
// At multiplier>1, up to multiplier*limit tau-surviving candidates now flow
// out to hybridSearchNodes -> ResolveSubjects -> ResolveFromMergedCandidates,
// which is the ONLY place cross-mechanism ranking can happen (this function
// has no visibility into the lexical arm, other search terms, or
// corroboration) and already truncates to the FINAL response size
// (request.Options.MaxSubjectCandidates) after that ranking -- exactly the
// "rank first, truncate last" architecture this fix aligns with rather than
// duplicates.
//
// The trailing `+1` sentinel is unchanged in spirit, just re-scaled: it now
// sits one past the WIDENED cap (multiplier*limit+1 raw rows fetched),
// so truncated is still derived from whether more than the returned cap of
// candidates SURVIVE the filters below, never from the raw fetch size itself
// -- a wider pool can only ever recover more genuine candidates, it can never
// itself manufacture a truncated=true a narrower pool would not also have
// reported (review round 2 rec 2's caveat, restated here as code).
//
// multiplier <= 0 is treated as 1 (the production default), never as a
// zero-or-negative fetch size.
func (a *Adapter) vectorSearchNodesWithOverFetch(ctx context.Context, key, orgID string, vector []float32, tau float64, limit, multiplier int) ([]graphrank.CandidateNode, bool, error) {
	if len(vector) == 0 {
		return nil, false, nil
	}
	if limit <= 0 || limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	if multiplier <= 0 {
		multiplier = 1
	}
	// returnCap is how many tau-surviving candidates this call may return --
	// see the doc comment above for why this is no longer always `limit`.
	returnCap := multiplier * limit
	fetchK := returnCap + 1
	cypher := fmt.Sprintf(
		"CALL db.idx.vector.queryNodes('%s', '%s', %d, vecf32($vec)) YIELD node, score "+
			"WHERE node.%s = $org "+
			"RETURN node, score ORDER BY score ASC",
		labelSubject, propEmbedding, fetchK, propOrgID,
	)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"vec": vectorParam(vector), "org": orgID}, true)
	if err != nil {
		return nil, false, safeDependencyError("vector search context graph", err)
	}
	// Codex round-1 F1: the tau filter runs BEFORE the truncation decision and
	// before the top-returnCap slice, and truncation is derived from how many
	// rows SURVIVED tau -- never from how many the server returned.
	//
	// The earlier order had it backwards, and the consequence was not
	// cosmetic. A query whose every row fell below the similarity floor
	// returned ZERO candidates while still reporting truncated=true, and
	// truncation is resolution-wide authority: ResolveFromMergedCandidates
	// checks searchTruncated BEFORE any confidence threshold, so a vector
	// query that found NOTHING could force the whole resolution to ambiguous
	// and block an otherwise strong, unopposed lexical commit. A search that
	// found nothing must have authority over nothing.
	//
	// Deriving truncation from survivors is sound because the k-NN result is
	// ordered by ascending distance: if the (returnCap+1)th row falls below
	// tau, every row beyond it is further away and therefore also below tau,
	// so no genuine competitor was cut off. Only a full returnCap+1 rows ALL
	// clearing tau means the corpus may hold above-floor candidates this
	// (already widened, see the function doc comment) budget could not show.
	type survivor struct {
		node       *node
		similarity float64
	}
	survivors := make([]survivor, 0, len(rows))
	for _, row := range rows {
		n, ok := row["node"].(*node)
		if !ok || n == nil {
			continue
		}
		distance, ok := row["score"].(float64)
		if !ok {
			continue
		}
		similarity := embedprovider.CosineFromDistance(distance)
		if !aboveSimilarityFloor(similarity, tau) {
			// AC-3778-4: not close enough to be evidence of anything.
			continue
		}
		survivors = append(survivors, survivor{node: n, similarity: similarity})
	}
	truncated := len(survivors) > returnCap
	if truncated {
		survivors = survivors[:returnCap]
	}
	candidates := make([]graphrank.CandidateNode, 0, len(survivors))
	for _, s := range survivors {
		candidate := toCandidateNode(s.node)
		relevance := vectorRelevanceFloor
		if !truncated {
			relevance = vectorRelevanceFromSimilarity(s.similarity, tau)
		}
		candidate.Relevance = graphrank.Normalized(relevance)
		candidate.Mechanism = contextfabric.MatchVector
		// CHAOS-3829: VectorSimilarity is the RAW similarity, set
		// UNCONDITIONALLY -- unlike Relevance above, never clamped when
		// truncated is true. See CandidateNode.VectorSimilarity's own doc
		// comment for why the commit-path carve-out needs this specific,
		// unclamped value and why that is sound even under truncation. A
		// fresh local each iteration (not a loop-scoped reused pointer),
		// since this becomes a stored, individually-addressed *float64 per
		// candidate.
		similarity := s.similarity
		candidate.VectorSimilarity = &similarity
		candidates = append(candidates, candidate)
	}
	return candidates, truncated, nil
}

// aboveSimilarityFloor is the SINGLE production predicate for "does this
// similarity clear the tau floor" -- STRICTLY greater than tau, never >=. A
// candidate whose similarity exactly EQUALS tau is dropped here exactly like
// one below it (AC-3778-4: "not close enough" includes the boundary itself).
//
// tau_calibration.go's recall/hard-negative/reject-rate accounting (codex
// round-1 P1) calls this EXACT function rather than re-deriving its own
// comparison, so calibration math can never silently disagree with what
// production actually retrieves -- see
// TestCalibrateFromReport_BoundarySampleAtTauNotCountedAsRecalled.
func aboveSimilarityFloor(similarity, tau float64) bool {
	return similarity > tau
}

// floorApplicable is the SINGLE production authority for "is this value a
// usable SimilarityFloor at all" -- strictly inside (0, 1), matching
// attachEmbedder's own out-of-range fallback (adapter.go: "a value outside
// that range is replaced by embedprovider.DefaultSimilarityFloor rather than
// accepted, because a floor of 0 would silently disable the AC-3778-4
// no-match guard"). A floor at or below 0 admits everything (no guard at
// all); a floor at or above 1 admits nothing (no candidate could ever clear
// it, including a perfect match) -- neither is a usable similarity floor.
//
// tau_calibration.go's CalibrateFromReport (codex round-3 P2) calls this
// EXACT function to validate a computed recall-gate tau BEFORE returning it
// as a recommendation, so a caller can never receive an ApplyReady=true
// result whose SimilarityFloor this same predicate -- the one
// EmbedderFromEnv itself gates on just below -- would silently refuse to
// apply.
func floorApplicable(floor float64) bool {
	return floor > 0 && floor < 1
}

// hybridSearchNodes is the ResolveDeps.Search implementation: the lexical
// full-text step and, when an embedder is configured, the vector step beside
// it, merged into ONE candidate list.
//
// Both mechanisms search the SAME search_text corpus (see
// docs/design/context-fabric-vector-retrieval.md §3), so the two paths differ
// only in mechanism -- which is what makes their agreement meaningful to
// graphrank's corroboration band and what makes an AC-3778-2 measurement a
// comparison of mechanisms rather than of corpora.
//
// FAIL-OPEN TO LEXICAL. An embedder that errors or exceeds its (deliberately
// small, 250 ms by default) timeout degrades this call to lexical-only. It
// never fails the request and never blocks past the AC-3778-5 budget. The
// measured cold-start cost of a local embedder is 9.3 s against 10-17 ms warm,
// so this is not a theoretical branch: it is what keeps a model reload from
// turning every investigation into a timeout. A degraded call reports
// truncation exactly as the lexical path alone would; it does not claim the
// vector mechanism found nothing, because it never asked.
//
// Candidates from the two mechanisms are NOT deduplicated here. graphrank's
// own candidatesBySubject merge (MergeCandidates) is the single place that
// unions two findings of one subject, and routing both mechanisms through it
// is precisely how a subject found BOTH ways ends up carrying both mechanisms.
//
// The third return value reports whether the VECTOR mechanism was unavailable
// for this call (codex round-1 F4). It is request-scoped by construction --
// derived from what happened during this one search, never from the
// organization's recent history -- which is what makes it usable as the
// engine's input for a coverage/limitation decision about THIS answer.
func (a *Adapter) hybridSearchNodes(ctx context.Context, key, orgID, term string, limit int, fence *resolutionFence, temporal temporalFilter) ([]graphrank.CandidateNode, bool, bool, error) {
	// CHAOS-3827 (codex round-1 High): BOTH mechanisms are gated on the one
	// predicate, here at the seam, rather than each deciding for itself.
	//
	// The lexical arm has always stopped on a term that tokenizes to nothing
	// (fulltextSearchNodes' len(terms)==0 early return), but the vector arm
	// had no equivalent and would still embed the raw text and return its
	// nearest neighbours. For a term like "???" those neighbours are
	// arbitrary -- the embedding carries no subject meaning for them to be
	// near -- yet they arrive as ordinary above-floor candidates, so
	// resolution reaches clarification or no-match holding garbage instead of
	// holding nothing. That is the same failure AC-3778-4's similarity floor
	// exists to prevent (meaningless input must not manufacture candidates),
	// reached by a different door. Before this branch the punctuation-only
	// term hard-errored in the lexical step and never got this far, so
	// closing it belongs here.
	//
	// hasLexicalContent delegates to tokenizeForFulltext, so the vector skip
	// and the lexical early return cannot drift apart: they are the same
	// tokenizer's verdict, asked once. The text is deliberately never
	// embedded -- not embedded-then-discarded -- so no provider call is made
	// on input that cannot mean anything.
	//
	// degraded stays FALSE: nothing was withheld by a fault. No mechanism
	// could act on this input, which is a property of the question, not an
	// outage, and marking the answer would misreport it (the same reasoning
	// the embedder-nil branch below applies to "nothing was expected").
	if !hasLexicalContent(term) {
		return nil, false, false, nil
	}
	// codex round-6 P2 (fix A): subject resolution is the ONE path the
	// CHAOS-3838 union/lexicon-expansion contract belongs to -- see
	// fulltextSearchNodesForResolution's own doc comment for why
	// DiscoverContext must never share it.
	lexical, truncated, err := a.fulltextSearchNodesForResolution(ctx, key, orgID, term, limit, temporal)
	if err != nil {
		return nil, false, false, err
	}
	for i := range lexical {
		lexical[i].Mechanism = contextfabric.MatchLexical
	}
	// Codex round-1 F2: the AC-3778-7 fence is verified HERE, on the read
	// path, not only at projection bootstrap -- the hosted API never runs
	// bootstrap, so a read-only process previously checked neither the index
	// dimension nor the stored embedder identity. The verdict is cached per
	// organization (see ensureVectorReadable), so this is one bounded probe
	// per organization per process, not per request.
	if a.embedder == nil {
		// Vector retrieval is not configured for this deployment. That is not
		// a degradation -- nothing was expected to be available -- so it must
		// not mark the answer.
		return lexical, truncated, false, nil
	}
	// CHAOS-3781: the vector index has no notion of a validity window.
	//
	// db.idx.vector.queryNodes returns the top-k by distance and the org
	// predicate is a POST-FILTER over that k (see vectorSearchNodes). A
	// temporal predicate added the same way would inherit that shape, and
	// unlike org -- a near no-op, since the graph key already scopes the
	// database to one organization -- a validity window can eliminate most
	// of the k. Two consequences, both wrong: under-recall that reads as
	// absence, because the k came back full of current-only nodes; and a
	// broken truncation argument, which is sound only because results are
	// distance-ordered, so nothing closer was cut.
	//
	// So on a historical axis the vector step is SKIPPED and the answer
	// says so. The alternative -- over-fetching some multiple of k and
	// filtering -- needs an unbounded multiplier and a rewrite of a
	// truncation argument that converged over eight review rounds; it is
	// recorded as the future enhancement rather than taken now.
	//
	// PLACEMENT IS THE ARGUMENT. This sits AFTER the embedder-nil branch
	// above, so the three cases fall out of ORDER rather than a compound
	// condition: nil embedder keeps 3778's nothing-was-expected rule and
	// reports degraded=FALSE, while here a mechanism WAS expected and is
	// unavailable, so degraded=TRUE is the honest value. A current-axis
	// question never reaches this line and behaves exactly as before.
	if temporal.active {
		// Round-16: answer-level degraded and telemetry-level degraded must
		// fire TOGETHER. This branch used to set the answer flag and emit
		// nothing, so a historical query with a configured embedder produced
		// a degraded answer with zero operational signal -- indistinguishable
		// from healthy retrieval, and from an outage, at exactly the moment
		// an operator would be trying to tell those apart.
		a.recordVectorSuppressed(ctx, orgID)
		return lexical, truncated, true, nil
	}
	if !fence.readable(ctx, a, key, orgID) {
		// The graph holds vectors this embedder did not produce, or the fence
		// could not be verified. Lexical retrieval proceeds, and the answer
		// records that a mechanism was missing.
		a.recordVectorDegraded(ctx, orgID)
		return lexical, truncated, true, nil
	}
	// CHAOS-3836 seam: the query-side task prefix wraps the term's
	// TRANSMISSION to the model only -- the lexical arm above already
	// searched the unprefixed term, and nothing prefixed is ever stored.
	//
	// CHAOS-3838 (spec L13, dual-arm): vectorQueryText widens term with the
	// domain lexicon before prefixing, same closed vocabulary the lexical
	// arm's query widened with above -- byte-identical to term when nothing
	// in it matches, so this is a no-op (including for the embedcache key)
	// for every term the lexicon has no opinion about.
	vectors, embedErr := a.embedder.Embed(ctx, []string{a.queryPrefixed(vectorQueryText(term))})
	if embedErr != nil || len(vectors) != 1 {
		a.recordVectorDegraded(ctx, orgID)
		return lexical, truncated, true, nil
	}
	// CHAOS-3834: a.overFetchMultiplier is the calibrated per-identity K
	// (zero when uncalibrated, which vectorSearchNodesWithOverFetch already
	// treats as multiplier 1 -- byte-identical to the pre-CHAOS-3834
	// vectorSearchNodes call this replaces).
	vectorCandidates, vectorTruncated, err := a.vectorSearchNodesWithOverFetch(ctx, key, orgID, vectors[0], a.similarityFloor, limit, a.overFetchMultiplier)
	if err != nil {
		// A graph-side failure of the vector step degrades the same way an
		// embedder failure does. The lexical answer is still a real answer.
		a.recordVectorDegraded(ctx, orgID)
		return lexical, truncated, true, nil
	}
	return append(lexical, vectorCandidates...), truncated || vectorTruncated, false, nil
}

// questionVectorSearchNodes is the ResolveDeps.SearchQuestion implementation
// (CHAOS-3838 / spec L11): ONE additional vector-only search per resolution,
// embedding the full interpreted QUESTION text rather than an extracted
// subject term. reader.go's ResolveSubjects wires this to run exactly once
// per ResolveSubjects call -- see graphrank.ResolveSubjects' SearchQuestion
// handling -- unioning its candidates into the SAME candidatesBySubject
// merge every per-term hybridSearchNodes call already feeds.
//
// Vector-only, deliberately: L11 is a recall lever for the mechanism that
// already runs per term (MatchVector), not a second lexical pass. Adding a
// lexical arm over raw question text here would duplicate DiscoverContext's
// own full-text-over-question step (cohort discovery, a different purpose)
// for no additional corroboration value: MergeMechanisms dedups by ENUM
// MEMBER, so a term-embed find and a question-embed find of the same
// subject merge into ONE MatchVector entry, never two (mechanism.go's
// DistinctMechanismCount doc comment) -- corroboration still requires a
// genuinely distinct mechanism. L13's lexical-arm expansion is what
// supplies that second mechanism; this function's whole job is finding MORE
// subjects for it to potentially corroborate.
//
// Mirrors hybridSearchNodes' three fail-open guards, in the same order, so
// the two paths can never silently disagree about when vector retrieval is
// available: no embedder configured (nothing expected, degraded=false), a
// historical time axis (vector has no validity window, degraded=true), and
// an unreadable AC-3778-7 fence (degraded=true). The fence and temporal
// filter are the SAME instances hybridSearchNodes' calls already share for
// this resolution -- one fence probe per resolution, not one per call.
func (a *Adapter) questionVectorSearchNodes(ctx context.Context, key, orgID, question string, limit int, fence *resolutionFence, temporal temporalFilter) ([]graphrank.CandidateNode, bool, bool, error) {
	// codex round-1 P2 (fix B): the guard must evaluate the EXACT bounded
	// bytes that will reach the embedder, not the unbounded question --
	// same class as CHAOS-3835's round-3 capped-bytes fix. A question that
	// carries real word content only past the embed truncation point (e.g.
	// several thousand punctuation runes, then "auth") previously passed
	// this guard on the FULL text and then had Embed silently truncate to
	// pure punctuation, embedding meaningless bytes into an arbitrary
	// nearest-neighbor query. bound() is the single authority for both what
	// gets guarded and what gets embedded below -- neither re-derives
	// embedprovider's own MaxTextRunes/prefix-budget math.
	bound := a.boundedQueryText(question)
	if !hasLexicalContent(bound.substance) {
		// Same reasoning as hybridSearchNodes' identical guard: meaningless
		// input must not manufacture arbitrary nearest-neighbor candidates,
		// and this is a property of the question, not a fault, so
		// degraded stays false.
		return nil, false, false, nil
	}
	if a.embedder == nil {
		return nil, false, false, nil
	}
	if temporal.active {
		a.recordVectorSuppressed(ctx, orgID)
		return nil, false, true, nil
	}
	if !fence.readable(ctx, a, key, orgID) {
		a.recordVectorDegraded(ctx, orgID)
		return nil, false, true, nil
	}
	vectors, embedErr := a.embedder.Embed(ctx, []string{bound.transmitted})
	if embedErr != nil || len(vectors) != 1 {
		a.recordVectorDegraded(ctx, orgID)
		return nil, false, true, nil
	}
	candidates, truncated, err := a.vectorSearchNodesWithOverFetch(ctx, key, orgID, vectors[0], a.similarityFloor, limit, a.overFetchMultiplier)
	if err != nil {
		a.recordVectorDegraded(ctx, orgID)
		return nil, false, true, nil
	}
	return candidates, truncated, false, nil
}

// boundedQueryResult carries the two texts questionVectorSearchNodes' guard
// and embed call need, computed together so they can never drift: transmitted
// is exactly what Embed receives; substance is transmitted with any
// configured query task-prefix stripped back off, so a caller-typed
// question's own bounded content -- never the fixed, code-owned prefix,
// which always carries real words and would otherwise mask a meaningless
// substance from hasLexicalContent -- is what the guard evaluates.
type boundedQueryResult struct {
	transmitted string
	substance   string
}

// boundedQueryText computes the bounded query text for text (CHAOS-3838
// question-level path, codex round-1 P2 fix B, corrected round-3 P1 fix A):
// lexicon-widened (spec L13's vectorQueryText), prefixed via
// a.queryPrefixed (embedprovider.ApplyQueryPrefix when a prefix family is
// configured, else the identity function), then UNCONDITIONALLY bounded to
// a.embedBudgetRunes() via embedprovider.TruncateRunes -- the SAME two
// primitives Embed's own internal truncation is built from, never
// re-derived. One path, no branch on prefix presence (see the inline
// comment below for why a branch there was the round-1 fix's own bug).
func (a *Adapter) boundedQueryText(text string) boundedQueryResult {
	widened := vectorQueryText(text)
	prefixed := a.queryPrefixed(widened)
	// codex round-3 P1 (fix A): ONE path, unconditional -- no branch on
	// whether a.applyQueryPrefix is nil. The round-1 fix branched on that,
	// reasoning "a configured prefix already budgets itself via
	// ApplyQueryPrefix's own contract" -- true only when the prefix STRING
	// is non-empty. embedprovider.applyPrefixWithBudget's very first line is
	// `if prefix == "" { return text }`, a complete bypass of ALL budgeting
	// -- and a.applyQueryPrefix is a non-nil bound method (embedder.ApplyQueryPrefix)
	// for EVERY configured embedder regardless of prefix family, including
	// PrefixFamilyNone (the DEPLOYED production default for
	// openai/text-embedding-3-large, embedprovider.PrefixFamilyNone == "").
	// So the "prefix WAS applied" branch was, in the live default
	// configuration, silently trusting an untruncated string -- exactly the
	// original P2 hole, reopened by the branch meant to close it. Bounding
	// here UNCONDITIONALLY, after the fact, cannot diverge from a REAL
	// prefix's own internal budgeting (already <= a.embedBudgetRunes(),
	// this TruncateRunes is then a no-op) and is the ONLY thing that
	// actually bounds an EMPTY-prefix or no-prefix result.
	bounded := embedprovider.TruncateRunes(prefixed, a.embedBudgetRunes())
	// prefixOnly is derived the SAME way (queryPrefixed then the SAME
	// unconditional bound), so it can never disagree with what bounded
	// actually carries as its prefix, whatever a.applyQueryPrefix does or
	// does not do with an empty substance.
	prefixOnly := embedprovider.TruncateRunes(a.queryPrefixed(""), a.embedBudgetRunes())
	return boundedQueryResult{transmitted: bounded, substance: strings.TrimPrefix(bounded, prefixOnly)}
}

// recordVectorDegraded reports a vector-retrieval degradation to telemetry.
//
// The ANSWER-facing half of this signal does not live here: it is carried
// out of ResolveSubjects on SubjectResolution.RetrievalDegraded, per the
// orchestrator's ruling on codex F4. See hybridSearchNodes' degraded return
// value and reader.go's ResolveSubjects.
func (a *Adapter) recordVectorDegraded(ctx context.Context, orgID string) {
	if a.config.Telemetry == nil {
		return
	}
	a.config.Telemetry.RecordVectorRetrievalDegraded(ctx, orgID)
}

// recordVectorSuppressed reports a deliberate historical-axis suppression.
// Separate from recordVectorDegraded so an operator can tell "withheld
// because it cannot answer this" from "broken".
func (a *Adapter) recordVectorSuppressed(ctx context.Context, orgID string) {
	if a.config.Telemetry == nil {
		return
	}
	a.config.Telemetry.RecordVectorRetrievalSuppressed(ctx, orgID)
}

// recordVectorProjection reports one batch's vector outcome (codex round-3
// F2). Called on EVERY embedding path -- success, clear, and skip -- so the
// cleared count is a complete accounting rather than a best-effort one.
//
// skipped carries the CHAOS-3835 (§7 D2) reason breakdown (kind skip-list
// vs id-only) rather than one combined number, so the telemetry sink can
// report -- or a caller inspecting embedSkipCounts.Total() can still get --
// the complete accounting either way.
func (a *Adapter) recordVectorProjection(ctx context.Context, orgID string, embedded, cleared int, skipped embedSkipCounts) {
	if a.config.Telemetry == nil {
		return
	}
	a.config.Telemetry.RecordVectorProjection(ctx, orgID, embedded, cleared, skipped.Kind, skipped.IDOnly)
}

// recordVectorIndexEfRuntimeMismatch reports an existing vector index's
// efRuntime disagreeing with the calibrated policy to telemetry (codex
// round-9 P2 wiring fix -- see ensureVectorIndex's doc comment and
// GraphTelemetry.RecordVectorIndexEfRuntimeMismatch's doc comment). A
// silent no-op when Telemetry is unset, matching every sibling recordX
// method's nil-safe contract: an operator who declined telemetry
// (NoopTelemetry, or simply never setting Config.Telemetry) sees nothing,
// rather than this diagnostic falling back to an unconfigured global
// default that bypasses whatever sink/level they actually configured.
func (a *Adapter) recordVectorIndexEfRuntimeMismatch(ctx context.Context, key string, policyEfRuntime, indexEfRuntime int) {
	if a.config.Telemetry == nil {
		return
	}
	a.config.Telemetry.RecordVectorIndexEfRuntimeMismatch(ctx, key, policyEfRuntime, indexEfRuntime)
}

// EmbedderFromEnv builds the optional vector-retrieval dependencies from the
// environment (CHAOS-3778).
//
// A deployment that has not set ACR_CONTEXT_FABRIC_EMBED_BASE_URL gets a zero
// EmbedderOptions and a nil error: vector retrieval is OFF, which is a
// supported steady state, not a misconfiguration. Every other error IS a
// misconfiguration (an unparseable dimension, a plaintext base URL without the
// explicit insecure opt-in) and is returned, because silently running without
// vector retrieval because a value failed to parse would be indistinguishable
// from having chosen not to enable it.
//
// It exists so the two construction sites -- the hosted API's graph reader and
// acr-projector's projection backend -- cannot drift. Both must agree on
// whether embeddings are written and queried: a projector writing vectors the
// reader never queries is wasted work, and a reader querying an index the
// projector never fills is silently degraded retrieval.
func EmbedderFromEnv(lookup func(string) (string, bool)) (EmbedderOptions, error) {
	if !embedprovider.Configured(lookup) {
		return EmbedderOptions{}, nil
	}
	cfg, err := embedprovider.ConfigFromEnv(lookup)
	if err != nil {
		return EmbedderOptions{}, err
	}
	embedder, err := embedprovider.New(cfg)
	if err != nil {
		return EmbedderOptions{}, err
	}
	// Capabilities are captured HERE, off the concrete embedder, because
	// this is the last point the concrete type is visible: the hosted API
	// wraps Embedder in a read-path cache (CHAOS-3841) that implements only
	// the two-method port, so anything not captured now is unreachable
	// after wrapping. See EmbedderOptions' field docs.
	options := EmbedderOptions{
		Embedder:            embedder,
		SimilarityFloor:     embedder.SimilarityFloor(),
		MaxTextRunes:        embedder.MaxTextRunes(),
		ApplyDocumentPrefix: embedder.ApplyDocumentPrefix,
		ApplyQueryPrefix:    embedder.ApplyQueryPrefix,
		PrefixTagComponent:  embedder.PrefixTagComponent(),
	}
	// CHAOS-3834: override with a calibrated per-identity RetrievalPolicy
	// when this deployment's exact embed retrieval identity has one.
	// EmbedRetrievalIdentityFromEnv is the single authority for that string
	// (identity.String()+"#"+compositionTag) -- reusing it here, rather than
	// re-deriving the composition tag, means the policy lookup key can never
	// drift from the key migration 0014's embed_retrieval_identity column
	// persists for the same deployment. Configured(lookup) already holds at
	// this point (checked above), so this never returns EmbedRetrievalIdentityNone.
	//
	// cfg.Dimension is passed SEPARATELY (codex round-3 P1): dimension is
	// deliberately excluded from EmbedRetrievalIdentityFromEnv's persisted
	// string (EmbedderIdentity.String()'s own doc comment explains why), but
	// a calibrated tau/efRuntime entry is measured against ONE specific
	// width -- LookupRetrievalPolicy folds it back in for the policy lookup
	// specifically, without changing what gets persisted for answer reuse.
	identity, err := EmbedRetrievalIdentityFromEnv(lookup)
	if err != nil {
		return EmbedderOptions{}, err
	}
	if policy, ok := LookupRetrievalPolicy(identity, cfg.Dimension); ok {
		// A calibrated entry's zero fields still mean "unchanged from
		// today's default" (RetrievalPolicy's doc comment) -- e.g. the
		// shipped openai/text-embedding-3-large entry deliberately leaves
		// OverFetchMultiplier at 0 ("K unchanged"). Only SimilarityFloor
		// needs the extra guard below, mirroring attachEmbedder's own
		// floor validation: a policy bug that let a zero tau through must
		// not silently disable the AC-3778-4 no-match guard.
		//
		// Precedence, evaluated per knob (codex round-1 P1: an unconditional
		// override silently discarded an operator's explicit
		// ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR, which is
		// measurement-integrity critical for live harnesses that pin their
		// own floor). The calibrated table is a DEFAULT, not a forced
		// override: it applies only where the operator supplied no explicit
		// value for that specific knob. SimilarityFloor has an env knob
		// (EnvSimilarityFloor) to check against; "explicit" mirrors envFloat's
		// own definition (set AND non-blank) so a blank env var does not
		// count as an override. OverFetchMultiplier and EfRuntime have no
		// env-configurable source anywhere in embedprovider -- there is no
		// operator value to preserve for either, so the calibrated table
		// stays the sole source for both, unconditionally.
		if explicitFloor, ok := lookup(embedprovider.EnvSimilarityFloor); !(ok && strings.TrimSpace(explicitFloor) != "") {
			if floorApplicable(policy.SimilarityFloor) {
				options.SimilarityFloor = policy.SimilarityFloor
			}
		}
		options.OverFetchMultiplier = policy.OverFetchMultiplier
		options.EfRuntime = policy.EfRuntime
		// CHAOS-3829: like OverFetchMultiplier/EfRuntime above, no operator
		// env knob exists for this -- the calibrated table is the sole
		// source, unconditionally.
		options.VectorMarginCommitThreshold = policy.VectorMarginCommitThreshold
	}
	return options, nil
}

// resolutionFence memoizes the AC-3778-7 fence verification for the lifetime
// of ONE ResolveSubjects call.
//
// Codex round-2 R2-1 ruled that the fence must be verified per query rather
// than cached across requests, because acr-api and acr-projector configure
// their embedders independently and a process-lifetime ENABLED verdict can
// outlive the configuration it was based on. This is the narrowest scope that
// honors that: fresh for every resolution, shared across the several Search
// calls one resolution makes (ResolveSubjects issues one per interpreted
// subject term), so a resolution pays exactly ONE bounded probe rather than
// one per term.
//
// It is created per ResolveSubjects call and never escapes it, so it needs no
// synchronization -- ResolveSubjects walks its terms sequentially.
type resolutionFence struct {
	decided bool
	enabled bool
}

func (f *resolutionFence) readable(ctx context.Context, a *Adapter, key, orgID string) bool {
	if f == nil {
		// Defensive: a caller that did not supply a fence gets an
		// unmemoized, still-correct verification rather than a silent pass.
		return a.ensureVectorReadable(ctx, key, orgID)
	}
	if !f.decided {
		f.enabled = a.ensureVectorReadable(ctx, key, orgID)
		f.decided = true
	}
	return f.enabled
}
