package falkorgraph

import (
	"context"
	"errors"
	"fmt"
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
// Malformed rows (a missing kind/canonical-id/embedding, or an embedding that
// fails decodeVectorProperty) are skipped rather than failing the whole
// fetch -- one corrupt node must not blank out a 36k-vector baseline the rest
// of the corpus could still support.
func (a *Adapter) fetchEmbedderFenceCorpus(ctx context.Context, key, orgID string) ([]oracleVector, error) {
	if a.embedder == nil {
		return nil, errOracleEmbedderRequired
	}
	identity := a.embedder.Identity().String()
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org}) WHERE n.%s IS NOT NULL AND n.%s = $identity RETURN n",
		labelSubject, propOrgID, propEmbedding, propEmbedderIdentity,
	)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "identity": identity}, true)
	if err != nil {
		return nil, safeDependencyError("fetch exact-search oracle corpus", err)
	}
	corpus := make([]oracleVector, 0, len(rows))
	for _, r := range rows {
		n, ok := r["n"].(*node)
		if !ok || n == nil {
			continue
		}
		kind := propStringValue(n.Properties[propKind])
		canonicalID := propStringValue(n.Properties[propCanonicalID])
		if kind == "" || canonicalID == "" {
			continue
		}
		vector, ok := decodeVectorProperty(n.Properties[propEmbedding])
		if !ok || len(vector) == 0 {
			continue
		}
		corpus = append(corpus, oracleVector{
			Kind: kind, CanonicalID: canonicalID,
			Label: propStringValue(n.Properties[propLabel]), Vector: vector,
		})
	}
	return corpus, nil
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
// current identity" -- the oracle_live_test.go driver reports that case as
// its own miss cause (no_vector), distinct from text_loss, because it is not
// a statement about embedding SEMANTICS at all.
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
