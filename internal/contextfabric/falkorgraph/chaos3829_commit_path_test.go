package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestVectorSearchNodesSetsVectorSimilarityUnclampedWhenNotTruncated pins
// CHAOS-3829's new field alongside the pre-existing Relevance/Score pinning
// (TestVectorCandidateDeclaresRelevanceAndNeverCarriesTheRawDistance): an
// untruncated candidate's VectorSimilarity carries the SAME true-cosine
// value CosineFromDistance computed, distinct from -- but consistent with --
// the banded Relevance.
func TestVectorSearchNodesSetsVectorSimilarityUnclampedWhenNotTruncated(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		// distance 0.2 -> similarity 0.90.
		return []row{{
			"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "Auth"}},
			"score": 0.2,
		}}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	candidates, truncated, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0, 0, 0}, 0.55, 5)
	if err != nil {
		t.Fatalf("vectorSearchNodes: %v", err)
	}
	if truncated {
		t.Fatal("a single above-floor row under a budget of 5 must not truncate")
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	candidate := candidates[0]
	wantSimilarity := embedprovider.CosineFromDistance(0.2)
	if candidate.VectorSimilarity == nil {
		t.Fatal("a vector candidate must declare VectorSimilarity")
	}
	if *candidate.VectorSimilarity != wantSimilarity {
		t.Fatalf("VectorSimilarity = %v, want %v (CosineFromDistance(0.2))", *candidate.VectorSimilarity, wantSimilarity)
	}
}

// TestVectorSearchNodesSetsVectorSimilarityUnclampedEvenWhenTruncated is the
// core CHAOS-3829 pinning test: unlike Relevance (correctly floor-clamped
// for EVERY survivor once the call truncates -- see
// TestF1_TruncationIsStillReportedWhenAboveFloorRowsExceedTheLimit),
// VectorSimilarity must carry each candidate's OWN real similarity
// regardless -- the whole reason CandidateNode.VectorSimilarity exists
// separately from Relevance is that a margin computed from the clamped
// value would read as zero on almost every real (truncated) call.
func TestVectorSearchNodesSetsVectorSimilarityUnclampedEvenWhenTruncated(t *testing.T) {
	const limit = 2
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		rows := make([]row, 0, limit+1)
		// Two DISTINCT similarities among the survivors, so a truncated
		// batch's Relevance clamp (uniform floor) is visibly different from
		// VectorSimilarity (still distinct per row).
		distances := []float64{0.0, 0.3, 0.31} // similarities: 1.0, 0.7, 0.69
		for i, d := range distances {
			rows = append(rows, row{
				"node": &node{Properties: map[string]interface{}{
					propKind: "project", propCanonicalID: fmt.Sprintf("p%d", i), propLabel: "Auth",
				}},
				"score": d,
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
		t.Fatal("3 above-floor rows under a budget of 2 must truncate")
	}
	if len(candidates) != limit {
		t.Fatalf("expected %d candidates, got %d", limit, len(candidates))
	}
	seen := map[float64]bool{}
	for i, candidate := range candidates {
		if candidate.Relevance.Float() != vectorRelevanceFloor {
			t.Fatalf("candidate %d: Relevance = %v, want the floor-clamp %v under truncation", i, candidate.Relevance.Float(), vectorRelevanceFloor)
		}
		if candidate.VectorSimilarity == nil {
			t.Fatalf("candidate %d: VectorSimilarity must still be set under truncation", i)
		}
		if *candidate.VectorSimilarity <= vectorRelevanceFloor {
			// Sanity: both survivors here are well above the floor
			// (similarities ~1.0 and ~0.7 against tau=0.55) -- this is not
			// asserting a specific value, only that it was NOT collapsed to
			// the same clamp Relevance uses.
			t.Fatalf("candidate %d: VectorSimilarity = %v looks clamped, want the real similarity", i, *candidate.VectorSimilarity)
		}
		seen[*candidate.VectorSimilarity] = true
	}
	if len(seen) != 2 {
		t.Fatalf("VectorSimilarity values = %v, want 2 DISTINCT values (Relevance is uniformly floor-clamped here, but VectorSimilarity must not be)", seen)
	}
}

// TestEmbedderFromEnv_CalibratedIdentityAppliesVectorMarginCommitThreshold
// wires the calibrated M end to end through EmbedderFromEnv, mirroring
// TestEmbedderFromEnv_CalibratedIdentityOverridesDefaults's pattern for
// OverFetchMultiplier/EfRuntime.
func TestEmbedderFromEnv_CalibratedIdentityAppliesVectorMarginCommitThreshold(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(nil))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.Embedder == nil {
		t.Fatal("expected a configured embedder")
	}
	if options.VectorMarginCommitThreshold <= 0 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want the calibrated positive M for the pinned identity", options.VectorMarginCommitThreshold)
	}
}

// TestEmbedderFromEnv_ExplicitSimilarityFloorOverrideDisablesM is CHAOS-3829
// codex r2 G3's core pinning test: an operator-supplied
// ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR below the policy's calibrated
// tau admits an UNMEASURED population -- M must stay at its zero
// ("uncalibrated"/disabled) value in that case, even though the identity
// otherwise matches the calibrated table entry exactly.
func TestEmbedderFromEnv_ExplicitSimilarityFloorOverrideDisablesM(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvSimilarityFloor: "0.10", // below the policy's calibrated 0.30
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.SimilarityFloor != 0.10 {
		t.Fatalf("SimilarityFloor = %v, want the explicit override 0.10 to be honored (codex round-1 P1, unrelated to this fix)", options.SimilarityFloor)
	}
	if options.VectorMarginCommitThreshold != 0 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want 0 -- M was calibrated at tau=0.30, not the overridden 0.10, and must not install against an unmeasured floor", options.VectorMarginCommitThreshold)
	}
}

// TestEmbedderFromEnv_NoFloorOverrideStillAppliesM is the positive control:
// with NO explicit floor override, the effective floor equals the policy's
// calibrated tau, and M installs exactly as before this fix.
func TestEmbedderFromEnv_NoFloorOverrideStillAppliesM(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(nil))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.VectorMarginCommitThreshold <= 0 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want the calibrated positive M when the floor is NOT overridden", options.VectorMarginCommitThreshold)
	}
}

// TestEmbedderFromEnv_BlankSimilarityFloorOverrideStillAppliesM proves a
// BLANK env var does not count as an override (mirrors envFloat's own
// "set AND non-blank" definition of explicit, codex round-1 P1) -- M must
// still install.
func TestEmbedderFromEnv_BlankSimilarityFloorOverrideStillAppliesM(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvSimilarityFloor: "",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.VectorMarginCommitThreshold <= 0 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want the calibrated positive M -- a blank override is not an explicit one", options.VectorMarginCommitThreshold)
	}
}

// TestAttachEmbedder_CapturesVectorMarginCommitThreshold proves the adapter
// field wiring: attachEmbedder captures EmbedderOptions.VectorMarginCommitThreshold
// the same way it captures OverFetchMultiplier/EfRuntime.
func TestAttachEmbedder_CapturesVectorMarginCommitThreshold(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.attachEmbedder(EmbedderOptions{
		Embedder: &stubEmbedder{vector: []float32{1, 0, 0}}, SimilarityFloor: 0.5,
		VectorMarginCommitThreshold: 0.042,
	})
	if adapter.vectorMarginCommitThreshold != 0.042 {
		t.Fatalf("adapter.vectorMarginCommitThreshold = %v, want 0.042", adapter.vectorMarginCommitThreshold)
	}
}

// TestAttachEmbedder_ZeroVectorMarginCommitThresholdLeavesCarveOutDisabled
// is the default/uncalibrated case: an EmbedderOptions with no explicit
// VectorMarginCommitThreshold leaves the adapter field at its zero value,
// which graphrank's carve-out treats as "disabled".
func TestAttachEmbedder_ZeroVectorMarginCommitThresholdLeavesCarveOutDisabled(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: []float32{1, 0, 0}}, SimilarityFloor: 0.5})
	if adapter.vectorMarginCommitThreshold != 0 {
		t.Fatalf("adapter.vectorMarginCommitThreshold = %v, want 0 (uncalibrated default)", adapter.vectorMarginCommitThreshold)
	}
}

// TestResolveSubjects_CommitPathCarveOutFiresThroughTheRealAdapterWiring is
// the CHAOS-3829 end-to-end proof, through reader.go's ResolveSubjects
// exactly as production wires it (not a hand-rolled shortcut): an ambiguous
// two-candidate resolution (one corroborated, one not -- neither clears the
// 0.88 top-of-two/0.72 lone gates) now COMMITS the higher-vector-similarity
// candidate once the adapter carries a calibrated
// vectorMarginCommitThreshold, proving vector.go's VectorSimilarity
// population, graphrank's side-map construction (mergeSearchResults), and
// the carve-out itself (vectorMarginCommit) are correctly wired together
// end to end.
func TestResolveSubjects_CommitPathCarveOutFiresThroughTheRealAdapterWiring(t *testing.T) {
	// Two subjects, both above tau (0.55). Neither's LABEL is the literal
	// search term "auth" (deliberately -- an exact-label match would
	// promote it to Confidence=1/MatchExact and win via CHAOS-3810's own
	// carve-out before this one ever gets a turn, which is not what this
	// test is proving). "authsvc" is the DECISIVELY higher vector-similarity
	// one and is ALSO found lexically for the same term, so it alone is
	// corroborated (MatchLexical + MatchVector); "identitysvc" is vector-only
	// (uncorroborated, capped at vectorRelevanceCeiling=0.70 < the 0.72 lone
	// gate), present purely so a SECOND vector-arm candidate exists for the
	// carve-out's own >=2-candidates precondition and so the ordinary
	// top-of-two gate has two commit-eligible candidates to fail on.
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return []row{
				// distance 0.05 -> similarity 0.95 (decisive top-1).
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service"}}, "score": 0.05},
				// distance 0.30 -> similarity 0.70: above tau (0.55), so it
				// survives the floor and gives the carve-out (and the
				// ordinary top-of-two gate) a SECOND commit-eligible
				// candidate to consider, well below authsvc's similarity.
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service"}}, "score": 0.30},
			}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth"}}, "score": 1.0},
			}, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	adapter.vectorMarginCommitThreshold = 0.10 // calibrated M

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "authsvc" {
		t.Fatalf("resolution.Committed = %v, want exactly [authsvc] (the decisively higher-vector-similarity, corroborated subject)", resolution.Committed)
	}
}

// TestResolveSubjects_CommitPathCarveOutStaysDisabledWithoutCalibration is
// the same fixture as the test above, WITHOUT a calibrated
// vectorMarginCommitThreshold -- proving the wiring change alone (adapter
// field defaulting to 0) is sufficient to keep every uncalibrated
// deployment byte-identical to pre-CHAOS-3829 behavior: still ambiguous.
func TestResolveSubjects_CommitPathCarveOutStaysDisabledWithoutCalibration(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service"}}, "score": 0.05},
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service"}}, "score": 0.30},
			}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth"}}, "score": 1.0},
			}, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	// vectorMarginCommitThreshold left at its zero value (uncalibrated).

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- uncalibrated M must never enable the carve-out", resolution.Committed)
	}
}

// TestHybridSearchNodes_StampsMatchLexicalOnFulltextResults is the codex r1
// F4 pinning test graphrank's vectorArmCorroborated doc comment cites: every
// fulltextSearchNodesForResolution result hybridSearchNodes returns is
// stamped contextfabric.MatchLexical (vector.go, immediately after that
// call) -- the EXACT mechanism value the commit-path carve-out's narrowed
// corroboration check requires alongside MatchVector. If this ever changes
// (a different mechanism, or the stamp moves/is removed), the carve-out's
// F4 doc comment -- and its safety property -- silently stops matching
// what production actually does, so this must fail loudly first.
func TestHybridSearchNodes_StampsMatchLexicalOnFulltextResults(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.fulltext.queryNodes") {
			return []row{{
				"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "Auth", propSearchText: "auth"}},
				"score": 1.0,
			}}, nil
		}
		return nil, nil
	}}
	// No embedder configured -- isolates the lexical arm's own stamping,
	// independent of the vector arm.
	adapter := newFakeAdapter(t, fake)
	candidates, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, temporalFilter{})
	if err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected exactly one lexical candidate, got %#v", candidates)
	}
	if candidates[0].Mechanism != contextfabric.MatchLexical {
		t.Fatalf("candidates[0].Mechanism = %q, want MatchLexical -- the exact mechanism graphrank's vectorArmCorroborated (F4) requires alongside MatchVector", candidates[0].Mechanism)
	}
}
