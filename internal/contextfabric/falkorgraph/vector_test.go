package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// stubEmbedder is a fully in-process contextfabric.Embedder double.
type stubEmbedder struct {
	vector []float32
	err    error
	calls  int
}

func (s *stubEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = s.vector
	}
	return out, nil
}

func (s *stubEmbedder) Identity() contextfabric.EmbedderIdentity {
	return contextfabric.EmbedderIdentity{Provider: "stub", Model: "stub-embed", Dimension: len(s.vector)}
}

func vectorAdapter(t *testing.T, fake *fakeConn, embedder contextfabric.Embedder, floor float64) *Adapter {
	t.Helper()
	adapter := newFakeAdapter(t, fake)
	adapter.attachEmbedder(EmbedderOptions{Embedder: embedder, SimilarityFloor: floor})
	return adapter
}

// The band ceiling is what makes AC-3778-3 true by arithmetic. If this
// constant ever reaches the 0.72 lone-candidate gate, a vector-only candidate
// could auto-commit and the acceptance bar is silently repealed.
func TestVectorBandCeilingStaysBelowTheLoneCandidateGate(t *testing.T) {
	if vectorRelevanceCeiling >= graphrank.CorroboratedFloor {
		t.Fatalf("vector ceiling %v must stay strictly below the lone-candidate gate %v",
			vectorRelevanceCeiling, graphrank.CorroboratedFloor)
	}
	if vectorRelevanceFloor <= 0 {
		t.Fatal("a genuine vector hit must never read as no signal")
	}
}

// The mapping is absolute and per-candidate: it depends only on this
// candidate's own similarity, so two different queries' vector confidences are
// directly comparable (the property Codex rounds 2/3 forced onto the lexical
// arm).
func TestVectorRelevanceIsAbsoluteMonotoneAndBounded(t *testing.T) {
	const tau = 0.55
	previous := -1.0
	for similarity := tau; similarity <= 1.0; similarity += 0.005 {
		got := vectorRelevanceFromSimilarity(similarity, tau)
		if got < previous {
			t.Fatalf("relevance fell from %v to %v as similarity rose to %v", previous, got, similarity)
		}
		if got < vectorRelevanceFloor || got > vectorRelevanceCeiling {
			t.Fatalf("relevance %v escaped the [%v, %v] band at similarity %v",
				got, vectorRelevanceFloor, vectorRelevanceCeiling, similarity)
		}
		previous = got
	}
	if got := vectorRelevanceFromSimilarity(1.0, tau); got != vectorRelevanceCeiling {
		t.Fatalf("a perfect similarity must reach the ceiling, got %v", got)
	}
	// Defensive: at or below the floor maps to the band floor, never below.
	if got := vectorRelevanceFromSimilarity(0.10, tau); got != vectorRelevanceFloor {
		t.Fatalf("a sub-floor similarity must clamp to the band floor, got %v", got)
	}
}

// AC-3778-4, the highest-severity bar: a k-NN query always returns k rows, so
// without an absolute floor a no-match question produces k confident-looking
// neighbors. Anything at or below tau must be DROPPED, not scored.
func TestAC_3778_4_NeighborsBelowTheSimilarityFloorAreDroppedNotScored(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		// Distance 0.60 -> similarity 0.40, below the 0.55 floor.
		return []row{{
			"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "Unrelated"}},
			"score": 0.60,
		}}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	candidates, truncated, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0, 0, 0}, 0.55, 5)
	if err != nil {
		t.Fatalf("vectorSearchNodes: %v", err)
	}
	if truncated {
		t.Fatal("a single sub-floor row must not read as a truncated result set")
	}
	if len(candidates) != 0 {
		t.Fatalf("a sub-floor neighbor must be dropped, got %d candidates", len(candidates))
	}
}

// The D11-class rule at the adapter boundary: a vector candidate declares
// Relevance and leaves Score nil, so the raw distance can never reach
// ResultConfidence.
func TestVectorCandidateDeclaresRelevanceAndNeverCarriesTheRawDistance(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		// Distance 0.0 -- a perfect match, the exact value that would read as
		// confidence 0 if it were passed through Score.
		return []row{{
			"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "Auth"}},
			"score": 0.0,
		}}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	candidates, _, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0, 0, 0}, 0.55, 5)
	if err != nil {
		t.Fatalf("vectorSearchNodes: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	candidate := candidates[0]
	if candidate.Score != nil {
		t.Fatalf("a vector candidate must leave Score nil, got %v", *candidate.Score)
	}
	if candidate.Relevance == nil {
		t.Fatal("a vector candidate must declare Relevance")
	}
	if *candidate.Relevance != vectorRelevanceCeiling {
		t.Fatalf("a perfect match must reach the band ceiling, got %v", *candidate.Relevance)
	}
	if candidate.Mechanism != contextfabric.MatchVector {
		t.Fatalf("mechanism = %q, want vector", candidate.Mechanism)
	}
	// And confirm the confidence graphrank would compute is order-correct.
	if got := graphrank.ResultConfidence(candidate.Relevance, candidate.Score); got != vectorRelevanceCeiling {
		t.Fatalf("ResultConfidence = %v, want %v", got, vectorRelevanceCeiling)
	}
}

// AC-3778-5's failure mode: a cold or unreachable embedder must degrade the
// request to lexical-only, never fail it and never block past the budget.
func TestHybridSearchFailsOpenToLexicalWhenTheEmbedderErrors(t *testing.T) {
	lexicalRows := []row{{
		"node": &node{Properties: map[string]interface{}{
			propKind: "project", propCanonicalID: "p1", propLabel: "Auth", propSearchText: "auth service",
		}},
		"score": 2.0,
	}}
	var vectorQueried bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.vector.queryNodes") {
			vectorQueried = true
			return nil, nil
		}
		if strings.Contains(cypher, "db.idx.fulltext.queryNodes") {
			return lexicalRows, nil
		}
		return nil, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{err: errors.New("connection refused")}, 0.55)
	candidates, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth service", 5)
	if err != nil {
		t.Fatalf("an embedder failure must not fail the request: %v", err)
	}
	if vectorQueried {
		t.Fatal("the vector index must not be queried when embedding failed")
	}
	if len(candidates) != 1 || candidates[0].Mechanism != contextfabric.MatchLexical {
		t.Fatalf("expected the lexical candidate to survive, got %#v", candidates)
	}
}

// With no embedder configured at all, the adapter must behave exactly as it
// did before CHAOS-3778.
func TestHybridSearchIsLexicalOnlyWithoutAnEmbedder(t *testing.T) {
	var vectorQueried bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.vector.queryNodes") {
			vectorQueried = true
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5); err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if vectorQueried {
		t.Fatal("no vector query may be issued when no embedder is configured")
	}
}

// A zero SimilarityFloor would silently disable the AC-3778-4 guard, so it
// must not be reachable through a zero-valued options struct.
func TestZeroSimilarityFloorFallsBackToTheDefaultRatherThanDisablingTheGuard(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: []float32{1, 0}}})
	if adapter.similarityFloor <= 0 || adapter.similarityFloor >= 1 {
		t.Fatalf("similarity floor = %v, want a usable default", adapter.similarityFloor)
	}
}

// The vector parameter must be the only list shape falkordb-go's ToString
// accepts without panicking: []interface{} of float64.
func TestVectorParamUsesTheOnlyCodecSafeListShape(t *testing.T) {
	values := vectorParam([]float32{0.5, -0.25})
	if len(values) != 2 {
		t.Fatalf("got %d values", len(values))
	}
	for i, value := range values {
		if _, ok := value.(float64); !ok {
			t.Fatalf("value %d is %T, want float64", i, value)
		}
	}
	if _, err := safeParams(map[string]interface{}{"vec": values}); err != nil {
		t.Fatalf("the vector parameter must pass the codec allowlist: %v", err)
	}
}

// The embedding pass must derive its text from the same expression the
// projection write uses, and must never include a relationship (TRD 19.4.4
// forbids a model in the write path of an edge).
func TestCollectEmbedTargetsMatchesTheProjectedSearchTextAndSkipsEdges(t *testing.T) {
	entity := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Ask Dev"},
		Aliases: []string{"AskDev"},
	}
	batch := contextfabric.ProjectionBatch{
		OrgID:    "org",
		Entities: []contextfabric.EntityProjection{entity},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "r1",
			From:           contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Ask Dev"},
			To:             contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "r", Label: "repo"},
		}},
	}
	targets := collectEmbedTargets(batch, 2000)
	if len(targets) != 1 {
		t.Fatalf("expected exactly one target (the entity, never the edge), got %d", len(targets))
	}
	if targets[0].text != entitySearchText(entity) {
		t.Fatalf("embedded text %q must equal the projected search text %q", targets[0].text, entitySearchText(entity))
	}
}

// AC-3778-7: changing the embedder dimension must force a rebuild, and a
// stale-dimension vector must never be queried. The organization degrades to
// lexical-only rather than failing, because failing would take down lexical
// retrieval too over an optional improvement.
func TestAC_3778_7_DimensionChangeDisablesVectorRetrievalUntilRebuild(t *testing.T) {
	var created bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "CREATE VECTOR INDEX") {
			created = true
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		// An index built at width 4 while the embedder now produces 8.
		return []indexStatus{{
			Label: labelSubject, EntityType: "NODE",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(4)}},
		}}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)
	if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); err != nil {
		t.Fatalf("a dimension mismatch must degrade, not error: %v", err)
	}
	if created {
		t.Fatal("a mismatched index must never be silently dropped and recreated")
	}
	if adapter.vectorEnabledForKey("graphkey") {
		t.Fatal("a stale-dimension index must disable vector retrieval for that organization")
	}
	// A different organization's graph is unaffected.
	if !adapter.vectorEnabledForKey("other-graphkey") {
		t.Fatal("one organization's stale index must not disable another's")
	}
}

// A matching dimension keeps vector retrieval on and does not recreate the
// index.
func TestAC_3778_7_MatchingDimensionKeepsVectorRetrievalEnabled(t *testing.T) {
	var created bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "CREATE VECTOR INDEX") {
			created = true
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{{
			Label: labelSubject, EntityType: "NODE",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
		}}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)
	if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); err != nil {
		t.Fatalf("ensureVectorIndex: %v", err)
	}
	if created {
		t.Fatal("an index at the right dimension must not be recreated")
	}
	if !adapter.vectorEnabledForKey("graphkey") {
		t.Fatal("a matching dimension must keep vector retrieval enabled")
	}
}

// A server that does not report the dimension is UNKNOWN, never a match --
// guessing a match is exactly the failure AC-3778-7 exists to prevent.
func TestAC_3778_7_UnreportedDimensionIsNotTreatedAsAMatch(t *testing.T) {
	status := indexStatus{Options: map[string]interface{}{propEmbedding: map[string]interface{}{}}}
	if _, ok := status.Dimension(); ok {
		t.Fatal("a missing dimension must report ok=false")
	}
	if _, ok := (indexStatus{}).Dimension(); ok {
		t.Fatal("an index with no options must report ok=false")
	}
}
