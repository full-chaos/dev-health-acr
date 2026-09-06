package falkorgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// stubEmbedder is a fully in-process contextfabric.Embedder double.
type stubEmbedder struct {
	vector []float32
	err    error
	calls  int
	// failFirstN, when > 0, makes Embed return err for exactly the first
	// failFirstN calls and succeed on every call after (CHAOS-4259: proves
	// a bounded retry-then-succeed path). Zero (the default) keeps the
	// pre-existing behavior: err, if set, fails EVERY call.
	failFirstN int
}

func (s *stubEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	s.calls++
	if s.err != nil && (s.failFirstN == 0 || s.calls <= s.failFirstN) {
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

// TestAC_3778_4_NeighborAtExactlyTheSimilarityFloorIsDroppedNotScored pins
// aboveSimilarityFloor's STRICT predicate at the production call site: a
// candidate whose similarity exactly EQUALS tau is dropped exactly like one
// below it, never scored. Distance 0.45 -> similarity 1-0.45 = 0.55, exactly
// the configured floor. codex round-1 P1 sibling check: if
// vectorSearchNodesWithOverFetch's `!aboveSimilarityFloor(...)` guard ever
// regressed to a non-strict `similarity < tau`, this exact-equality row would
// wrongly survive as a candidate.
func TestAC_3778_4_NeighborAtExactlyTheSimilarityFloorIsDroppedNotScored(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		// Distance 0.45 -> similarity exactly 0.55, equal to the floor below.
		return []row{{
			"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "Unrelated"}},
			"score": 0.45,
		}}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	candidates, truncated, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0, 0, 0}, 0.55, 5)
	if err != nil {
		t.Fatalf("vectorSearchNodes: %v", err)
	}
	if truncated {
		t.Fatal("a single at-floor row must not read as a truncated result set")
	}
	if len(candidates) != 0 {
		t.Fatalf("a neighbor exactly AT the similarity floor must be dropped (strict >, never >=), got %d candidates", len(candidates))
	}
}

// TestAboveSimilarityFloor_BoundaryIsStrict pins the shared production
// predicate directly: equal-to-tau is NOT above the floor, and the smallest
// possible float64 step above tau IS. tau_calibration.go's recall/reject-
// rate/near-duplicate accounting all call this exact function (codex
// round-1 P1), so this is the single point of truth their pinning tests rely
// on.
func TestAboveSimilarityFloor_BoundaryIsStrict(t *testing.T) {
	tau := 0.30
	if aboveSimilarityFloor(tau, tau) {
		t.Fatal("aboveSimilarityFloor(tau, tau) = true, want false -- a sample exactly at tau must not be counted as clearing it")
	}
	justAbove := math.Nextafter(tau, math.Inf(1))
	if !aboveSimilarityFloor(justAbove, tau) {
		t.Fatal("aboveSimilarityFloor(one ULP above tau, tau) = false, want true")
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

// TestResolveSubjects_OverFetchLetsACorroboratedCandidateBeyondRawVectorRankWin
// is the codex round-2 P2 fix's end-to-end proof, through the REAL caller
// contract (graphrank.ResolveSubjects, exactly as reader.go's ResolveSubjects
// wires it -- not a hand-rolled shortcut). Fixture: three subjects all clear
// tau, but "target" is raw-vector rank 3 (two other, DISTINCT subjects are
// closer by cosine distance) while MaxSubjectCandidates=2. "target" is ALSO
// found lexically (the same term), so once it reaches graphrank at all it is
// corroborated (MatchLexical + MatchVector), which -- per the D11-class
// invariant (vectorRelevanceCeiling's doc comment) -- guarantees Confidence
// in [0.72, 0.86], strictly above ANY single-mechanism vector candidate's
// ceiling of 0.70. So the only question this test needs to settle is whether
// "target" reaches graphrank's ranking AT ALL: before the fix it could not
// (vectorSearchNodesWithOverFetch discarded anything past raw rank `limit`,
// for any multiplier); with the fix and a calibrated multiplier=3
// (returnCap=6 >= 3), it does, and then wins the final top-2 truncation on
// ranking, not raw ANN position -- proving the widened pool genuinely flows
// through cross-mechanism ranking (ResolveFromMergedCandidates' documented
// "rank first, truncate last" architecture), not just through this
// function's own local slice.
func TestResolveSubjects_OverFetchLetsACorroboratedCandidateBeyondRawVectorRankWin(t *testing.T) {
	vectorRows := []row{
		{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "closer-a", propLabel: "Closer A", propSearchText: "unrelated"}}, "score": 0.00},
		{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "closer-b", propLabel: "Closer B", propSearchText: "unrelated"}}, "score": 0.05},
		{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "target", propLabel: "Target Project", propSearchText: "target project"}}, "score": 0.10},
	}
	lexicalRows := []row{
		fulltextRow("project", "target", "Target Project", "target project", nil),
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return vectorRows, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return lexicalRows, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(2)}, nil
	}
	adapter := newFakeAdapter(t, fake)
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: []float32{1, 0}}, SimilarityFloor: 0.55, OverFetchMultiplier: 3})

	request := contextfabric.InvestigationRequest{
		Question: "target project",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 2, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"target project"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}

	resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	var target *contextfabric.SubjectCandidate
	for i, c := range resolution.Candidates {
		if c.Subject.CanonicalID == "target" {
			target = &resolution.Candidates[i]
		}
	}
	if target == nil {
		t.Fatalf("resolution.Candidates = %#v, want \"target\" present -- raw vector rank 3 beyond MaxSubjectCandidates=2, but corroborated (lexical+vector) once the over-fetched pool reaches ranking", resolution.Candidates)
	}
	// The precise assertion, not just presence: "target" is ALSO found
	// lexically regardless of the vector-arm fix, so merely appearing in
	// Candidates is not proof of anything -- lexical alone could put it
	// there. What only the fix can produce is BOTH mechanisms recorded on
	// it, proving the vector arm's over-fetched pool (not just the lexical
	// arm) is what reached ranking for this subject.
	if !graphrank.HasMechanism(target.MatchMechanisms, contextfabric.MatchVector) {
		t.Fatalf("target.MatchMechanisms = %v, want MatchVector present -- the widened vector pool must have reached ranking for this candidate, not just the independent lexical hit", target.MatchMechanisms)
	}
	if !graphrank.HasMechanism(target.MatchMechanisms, contextfabric.MatchLexical) {
		t.Fatalf("target.MatchMechanisms = %v, want MatchLexical present too (this fixture's corroboration setup)", target.MatchMechanisms)
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
	candidates, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth service", 5, &resolutionFence{}, temporalFilter{})
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
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, temporalFilter{}); err != nil {
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
	targets, _, skipped := collectEmbedTargets(batch, 2000, false)
	if len(targets) != 1 {
		t.Fatalf("expected exactly one target (the entity, never the edge), got %d", len(targets))
	}
	if skipped.Total() != 0 {
		t.Fatalf("expected no skipped nodes, got %+v", skipped)
	}
	if targets[0].text != subjectSearchText(entity, false) {
		t.Fatalf("embedded text %q must equal the projected search text %q", targets[0].text, subjectSearchText(entity, false))
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
		SimilarityFloor: 0.55, Timeout: 5 * time.Second, BatchTimeout: 5 * time.Second,
		MaxBatch: 8, MaxTextRunes: 2000, AllowInsecureBaseURL: true,
		// No credential, matching this fixture's own "lmstudio" no-auth
		// modeling -- explicit AllowNoCredential (CHAOS-4192).
		AllowNoCredential: true,
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

	candidates, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth service", 5, &resolutionFence{}, temporalFilter{})
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
			adapter.config.RequestTimeout = 50 * time.Millisecond
			if err := adapter.ensureVectorIndex(context.Background(), "graphkey"); !errors.Is(err, errVectorIndexNotReady) {
				t.Fatalf("an index that never becomes readable must fail loudly, got %v", err)
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

	candidates, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth service", 5, &resolutionFence{}, temporalFilter{})
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
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, temporalFilter{}); err != nil {
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
		if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", term, 5, fence, temporalFilter{}); err != nil {
			t.Fatalf("hybridSearchNodes(%q): %v", term, err)
		}
	}
	if identityProbes != 1 {
		t.Fatalf("one resolution must probe once across all its terms, got %d", identityProbes)
	}

	// A NEW resolution gets a new fence and therefore a new probe.
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "again", 5, &resolutionFence{}, temporalFilter{}); err != nil {
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

// recordingTelemetry captures every graph signal so a test can assert what an
// operator would actually see.
type recordingTelemetry struct {
	degraded      int
	suppressed    int
	embedded      int
	cleared       int
	skipped       int
	skippedKind   int
	skippedIDOnly int
	projections   int

	// efRuntimeMismatches records every RecordVectorIndexEfRuntimeMismatch
	// call verbatim (codex round-9 P2 wiring fix's test double) -- a slice,
	// not just a count, so a test can assert the exact key/policy/index
	// values reported, not merely that SOMETHING fired.
	efRuntimeMismatches []efRuntimeMismatchRecord

	// identityGraphMissing counts every RecordIdentityGraphMissing call's
	// count argument (CHAOS-3884).
	identityGraphMissing int

	// cohortKindBases records every RecordCohortKindBasis call verbatim.
	cohortKindBases []cohortKindBasisRecord

	// vectorFences/lexiconExpansions record every RecordVectorFence/
	// RecordLexiconExpansion call verbatim (CHAOS-3890) -- slices, so a
	// test can assert the exact reason/memoized or fired/batch/added/
	// truncated values reported, not merely that SOMETHING fired.
	vectorFences      []vectorFenceRecord
	lexiconExpansions []lexiconExpansionRecord

	// subjectCandidatesAuthzDropped, cohortMembersAuthzDropped, and the
	// edgesFiltered* fields count every CHAOS-3888 authz-drop-observability
	// call's count argument.
	subjectCandidatesAuthzDropped int
	cohortMembersAuthzDropped     int
	edgesFilteredAuthz            int
	edgesFilteredTemporalWindow   int
	edgesFilteredSelfLoop         int

	// cohortDeniedByAuthorization counts every RecordCohortDeniedByAuthorization
	// call's count argument (CHAOS-4577) -- distinct from
	// cohortMembersAuthzDropped, which fires on every authz-narrowed cohort;
	// this only fires when the WHOLE cohort was denied.
	cohortDeniedByAuthorization int

	// cohortExactNameCensusGates records every RecordCohortExactNameCensusGate
	// call verbatim (CHAOS-4622 remainder) -- a slice, so a test can assert
	// the exact admitted/basis pair reported, not merely that the gate fired.
	cohortExactNameCensusGates []cohortExactNameCensusGateRecord
	neighborLookupFailures     []neighborLookupFailureRecord

	// embedFailureEscalations records every
	// RecordVectorProjectionEmbedFailuresEscalated call verbatim
	// (CHAOS-4259) -- a slice, so a test can assert exactly when escalation
	// started firing and with what consecutive-failure count, not merely
	// that it fired at all.
	embedFailureEscalations []embedFailureEscalationRecord
}

// embedFailureEscalationRecord is one recorded
// RecordVectorProjectionEmbedFailuresEscalated call's arguments.
type embedFailureEscalationRecord struct {
	orgID               string
	consecutiveFailures int
	transient           bool
}

// vectorFenceRecord is one recorded RecordVectorFence call's arguments.
type vectorFenceRecord struct {
	orgID    string
	result   VectorFenceResult
	memoized bool
}

// lexiconExpansionRecord is one recorded RecordLexiconExpansion call's
// arguments.
type lexiconExpansionRecord struct {
	orgID                string
	fired                bool
	batchCount           int
	addedCandidates      int
	truncatedByExpansion bool
}

// efRuntimeMismatchRecord is one recorded
// RecordVectorIndexEfRuntimeMismatch call's arguments.
type efRuntimeMismatchRecord struct {
	key                             string
	policyEfRuntime, indexEfRuntime int
}

// cohortExactNameCensusGateRecord is one recorded
// RecordCohortExactNameCensusGate call's arguments (CHAOS-4622 remainder).
type cohortExactNameCensusGateRecord struct {
	orgID    string
	admitted bool
	basis    CohortExactNameCensusBasis
}

// cohortKindBasisRecord is one RecordCohortKindBasis call, verbatim -- the
// same slice-not-a-count shape efRuntimeMismatches and vectorFences already
// use, so a test can assert WHICH basis was reported and whether a cohort
// came back, never merely that something fired.
type cohortKindBasisRecord struct {
	orgID        string
	declaredKind contextfabric.SubjectKind
	basis        graphrank.CohortKindBasis
	discovered   bool
	// poolTruncation (CHAOS-5168) is recorded for the same reason the
	// fields above it are: a test that asserts a cohort was built from a
	// clipped pool must read what the production emit reported, never
	// merely that the emit fired.
	poolTruncation     CohortPoolTruncationBasis
	poolTruncationArms []CohortPoolTruncationArm
}

func (r *recordingTelemetry) RecordObservationTraversalDegraded(context.Context, string, int) {}
func (r *recordingTelemetry) RecordVectorRetrievalDegraded(context.Context, string) {
	r.degraded++
}
func (r *recordingTelemetry) RecordVectorRetrievalSuppressed(context.Context, string) {
	r.suppressed++
}
func (r *recordingTelemetry) RecordVectorProjection(_ context.Context, _ string, embedded, cleared, skippedKind, skippedIDOnly int) {
	r.projections++
	r.embedded += embedded
	r.cleared += cleared
	r.skipped += skippedKind + skippedIDOnly
	r.skippedKind += skippedKind
	r.skippedIDOnly += skippedIDOnly
}
func (r *recordingTelemetry) RecordVectorProjectionEmbedFailuresEscalated(_ context.Context, orgID string, consecutiveFailures int, transient bool) {
	r.embedFailureEscalations = append(r.embedFailureEscalations, embedFailureEscalationRecord{orgID: orgID, consecutiveFailures: consecutiveFailures, transient: transient})
}
func (r *recordingTelemetry) RecordVectorIndexEfRuntimeMismatch(_ context.Context, key string, policyEfRuntime, indexEfRuntime int) {
	r.efRuntimeMismatches = append(r.efRuntimeMismatches, efRuntimeMismatchRecord{key, policyEfRuntime, indexEfRuntime})
}
func (r *recordingTelemetry) RecordIdentityGraphMissing(_ context.Context, _ string, count int) {
	r.identityGraphMissing += count
}
func (r *recordingTelemetry) RecordVectorFence(_ context.Context, orgID string, result VectorFenceResult, memoized bool) {
	r.vectorFences = append(r.vectorFences, vectorFenceRecord{orgID: orgID, result: result, memoized: memoized})
}
func (r *recordingTelemetry) RecordLexiconExpansion(_ context.Context, orgID string, fired bool, batchCount, addedCandidates int, truncatedByExpansion bool) {
	r.lexiconExpansions = append(r.lexiconExpansions, lexiconExpansionRecord{
		orgID: orgID, fired: fired, batchCount: batchCount, addedCandidates: addedCandidates, truncatedByExpansion: truncatedByExpansion,
	})
}
func (r *recordingTelemetry) RecordSubjectCandidatesAuthzDropped(_ context.Context, _ string, count int) {
	r.subjectCandidatesAuthzDropped += count
}
func (r *recordingTelemetry) RecordCohortMembersAuthzDropped(_ context.Context, _ string, count int) {
	r.cohortMembersAuthzDropped += count
}
func (r *recordingTelemetry) RecordEdgesFilteredByReason(_ context.Context, _ string, authz, temporalWindow, selfLoop int) {
	r.edgesFilteredAuthz += authz
	r.edgesFilteredTemporalWindow += temporalWindow
	r.edgesFilteredSelfLoop += selfLoop
}
func (r *recordingTelemetry) RecordCohortDeniedByAuthorization(_ context.Context, _ string, count int) {
	r.cohortDeniedByAuthorization += count
}
func (r *recordingTelemetry) RecordCohortExactNameCensusGate(_ context.Context, orgID string, admitted bool, basis CohortExactNameCensusBasis) {
	r.cohortExactNameCensusGates = append(r.cohortExactNameCensusGates, cohortExactNameCensusGateRecord{orgID: orgID, admitted: admitted, basis: basis})
}

// RecordCohortKindBasis RECORDS. It used to discard every argument, which
// made it a discarding fake wearing a recorder's name: deleting the
// production emit at reader.go would not have failed a single test in this
// package. The cohort-kind basis is the one signal that distinguishes "the
// graph had no matching nodes" from "the seam refused this kind", so a test
// asserting a refusal or a discovery needs to read it, not merely to have
// caused it.
func (r *recordingTelemetry) RecordCohortKindBasis(_ context.Context, orgID string, declaredKind contextfabric.SubjectKind, basis graphrank.CohortKindBasis, discovered bool, poolTruncation CohortPoolTruncationBasis, poolTruncationArms []CohortPoolTruncationArm) {
	r.cohortKindBases = append(r.cohortKindBases, cohortKindBasisRecord{orgID: orgID, declaredKind: declaredKind, basis: basis, discovered: discovered, poolTruncation: poolTruncation, poolTruncationArms: poolTruncationArms})
}

func (r *recordingTelemetry) RecordNeighborLookupFailed(_ context.Context, orgID, originCanonicalID, neighborUUID string, site NeighborLookupFailureSite, err error) {
	r.neighborLookupFailures = append(r.neighborLookupFailures, neighborLookupFailureRecord{
		orgID: orgID, originCanonicalID: originCanonicalID, neighborUUID: neighborUUID, site: site, err: err,
	})
}

// neighborLookupFailureRecord is one RecordNeighborLookupFailed call, verbatim.
// Recorded rather than counted for the same reason the production line carries
// identifiers at all: a count cannot say WHICH member went missing.
type neighborLookupFailureRecord struct {
	orgID             string
	originCanonicalID string
	neighborUUID      string
	// site is WHICH read failed. Recorded because one counter has three
	// producers: without it a test can prove the loss was reported and still
	// not prove the reported site is the one that actually fired.
	site NeighborLookupFailureSite
	err  error
}

func vectorAdapterWithTelemetry(t *testing.T, fake *fakeConn, embedder contextfabric.Embedder, telemetry GraphTelemetry) *Adapter {
	t.Helper()
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true, Telemetry: telemetry,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI: %v", err)
	}
	adapter.attachEmbedder(EmbedderOptions{Embedder: embedder, SimilarityFloor: 0.55})
	return adapter
}

// vectorAdapterWithRetryConfig is vectorAdapterWithTelemetry plus explicit
// CHAOS-4259 embed-failure retry/escalation knobs (the zero value of which
// disables both -- see Config's own field doc comments), for tests that
// exercise embedProjectionBatch's retry-before-clear and
// escalate-after-N-consecutive-failures behavior.
func vectorAdapterWithRetryConfig(t *testing.T, fake *fakeConn, embedder contextfabric.Embedder, telemetry GraphTelemetry, maxRetries int, backoff time.Duration, escalateAfter int) *Adapter {
	t.Helper()
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true, Telemetry: telemetry,
		EmbedFailureMaxRetries: maxRetries, EmbedFailureRetryBackoff: backoff, EmbedFailureEscalateAfter: escalateAfter,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI: %v", err)
	}
	adapter.attachEmbedder(EmbedderOptions{Embedder: embedder, SimilarityFloor: 0.55})
	return adapter
}

func vectorProbeBatch(orgID string) contextfabric.ProjectionBatch {
	observed := time.Now().UTC()
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r3_00000001", OrgID: orgID,
		Source: "round3-test", SourceVersion: "v1", Cursor: "c1", NextCursor: "c2", GeneratedAt: observed,
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
}

// Codex round-3 F1, RED->GREEN: an index-verification failure is the
// failed-VERIFY door into the room R2-3 closed via failed-CLEAR. The batch
// wrote new search_text; if verification fails and the batch commits, the OLD
// vector stays attached, still carrying the CONFIGURED identity, so once the
// transient condition clears the read fence sees nothing wrong and serves it
// against text it was never derived from -- permanently, because the
// checkpoint advanced.
func TestR3_F1_IndexProbeFailureFailsTheBatchBeforeTheWatermark(t *testing.T) {
	cases := []struct {
		name    string
		indexes func(ctx context.Context, key string) ([]indexStatus, error)
	}{
		{"probe errors", func(context.Context, string) ([]indexStatus, error) {
			return nil, errors.New("transient GRAPH.QUERY failure")
		}},
		{"index still under construction", func(context.Context, string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, EntityType: "NODE", Status: "UNDER CONSTRUCTION",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
			}}, nil
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			watermarkWritten := false
			embeddingWritten := false
			fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
				switch {
				case strings.Contains(cypher, labelWatermark):
					watermarkWritten = true
				case strings.Contains(cypher, "vecf32($vec)"):
					embeddingWritten = true
				}
				return nil, nil
			}}
			fake.indexesFunc = testCase.indexes
			fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
				return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
			}
			adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

			if _, err := adapter.ApplyProjectionBatch(context.Background(), vectorProbeBatch("org")); err == nil {
				t.Fatal("a batch that cannot verify the vector index must fail, not commit")
			}
			if watermarkWritten {
				t.Fatal("the watermark must not advance past a batch whose vector state is unverified")
			}
			if embeddingWritten {
				t.Fatal("no embedding may be written against an unverified index")
			}
		})
	}
}

// The replay half: once the index becomes readable, the same batch reconciles
// and commits. A failing batch that could never succeed would be a stall, not
// containment.
func TestR3_F1_BatchReconcilesOnReplayOnceTheIndexIsReady(t *testing.T) {
	ready := false
	watermarkWritten := false
	embeddingWritten := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, labelWatermark):
			watermarkWritten = true
		case strings.Contains(cypher, "vecf32($vec)"):
			embeddingWritten = true
		}
		return nil, nil
	}}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		if !ready {
			return []indexStatus{{
				Label: labelSubject, EntityType: "NODE", Status: "UNDER CONSTRUCTION",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
			}}, nil
		}
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	if _, err := adapter.ApplyProjectionBatch(context.Background(), vectorProbeBatch("org")); err == nil {
		t.Fatal("RED case is not red: the first attempt was expected to fail")
	}
	if watermarkWritten {
		t.Fatal("the first attempt must not advance the watermark")
	}

	ready = true
	if _, err := adapter.ApplyProjectionBatch(context.Background(), vectorProbeBatch("org")); err != nil {
		t.Fatalf("the replayed batch must reconcile and commit: %v", err)
	}
	if !embeddingWritten {
		t.Fatal("the replayed batch must embed the node it previously could not")
	}
	if !watermarkWritten {
		t.Fatal("the replayed batch must advance the watermark")
	}
}

// A PERSISTENT dimension mismatch is the one verification failure that must
// NOT replay forever: it needs operator action, and stalling canonical
// projection indefinitely over an optional retrieval feature is worse than
// degraded retrieval. The batch makes its own vector state honest -- clears --
// and commits.
func TestR3_F1_PersistentDimensionMismatchClearsAndCommitsRatherThanStalling(t *testing.T) {
	watermarkWritten := false
	cleared := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, labelWatermark):
			watermarkWritten = true
		case strings.Contains(cypher, "SET n."+propEmbedding+" = NULL"):
			cleared = true
		}
		return nil, nil
	}}
	// Index built at 4; embedder now produces 8.
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	if _, err := adapter.ApplyProjectionBatch(context.Background(), vectorProbeBatch("org")); err != nil {
		t.Fatalf("a persistent dimension mismatch must not stall projection: %v", err)
	}
	if !cleared {
		t.Fatal("the batch must clear the vectors it just invalidated")
	}
	if !watermarkWritten {
		t.Fatal("a batch whose vector state IS consistent (cleared) may commit")
	}
}

// Codex round-3 F2, RED->GREEN: the vector signals are required methods and
// production supplies a real sink, so a mass clear is visible.
func TestR3_F2_VectorSignalsReachTelemetry(t *testing.T) {
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 8)}, telemetry)

	if err := adapter.embedProjectionBatch(context.Background(), "k", vectorProbeBatch("org")); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}
	if telemetry.projections != 1 || telemetry.embedded != 1 || telemetry.cleared != 0 {
		t.Fatalf("a healthy batch must report its embedded count: %#v", telemetry)
	}

	// Now an embed failure that clears -- the mass-clear signal.
	clearing := vectorAdapterWithTelemetry(t, fake,
		&stubEmbedder{vector: make([]float32, 8), err: errors.New("embedder down")}, telemetry)
	if err := clearing.embedProjectionBatch(context.Background(), "k", vectorProbeBatch("org")); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}
	if telemetry.cleared != 1 {
		t.Fatalf("a cleared vector must be counted so a mass clear is visible: %#v", telemetry)
	}
	if telemetry.degraded == 0 {
		t.Fatal("the degradation signal must reach telemetry")
	}
}

// The interface must not be satisfiable by accident: a type missing the vector
// methods is not a GraphTelemetry. This is what makes "unwired" a compile
// error rather than a silent no-op.
func TestR3_F2_VectorSignalsAreRequiredNotAnOptionalExtension(t *testing.T) {
	var _ GraphTelemetry = NoopTelemetry{}
	var _ GraphTelemetry = SlogTelemetry{}
	var _ GraphTelemetry = &recordingTelemetry{}
}

// Codex round-3 bootstrap gap, RED->GREEN: bootstrap waited for constraints
// but not for the VECTOR index, so the first batch after bootstrap could find
// it still building. F1 now fails such a batch rather than committing it, but
// bootstrap failing to wait would turn a condition it could simply have waited
// out into a self-inflicted first-batch failure.
func TestR3_BootstrapWaitsForVectorIndexReadiness(t *testing.T) {
	polls := 0
	fake := &fakeConn{}
	fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		polls++
		if polls < 3 {
			// Absent on the first look (so bootstrap creates it), then
			// building, then ready.
			if polls == 1 {
				return nil, nil
			}
			return []indexStatus{{
				Label: labelSubject, EntityType: "NODE", Status: "UNDER CONSTRUCTION",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
			}}, nil
		}
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	if err := adapter.ensureOrgGraph(context.Background(), "graphkey"); err != nil {
		t.Fatalf("bootstrap must wait out a building vector index, not fail: %v", err)
	}
	if polls < 3 {
		t.Fatalf("bootstrap must poll the vector index to readiness, polled %d times", polls)
	}
}

// A vector index that never becomes ready must time out loudly rather than
// letting bootstrap proceed against an index of unknown state.
func TestR3_BootstrapFailsWhenTheVectorIndexNeverBecomesReady(t *testing.T) {
	fake := &fakeConn{}
	fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	first := true
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		if first {
			first = false
			return nil, nil
		}
		return []indexStatus{{
			Label: labelSubject, EntityType: "NODE", Status: "UNDER CONSTRUCTION",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
		}}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)
	// Squeeze the deadline so the test does not wait out a real timeout.
	adapter.config.RequestTimeout = 50 * time.Millisecond

	if err := adapter.ensureOrgGraph(context.Background(), "graphkey"); !errors.Is(err, errVectorIndexNotReady) {
		t.Fatalf("bootstrap must fail loudly on an index that never settles, got %v", err)
	}
}

// Codex round-4 F1, RED->GREEN: a PRE-EXISTING index that never settles must
// fail bootstrap loudly within the timeout, not return success.
//
// The earlier form polled only after CREATING an absent index, so a
// pre-existing under-construction index cached bootstrap success. Round-3 F1
// containment then correctly failed every batch -- holding the organization's
// checkpoint INDEFINITELY while the loud, bounded timeout built for exactly
// this condition was never reached. A silent livelock where a loud timeout
// exists is worse than the bug it replaced.
func TestR4_F1_PreExistingNeverSettlingIndexFailsBootstrapLoudly(t *testing.T) {
	for _, status := range []string{"UNDER CONSTRUCTION", "", "SOMETHING-NEW"} {
		t.Run(status, func(t *testing.T) {
			created := false
			fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
				if strings.Contains(cypher, "CREATE VECTOR INDEX") {
					created = true
				}
				return nil, nil
			}}
			fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
				return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
			}
			// The index ALREADY EXISTS and never becomes readable.
			fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
				return []indexStatus{{
					Label: labelSubject, EntityType: "NODE", Status: status,
					Types:   map[string][]string{propEmbedding: {"VECTOR"}},
					Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
				}}, nil
			}
			adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)
			adapter.config.RequestTimeout = 50 * time.Millisecond

			err := adapter.ensureOrgGraph(context.Background(), "graphkey")
			if !errors.Is(err, errVectorIndexNotReady) {
				t.Fatalf("a never-settling PRE-EXISTING index must fail bootstrap loudly, got %v", err)
			}
			if created {
				t.Fatal("an existing index must never be re-created")
			}
			// Retryable: bootstrap success must not have been cached.
			adapter.bootstrapMu.RLock()
			cached := adapter.bootstrapDone["graphkey"]
			adapter.bootstrapMu.RUnlock()
			if cached {
				t.Fatal("a failed bootstrap must not cache success")
			}
		})
	}
}

// A pre-existing index that settles is waited out, not failed.
func TestR4_F1_PreExistingIndexThatSettlesIsWaitedOut(t *testing.T) {
	polls := 0
	fake := &fakeConn{}
	fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		polls++
		if polls < 3 {
			return []indexStatus{{
				Label: labelSubject, EntityType: "NODE", Status: "UNDER CONSTRUCTION",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
			}}, nil
		}
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)
	if err := adapter.ensureOrgGraph(context.Background(), "graphkey"); err != nil {
		t.Fatalf("a settling pre-existing index must be waited out: %v", err)
	}
}

// A DIMENSION MISMATCH must NOT block bootstrap: it is the persistent,
// operator-fixable state whose batches clear-and-commit, so blocking here
// would reintroduce the stall that exception exists to avoid.
func TestR4_F1_DimensionMismatchDoesNotBlockBootstrap(t *testing.T) {
	fake := &fakeConn{}
	fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)
	adapter.config.RequestTimeout = 50 * time.Millisecond
	if err := adapter.ensureOrgGraph(context.Background(), "graphkey"); err != nil {
		t.Fatalf("a dimension mismatch must not block bootstrap: %v", err)
	}
}

// Codex round-4 F2, RED->GREEN: a batch with nothing to embed must still
// report zero counts. The absence of a signal must mean "no batch ran", never
// "a batch ran and had nothing to embed".
func TestR4_F2_BatchWithNoEmbeddingTargetsStillReportsZeroCounts(t *testing.T) {
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 8)}, telemetry)

	// A relationship-only batch: valid, and produces no embedding targets.
	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{
		OrgID: "org",
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_00000001", Type: "BLOCKS",
			From:       contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "A"},
			To:         contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p2", Label: "B"},
			ObservedAt: observed, SourceVersion: "v1",
		}},
	}
	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}
	if telemetry.projections != 1 {
		t.Fatalf("a batch with nothing to embed must still report, got %d records", telemetry.projections)
	}
	if telemetry.embedded != 0 || telemetry.cleared != 0 {
		t.Fatalf("the record must be zero-count, got embedded=%d cleared=%d", telemetry.embedded, telemetry.cleared)
	}
}

// historicalFilter is an active window, built the way production builds one
// rather than by hand-setting fields.
func historicalFilter(t *testing.T) temporalFilter {
	t.Helper()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	filter := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf})
	if !filter.active {
		t.Fatal("the fixture filter is inactive, so every historical case below would silently test the current axis")
	}
	return filter
}

// TestCHAOS3781_HistoricalAxisSkipsVectorRetrievalAndReportsIt is the
// CHAOS-3781 x CHAOS-3778 integration, under the orchestrator's ruling (a).
//
// The vector index has no notion of a validity window, and a temporal
// predicate cannot be bolted on: db.idx.vector.queryNodes returns the top-k
// by distance and the org predicate is a POST-FILTER over that k. A window
// applied the same way eliminates most of the k, which produces under-recall
// that reads as absence, and it breaks the truncation argument, which holds
// only because results are distance-ordered.
//
// So a historical axis skips the vector step and SAYS so. Silence would be
// the failure: an answer that quietly considered fewer candidates is the
// shape this whole branch exists to prevent.
func TestCHAOS3781_HistoricalAxisSkipsVectorRetrievalAndReportsIt(t *testing.T) {
	var vectorQueried bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.vector.queryNodes") {
			vectorQueried = true
		}
		return nil, nil
	}}
	// An OPERATIONAL fence and a matching embedder: the vector step is
	// fully available here, so the axis is the only reason it is skipped.
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	_, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, historicalFilter(t))
	if err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if vectorQueried {
		t.Fatal("a vector query was issued on a historical axis; the index cannot honour a validity window, so its candidates may not have existed at the as-of time")
	}
	if !degraded {
		t.Fatal("the skip was not reported as degraded; a mechanism WAS expected here and was unavailable, and an answer that silently considered fewer candidates is exactly the failure this branch exists to prevent")
	}
}

// TestCHAOS3781_NilEmbedderStaysUndegradedOnAHistoricalAxis proves the
// COMPOSITION, rather than asserting it in a comment.
//
// The historical skip sits after 3778's embedder-nil branch, so the two
// rules compose by ORDER: with no embedder configured nothing was expected,
// which is not a degradation, and 3778's rule must survive untouched even
// on a historical axis. Placement is the whole argument, so it gets a test
// that fails alone if the branches are ever reordered.
func TestCHAOS3781_NilEmbedderStaysUndegradedOnAHistoricalAxis(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)

	if _, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, historicalFilter(t)); err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	} else if degraded {
		t.Fatal("a historical axis with NO embedder configured reported degraded; nothing was expected here, so nothing was lost -- CHAOS-3778's rule must dominate, which it does only while the nil check precedes the historical skip")
	}
}

// TestCHAOS3781_CurrentAxisRetrievalIsUnchanged pins ZERO behaviour delta
// for every question that is not historical -- the overwhelming majority.
func TestCHAOS3781_CurrentAxisRetrievalIsUnchanged(t *testing.T) {
	var vectorQueried bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.vector.queryNodes") {
			vectorQueried = true
		}
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	_, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, temporalFilter{})
	if err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if !vectorQueried {
		t.Fatal("no vector query was issued on the CURRENT axis; the historical skip has leaked into ordinary questions")
	}
	if degraded {
		t.Fatal("a current-axis question reported degraded; nothing about it changed")
	}
}

// TestCHAOS3781_AnOutOfWindowSubjectReachableOnlyByVectorIsNotAdmitted is
// the case NEITHER lane could write alone.
//
// CHAOS-3778's tests run with no time axis; CHAOS-3781's exercise the
// lexical path. A subject that is outside the validity window and matches
// only semantically sits precisely in the gap: the lexical path excludes it
// correctly, and before this integration the vector path admitted it.
func TestCHAOS3781_AnOutOfWindowSubjectReachableOnlyByVectorIsNotAdmitted(t *testing.T) {
	var issued []string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		issued = append(issued, cypher)
		// The lexical query is temporally bounded, so it returns nothing:
		// the subject did not exist at the as-of time. Only the vector
		// index would surface it, because it cannot filter by window.
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(8)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: make([]float32, 8)}, 0.55)

	candidates, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "checkout service", 5, &resolutionFence{}, historicalFilter(t))
	if err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates surfaced on a historical axis: %+v -- an out-of-window subject reachable only by vector must not be admitted", candidates)
	}
	for _, cypher := range issued {
		if strings.Contains(cypher, "db.idx.vector.queryNodes") {
			t.Fatal("the vector index was consulted, so an out-of-window subject could have entered the candidate set through a path that cannot see the window")
		}
	}
	// The lexical query MUST still have run and MUST carry the window --
	// otherwise this test would pass on a code path that queried nothing.
	if len(issued) == 0 {
		t.Fatal("no query was issued at all, so this proves nothing about which path admitted what")
	}
	if !degraded {
		t.Fatal("the answer did not record that a retrieval mechanism was unavailable")
	}
}

// TestCHAOS3781_HistoricalSuppressionIsVisibleToOperators extends the
// composition matrix by one column: the TELEMETRY column.
//
// Round-16 finding: the historical skip set the answer-level degraded flag
// and emitted nothing, so a historical question with a configured embedder
// produced a degraded answer with zero operational signal -- indistinguishable
// from healthy retrieval, and from an outage, at exactly the moment an
// operator would be trying to tell those apart.
//
// The invariant restored here is that answer-level and telemetry-level
// degradation fire TOGETHER, and that suppression is distinguishable from
// failure: the suppressed counter moves, the degraded (outage) counter does
// not.
func TestCHAOS3781_HistoricalSuppressionIsVisibleToOperators(t *testing.T) {
	for _, test := range []struct {
		name           string
		temporal       func(*testing.T) temporalFilter
		embedder       contextfabric.Embedder
		wantSuppressed int
		wantDegraded   int
		wantAnswerFlag bool
	}{
		{
			name:           "historical with an embedder: suppressed, not degraded",
			temporal:       historicalFilter,
			embedder:       &stubEmbedder{vector: make([]float32, 8)},
			wantSuppressed: 1,
			wantDegraded:   0,
			wantAnswerFlag: true,
		},
		{
			name:           "current axis: neither signal",
			temporal:       func(*testing.T) temporalFilter { return temporalFilter{} },
			embedder:       &stubEmbedder{vector: make([]float32, 8)},
			wantSuppressed: 0,
			wantDegraded:   0,
			wantAnswerFlag: false,
		},
		{
			// No embedder means nothing was expected, so nothing was lost
			// and nothing is reported -- CHAOS-3778's rule, which the skip
			// must not disturb even on a historical axis.
			name:           "historical with no embedder: neither signal",
			temporal:       historicalFilter,
			embedder:       nil,
			wantSuppressed: 0,
			wantDegraded:   0,
			wantAnswerFlag: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
				return nil, nil
			}}
			fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
				return []indexStatus{operationalVectorIndex(8)}, nil
			}
			telemetry := &recordingTelemetry{}
			// attachEmbedder ignores a nil Embedder, so the nil case leaves
			// the adapter with no embedder -- which is the state under test.
			adapter := vectorAdapterWithTelemetry(t, fake, test.embedder, telemetry)

			_, _, degraded, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, test.temporal(t))
			if err != nil {
				t.Fatalf("hybridSearchNodes: %v", err)
			}
			if degraded != test.wantAnswerFlag {
				t.Fatalf("answer-level degraded = %v, want %v", degraded, test.wantAnswerFlag)
			}
			if telemetry.suppressed != test.wantSuppressed {
				t.Errorf("suppression signal fired %d times, want %d -- an operator cannot separate intentional historical suppression from healthy retrieval without it", telemetry.suppressed, test.wantSuppressed)
			}
			if telemetry.degraded != test.wantDegraded {
				t.Errorf("OUTAGE signal fired %d times, want %d -- suppression must not look like a broken embedder", telemetry.degraded, test.wantDegraded)
			}
		})
	}
}
