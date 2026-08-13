package falkorgraph

import (
	"context"
	"fmt"

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

// createVectorIndex creates the per-organization vector index on
// Subject.embedding.
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
	cypher := fmt.Sprintf(
		"CREATE VECTOR INDEX FOR (n:%s) ON (n.%s) OPTIONS {dimension:%d, similarityFunction:'%s'}",
		labelSubject, propEmbedding, dimension, vectorSimilarityCosine,
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
	if len(vector) == 0 {
		return nil, false, nil
	}
	if limit <= 0 || limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	cypher := fmt.Sprintf(
		"CALL db.idx.vector.queryNodes('%s', '%s', %d, vecf32($vec)) YIELD node, score "+
			"WHERE node.%s = $org "+
			"RETURN node, score ORDER BY score ASC",
		labelSubject, propEmbedding, limit+1, propOrgID,
	)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"vec": vectorParam(vector), "org": orgID}, true)
	if err != nil {
		return nil, false, safeDependencyError("vector search context graph", err)
	}
	// Codex round-1 F1: the tau filter runs BEFORE the truncation decision and
	// before the top-limit slice, and truncation is derived from how many rows
	// SURVIVED tau -- never from how many the server returned.
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
	// ordered by ascending distance: if the (limit+1)th row falls below tau,
	// every row beyond it is further away and therefore also below tau, so no
	// genuine competitor was cut off. Only a full limit+1 rows ALL clearing
	// tau means the corpus may hold above-floor candidates this budget could
	// not show.
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
		if similarity <= tau {
			// AC-3778-4: not close enough to be evidence of anything.
			continue
		}
		survivors = append(survivors, survivor{node: n, similarity: similarity})
	}
	truncated := len(survivors) > limit
	if truncated {
		survivors = survivors[:limit]
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
		candidates = append(candidates, candidate)
	}
	return candidates, truncated, nil
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
	lexical, truncated, err := a.fulltextSearchNodes(ctx, key, orgID, term, limit, temporal)
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
		return lexical, truncated, true, nil
	}
	if !fence.readable(ctx, a, key, orgID) {
		// The graph holds vectors this embedder did not produce, or the fence
		// could not be verified. Lexical retrieval proceeds, and the answer
		// records that a mechanism was missing.
		a.recordVectorDegraded(ctx, orgID)
		return lexical, truncated, true, nil
	}
	vectors, embedErr := a.embedder.Embed(ctx, []string{term})
	if embedErr != nil || len(vectors) != 1 {
		a.recordVectorDegraded(ctx, orgID)
		return lexical, truncated, true, nil
	}
	vectorCandidates, vectorTruncated, err := a.vectorSearchNodes(ctx, key, orgID, vectors[0], a.similarityFloor, limit)
	if err != nil {
		// A graph-side failure of the vector step degrades the same way an
		// embedder failure does. The lexical answer is still a real answer.
		a.recordVectorDegraded(ctx, orgID)
		return lexical, truncated, true, nil
	}
	return append(lexical, vectorCandidates...), truncated || vectorTruncated, false, nil
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

// recordVectorProjection reports one batch's vector outcome (codex round-3
// F2). Called on EVERY embedding path -- success, clear, and skip -- so the
// cleared count is a complete accounting rather than a best-effort one.
func (a *Adapter) recordVectorProjection(ctx context.Context, orgID string, embedded, cleared int) {
	if a.config.Telemetry == nil {
		return
	}
	a.config.Telemetry.RecordVectorProjection(ctx, orgID, embedded, cleared)
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
	return EmbedderOptions{Embedder: embedder, SimilarityFloor: embedder.SimilarityFloor()}, nil
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
