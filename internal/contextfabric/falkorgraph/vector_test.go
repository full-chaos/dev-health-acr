package falkorgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
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
	if candidate.Relevance.Float() != vectorRelevanceCeiling {
		t.Fatalf("a perfect match must reach the band ceiling, got %v", candidate.Relevance.Float())
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
	candidates, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth service", 5, &resolutionFence{})
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
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}); err != nil {
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
			Status:  "OPERATIONAL",
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
	if usable, err := adapter.vectorIndexUsable(context.Background(), "graphkey"); err != nil || usable {
		t.Fatalf("a stale-dimension index must not be usable (usable=%v err=%v)", usable, err)
	}
	if adapter.ensureVectorReadable(context.Background(), "graphkey", "org") {
		t.Fatal("a stale-dimension index must not pass the read fence")
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
			Status:  "OPERATIONAL",
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
	if !adapter.ensureVectorReadable(context.Background(), "graphkey", "org") {
		t.Fatal("a matching dimension and identity must pass the read-path fence")
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

// wrongModelEmbedServer serves structurally PERFECT embeddings that report a
// different serving model -- the LM Studio failure mode, where the request's
// model field is silently ignored.
func wrongModelEmbedServer(t *testing.T, servedModel string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		data := make([]map[string]any, 0, len(body.Input))
		for i := range body.Input {
			data = append(data, map[string]any{
				"object": "embedding", "index": i, "embedding": make([]float64, 8),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list", "model": servedModel, "data": data,
			"usage": map[string]any{"prompt_tokens": 0, "total_tokens": 0},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func wrongModelEmbedder(t *testing.T, baseURL string) *embedprovider.Embedder {
	t.Helper()
	embedder, err := embedprovider.New(embedprovider.Config{
		Provider: "lmstudio", BaseURL: baseURL,
		// We ASK for one model; the server serves another.
		Model: "text-embedding-nomic-embed-text-v1.5", Dimension: 8,
		SimilarityFloor: 0.55, Timeout: 5 * time.Second,
		MaxBatch: 8, MaxTextRunes: 2000, AllowInsecureBaseURL: true,
	})
	if err != nil {
		t.Fatalf("embedprovider.New: %v", err)
	}
	return embedder
}

// End to end for the LM Studio finding on the READ side: a server that serves
// a different model must degrade the request to lexical-only, and the vector
// index must never be queried with vectors from an unidentified model.
func TestWrongServingModelDegradesTheReadPathToLexical(t *testing.T) {
	// Same width as configured, so the dimension check cannot catch it.
	server := wrongModelEmbedServer(t, "text-embedding-embeddinggemma")
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
	adapter := vectorAdapter(t, fake, wrongModelEmbedder(t, server.URL), 0.55)

	candidates, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth service", 5, &resolutionFence{})
	if err != nil {
		t.Fatalf("a serving-model mismatch must not fail the request: %v", err)
	}
	if vectorQueried {
		t.Fatal("the vector index must never be queried with a vector from an unidentified model")
	}
	if len(candidates) != 1 || candidates[0].Mechanism != contextfabric.MatchLexical {
		t.Fatalf("expected the lexical candidate to survive alone, got %#v", candidates)
	}
}

// End to end on the WRITE side: no vector may be persisted when the serving
// model cannot be confirmed. Silent mixed-vector corruption is unrecoverable
// without a rebuild, so refusing to write is the only safe outcome.
func TestWrongServingModelPersistsNoVector(t *testing.T) {
	server := wrongModelEmbedServer(t, "text-embedding-embeddinggemma")
	var embeddingWritten bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "vecf32($vec)") && strings.Contains(cypher, "SET n.") {
			embeddingWritten = true
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, wrongModelEmbedder(t, server.URL), 0.55)

	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{
		OrgID: "org",
		Entities: []contextfabric.EntityProjection{{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service",
			},
			ObservedAt: observed, SourceVersion: "v1",
		}},
	}
	// embedProjectionBatch degrades rather than erroring -- the canonical
	// projection already succeeded and must not be rolled back over a missing
	// vector.
	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("a successful clear must not fail the batch: %v", err)
	}
	if embeddingWritten {
		t.Fatal("no embedding may be persisted when the serving model is not the configured one")
	}
}

// operationalVectorIndex is a well-formed db.indexes() row for the fence.
func operationalVectorIndex(dimension int64) indexStatus {
	return indexStatus{
		Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL",
		Types:   map[string][]string{propEmbedding: {"VECTOR"}},
		Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": dimension}},
	}
}

// Codex round-1 F5, RED->GREEN: an index that EXISTS but reports no usable
// metadata must DISABLE vector retrieval. The earlier code read it as
// "absent", issued CREATE, treated the resulting already-exists error as
// success, and left vector retrieval enabled against an index of entirely
// unknown width.
func TestF5_IndexWithUnknownMetadataFailsClosedRatherThanBeingTreatedAsAbsent(t *testing.T) {
	cases := []struct {
		name  string
		index indexStatus
	}{
		{"no reported dimension", indexStatus{
			Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{}},
		}},
		{"status still building", indexStatus{
			Label: labelSubject, EntityType: "NODE", Status: "UNDER CONSTRUCTION",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
		}},
		{"undecodable status", indexStatus{
			Label: labelSubject, EntityType: "NODE", Status: "",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var created bool
			fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
				if strings.Contains(cypher, "CREATE VECTOR INDEX") {
					created = true
				}
				return nil, nil
			}}
			fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
				return []indexStatus{testCase.index}, nil
			}
			adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)
			if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); err != nil {
				t.Fatalf("unknown metadata must degrade, not error: %v", err)
			}
			if created {
				t.Fatal("an EXISTING index must never be treated as absent and re-created")
			}
			if usable, err := adapter.vectorIndexUsable(context.Background(), "graphkey"); err != nil || usable {
				t.Fatalf("an index with unknown metadata must not be usable (usable=%v err=%v)", usable, err)
			}
		})
	}
}

// Codex round-1 F2, RED->GREEN: the read path must verify the fence itself.
// The hosted API never runs bootstrap, so before this the identity fence was
// write-only and a same-dimension model swap served stale vectors into
// resolution.
func TestF2_ReadPathVerifiesTheStoredEmbedderIdentity(t *testing.T) {
	lexicalRows := []row{{
		"node": &node{Properties: map[string]interface{}{
			propKind: "project", propCanonicalID: "p1", propLabel: "Auth", propSearchText: "auth service",
		}},
		"score": 2.0,
	}}
	var vectorQueried, identityChecked bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			vectorQueried = true
			return nil, nil
		case strings.Contains(cypher, propEmbedderIdentity):
			identityChecked = true
			// One node carries a DIFFERENT model's identity -- the
			// same-dimension swap the width check cannot see.
			return []row{{"n.canonical_id": "p9"}}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return lexicalRows, nil
		}
		return nil, nil
	}}
	// The index width MATCHES, so only the identity check can catch this.
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	candidates, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth service", 5, &resolutionFence{})
	if err != nil {
		t.Fatalf("a stale-identity graph must degrade, not fail: %v", err)
	}
	if !degraded {
		t.Fatal("a fenced-off vector mechanism must report degradation to the caller")
	}
	if !identityChecked {
		t.Fatal("the read path must verify the stored embedder identity")
	}
	if vectorQueried {
		t.Fatal("vectors from a different model must never be queried")
	}
	if len(candidates) != 1 || candidates[0].Mechanism != contextfabric.MatchLexical {
		t.Fatalf("lexical retrieval must proceed, got %#v", candidates)
	}
}

// A graph whose stored identity matches passes the read fence and queries.
func TestF2_MatchingStoredIdentityPassesTheReadFence(t *testing.T) {
	var vectorQueried bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.vector.queryNodes") {
			vectorQueried = true
		}
		// The identity probe returns NO mismatching rows.
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}); err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if !vectorQueried {
		t.Fatal("a matching identity and dimension must allow the vector query")
	}
}

// Codex round-1 F1, RED->GREEN: a vector query whose every row falls below the
// similarity floor found NOTHING, and must therefore claim NO truncation
// authority. The earlier order set the truncation flag from the raw row count
// before the tau filter ran, so such a query returned zero candidates while
// reporting truncated=true -- and truncation is checked BEFORE any confidence
// threshold in ResolveFromMergedCandidates, so it could force the whole
// resolution to ambiguous and block an unopposed, otherwise-strong lexical
// commit.
func TestF1_AllBelowFloorVectorQueryClaimsNoTruncationAuthority(t *testing.T) {
	const limit = 2
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		// limit+1 rows, EVERY one below the 0.55 floor (distance 0.9 ->
		// similarity 0.10). Enough rows to have tripped the old flag.
		rows := make([]row, 0, limit+1)
		for i := 0; i < limit+1; i++ {
			rows = append(rows, row{
				"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p", propLabel: "Unrelated"}},
				"score": 0.9,
			})
		}
		return rows, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	candidates, truncated, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0, 0, 0}, 0.55, limit)
	if err != nil {
		t.Fatalf("vectorSearchNodes: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("every row was below the floor; expected no candidates, got %d", len(candidates))
	}
	if truncated {
		t.Fatal("a vector query that found NOTHING must not claim truncation authority")
	}
}

// The other half of F1: truncation is still reported when it is real -- more
// than `limit` rows genuinely CLEARED the floor.
func TestF1_TruncationIsStillReportedWhenAboveFloorRowsExceedTheLimit(t *testing.T) {
	const limit = 2
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		rows := make([]row, 0, limit+1)
		for i := 0; i < limit+1; i++ {
			rows = append(rows, row{
				"node": &node{Properties: map[string]interface{}{
					propKind: "project", propCanonicalID: fmt.Sprintf("p%d", i), propLabel: "Auth",
				}},
				// distance 0.1 -> similarity 0.90, comfortably above tau.
				"score": 0.1,
			})
		}
		return rows, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	candidates, truncated, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0, 0, 0}, 0.55, limit)
	if err != nil {
		t.Fatalf("vectorSearchNodes: %v", err)
	}
	if !truncated {
		t.Fatal("more above-floor rows than the budget must report truncation")
	}
	if len(candidates) != limit {
		t.Fatalf("the extra detection row must be discarded, got %d candidates", len(candidates))
	}
	// A truncated batch caps every survivor at the band floor.
	for _, candidate := range candidates {
		if candidate.Relevance.Float() != vectorRelevanceFloor {
			t.Fatalf("a truncated batch must floor-cap its survivors, got %v", candidate.Relevance.Float())
		}
	}
}

// Exactly `limit` survivors after tau filtering is NOT truncation: the k-NN
// result is ordered by ascending distance, so if the (limit+1)th row fell
// below the floor, everything beyond it is further away and also below it --
// no genuine competitor was cut off.
func TestF1_SurvivorsAtExactlyTheLimitAreNotTruncated(t *testing.T) {
	const limit = 2
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		return []row{
			{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p0", propLabel: "A"}}, "score": 0.1},
			{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "B"}}, "score": 0.2},
			// The detection row falls below the floor.
			{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p2", propLabel: "C"}}, "score": 0.9},
		}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	candidates, truncated, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0, 0, 0}, 0.55, limit)
	if err != nil {
		t.Fatalf("vectorSearchNodes: %v", err)
	}
	if truncated {
		t.Fatal("a below-floor detection row must not read as truncation")
	}
	if len(candidates) != limit {
		t.Fatalf("expected %d above-floor candidates, got %d", limit, len(candidates))
	}
	// Not truncated, so relevance is the real band value, not the floor cap.
	if candidates[0].Relevance.Float() <= vectorRelevanceFloor {
		t.Fatalf("an untruncated batch must carry real band relevance, got %v", candidates[0].Relevance.Float())
	}
}

// Codex round-1 F3, RED->GREEN: when embedding fails after the batch has
// already written NEW search_text, the OLD vector must be CLEARED. Leaving it
// pairs model-A's understanding of yesterday's text with today's text,
// permanently -- the watermark advances, nothing retries until a rebuild, and
// no read-side check can see it because the vector is present, well-formed,
// and stamped with a matching identity.
func TestF3_EmbedFailureClearsTheStaleVectorInsteadOfLeavingIt(t *testing.T) {
	var cleared bool
	var clearedTargets []interface{}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "SET n."+propEmbedding+" = NULL") {
			cleared = true
			if list, ok := params["targets"].([]interface{}); ok {
				clearedTargets = list
			}
		}
		return nil, nil
	}}
	// The write side re-verifies the index every batch (R2-1), so the fake
	// must present an operational index at the embedder's own dimension.
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8), err: errors.New("connection refused")}, 0.55)

	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{
		OrgID: "org",
		Entities: []contextfabric.EntityProjection{{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service",
			},
			ObservedAt: observed, SourceVersion: "v1",
		}},
	}
	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("a successful clear must not fail the batch: %v", err)
	}

	if !cleared {
		t.Fatal("an embed failure must CLEAR the stale vector, not leave it attached to new text")
	}
	if len(clearedTargets) != 1 {
		t.Fatalf("expected exactly the batch's own node to be cleared, got %d", len(clearedTargets))
	}
}

// Mid-batch write failure clears only the nodes that still carry yesterday's
// vector -- never the ones this batch already refreshed.
func TestF3_MidBatchWriteFailureClearsOnlyTheUnwrittenNodes(t *testing.T) {
	writes := 0
	var clearedTargets []interface{}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "SET n."+propEmbedding+" = NULL"):
			if list, ok := params["targets"].([]interface{}); ok {
				clearedTargets = list
			}
		case strings.Contains(cypher, "vecf32($vec)"):
			writes++
			if writes == 2 {
				return nil, errors.New("write failed")
			}
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)

	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{OrgID: "org"}
	for _, id := range []string{"p1", "p2", "p3"} {
		batch.Entities = append(batch.Entities, contextfabric.EntityProjection{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: id, Label: "Subject " + id,
			},
			ObservedAt: observed, SourceVersion: "v1",
		})
	}
	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("a successful clear must not fail the batch: %v", err)
	}

	// Targets are sorted; the second write failed, so targets[1:] (two nodes)
	// still hold yesterday's vectors and must be cleared. The first was
	// refreshed successfully and must be left alone.
	if len(clearedTargets) != 2 {
		t.Fatalf("expected the two unwritten nodes to be cleared, got %d", len(clearedTargets))
	}
}

// Codex round-2 R2-1, RED->GREEN: the fence verdict must NOT survive across
// requests. The earlier design cached an ENABLED verdict for the process
// lifetime on the premise that configuration cannot change without a restart
// -- true per PROCESS, false per DEPLOYMENT, since acr-api and acr-projector
// configure their embedders independently. A differently-configured projector
// writing same-dimension identity-B vectors would then be served forever by an
// API that never probed again.
func TestR2_1_FenceIsReVerifiedOnEveryResolutionNotCachedAcrossThem(t *testing.T) {
	identityProbes := 0
	mismatch := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, propEmbedderIdentity) {
			identityProbes++
			if mismatch {
				return []row{{"n.canonical_id": "p9"}}, nil
			}
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	// First resolution: clean, so vector retrieval is enabled.
	if !adapter.ensureVectorReadable(context.Background(), "k", "org") {
		t.Fatal("a clean graph must pass the fence")
	}
	if identityProbes != 1 {
		t.Fatalf("expected one identity probe, got %d", identityProbes)
	}

	// A differently-configured projector now writes identity-B vectors.
	mismatch = true

	// A LATER resolution must notice. Under the old process-lifetime ENABLED
	// cache this returned true forever without probing again.
	if adapter.ensureVectorReadable(context.Background(), "k", "org") {
		t.Fatal("a later resolution must re-verify and reject drifted vectors")
	}
	if identityProbes != 2 {
		t.Fatalf("the fence must be re-probed per resolution, got %d probes", identityProbes)
	}
}

// The memo bounds the cost to ONE probe per resolution, not one per term --
// ResolveSubjects issues a Search per interpreted subject term.
func TestR2_1_ResolutionFenceProbesOncePerResolutionNotPerTerm(t *testing.T) {
	identityProbes := 0
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, propEmbedderIdentity) {
			identityProbes++
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	fence := &resolutionFence{}
	for _, term := range []string{"the auth work", "authentication", "login"} {
		if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", term, 5, fence); err != nil {
			t.Fatalf("hybridSearchNodes(%q): %v", term, err)
		}
	}
	if identityProbes != 1 {
		t.Fatalf("one resolution must probe once across all its terms, got %d", identityProbes)
	}

	// A NEW resolution gets a new fence and therefore a new probe.
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "again", 5, &resolutionFence{}); err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if identityProbes != 2 {
		t.Fatalf("a new resolution must probe again, got %d", identityProbes)
	}
}

// Codex round-2 R2-2, RED->GREEN: a node with an indexed embedding and a NULL
// identity is UNKNOWN provenance, and unknown must not read as verified. The
// earlier predicate asked only "identity IS NOT NULL AND <> configured", so
// such a node passed as clean.
func TestR2_2_VectoredNodeWithNullIdentityTripsTheFence(t *testing.T) {
	var probeCypher string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, propEmbedderIdentity) {
			probeCypher = cypher
			// The server finds a node matching the predicate.
			return []row{{"n.canonical_id": "p_unstamped"}}, nil
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	if adapter.ensureVectorReadable(context.Background(), "k", "org") {
		t.Fatal("a node of unknown vector provenance must trip the fence")
	}
	// The predicate must be anchored on the EMBEDDING being present and must
	// admit a NULL identity as a mismatch -- not merely a differing one.
	if !strings.Contains(probeCypher, "n."+propEmbedding+" IS NOT NULL") {
		t.Fatalf("the probe must be anchored on the embedding being present: %s", probeCypher)
	}
	if !strings.Contains(probeCypher, "n."+propEmbedderIdentity+" IS NULL") {
		t.Fatalf("the probe must treat a NULL identity as unverified: %s", probeCypher)
	}
}

// Codex round-2 R2-3, RED->GREEN: when the embed fails AND the clear also
// fails, the batch must FAIL so the projection checkpoint does not advance
// past unreconciled vector state. Telemetry is not containment: the stale
// vector still carries the CONFIGURED identity and dimension, so the read
// fence sees nothing wrong and serves it.
func TestR2_3_FailedClearFailsTheBatchSoTheCheckpointCannotAdvance(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "SET n."+propEmbedding+" = NULL") {
			return nil, errors.New("clear failed")
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8), err: errors.New("embed failed")}, 0.55)

	batch := contextfabric.ProjectionBatch{
		OrgID: "org",
		Entities: []contextfabric.EntityProjection{{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service",
			},
			ObservedAt: time.Now().UTC(), SourceVersion: "v1",
		}},
	}
	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err == nil {
		t.Fatal("an unreconciled vector state must fail the batch, not degrade silently")
	}
}

// The full ApplyProjectionBatch path: a failed clear must stop the watermark
// write, so the checkpoint genuinely cannot advance. Projection is idempotent,
// so the next tick replays and reconciles.
func TestR2_3_ApplyProjectionBatchDoesNotWriteTheWatermarkOnUnreconciledVectors(t *testing.T) {
	watermarkWritten := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "SET n."+propEmbedding+" = NULL"):
			return nil, errors.New("clear failed")
		case strings.Contains(cypher, labelWatermark):
			watermarkWritten = true
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	fake.constraintsFunc = func(ctx context.Context, key string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8), err: errors.New("embed failed")}, 0.55)

	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r23_00000001", OrgID: "org",
		Source: "r2-3-test", SourceVersion: "v1", Cursor: "c1", NextCursor: "c2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service",
			},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_vector_1234"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err == nil {
		t.Fatal("ApplyProjectionBatch must fail when vector state is unreconciled")
	}
	if watermarkWritten {
		t.Fatal("the watermark must not be written for a batch whose vector state is unreconciled")
	}
}
