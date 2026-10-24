package falkorgraph

import (
	"context"
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// kindScopedSemanticSearchNodes (CHAOS-10941) is graphrank.ResolveDeps.
// KindScopedSemantic: ONE embed of the term, then cosine against the stored
// embeddings of every node of exactly one repository/project/team kind, read
// by a single bounded Cypher query that carries the EXACT temporal predicate.
// That is why it is correct on a historical axis where the unscoped vector arm
// is withheld: the validity window is applied in the query, not as a post-
// filter over a top-k, so a node outside the window is never a candidate and
// no under-recall can read as absence.
//
// Population bound: exactNameCandidateQueryLimit (the same census bound
// chaos4348ExactNameCandidates uses). A kind that does not fit is not scored
// -- degraded, nothing returned -- never a partial ranking presented as whole.
// Only nodes stamped with THIS embedder's identity are read (the same fence
// every vector read keeps); an unstamped or foreign node is invisible here.
// degraded=false when no embedder is configured (nothing was expected).
func (a *Adapter) kindScopedSemanticSearchNodes(ctx context.Context, key, orgID, term string, kind contextfabric.SubjectKind, limit int, temporal temporalFilter) ([]graphrank.CandidateNode, bool, bool, error) {
	if a.embedder == nil || !isKindScopedSemanticKind(kind) {
		return nil, false, false, nil
	}
	bound := a.boundedQueryText(term)
	if !hasLexicalContent(bound.substance) {
		return nil, false, false, nil
	}
	if limit <= 0 || limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	vectors, embedErr := a.embedder.Embed(ctx, []string{bound.transmitted})
	if embedErr != nil || len(vectors) != 1 {
		a.recordQueryEmbedFailure(ctx, orgID, embedErr)
		return nil, false, true, nil
	}
	queryVector := make([]float64, len(vectors[0]))
	for i, f := range vectors[0] {
		queryVector[i] = float64(f)
	}
	if !finiteVector(queryVector) {
		a.recordVectorDegraded(ctx, orgID)
		return nil, false, true, nil
	}
	cypher := fmt.Sprintf(
		"MATCH (n:%s) WHERE n.%s = $org AND n.%s = $kind AND n.%s IS NOT NULL AND n.%s = $identity%s%s RETURN n LIMIT %d",
		labelSubject, propOrgID, propKind, propEmbedding, propEmbedderIdentity, temporal.predicate("n"), activeTeamCypher("n"), exactNameCandidateQueryLimit+1,
	)
	identity := a.stampedEmbedderIdentity(a.embedder.Identity())
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": orgID, "kind": string(kind), "identity": identity}), true)
	if err != nil {
		a.recordVectorDegraded(ctx, orgID)
		return nil, false, true, nil
	}
	if len(rows) > exactNameCandidateQueryLimit {
		a.recordVectorDegraded(ctx, orgID)
		return nil, false, true, nil
	}
	type scored struct {
		n          *node
		similarity float64
	}
	var hits []scored
	for _, r := range rows {
		n, ok := r["n"].(*node)
		if !ok || n == nil || inactiveTeamNode(n) {
			continue
		}
		vector, ok := decodeVectorProperty(n.Properties[propEmbedding])
		if !ok || len(vector) == 0 {
			continue
		}
		similarity := trueCosineSimilarity(queryVector, vector)
		if !aboveSimilarityFloor(similarity, a.similarityFloor) {
			continue
		}
		hits = append(hits, scored{n: n, similarity: similarity})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].similarity != hits[j].similarity {
			return hits[i].similarity > hits[j].similarity
		}
		return propStringValue(hits[i].n.Properties[propCanonicalID]) < propStringValue(hits[j].n.Properties[propCanonicalID])
	})
	truncated := len(hits) > limit
	if truncated {
		hits = hits[:limit]
	}
	candidates := make([]graphrank.CandidateNode, 0, len(hits))
	for _, h := range hits {
		candidate := toCandidateNode(h.n)
		candidate.Relevance = graphrank.Normalized(vectorRelevanceFromSimilarity(h.similarity, a.similarityFloor))
		candidate.Mechanism = contextfabric.MatchVector
		similarity := h.similarity
		candidate.VectorSimilarity = &similarity
		candidates = append(candidates, candidate)
	}
	return candidates, truncated, false, nil
}

// isKindScopedSemanticKind is the closed set the arm serves: the kinds whose
// whole population the exact-name census already treats as small enough to
// read in one query.
func isKindScopedSemanticKind(kind contextfabric.SubjectKind) bool {
	for _, censused := range exactNameKinds {
		if censused == string(kind) {
			return true
		}
	}
	return false
}
