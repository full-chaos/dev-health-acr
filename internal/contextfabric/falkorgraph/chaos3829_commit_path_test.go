package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
	// codex r5 K1 (accepted): CalibratedTopK is gated in lockstep with M
	// (same conditional in EmbedderFromEnv) -- it must install here too.
	if options.CalibratedTopK != 20 {
		t.Fatalf("CalibratedTopK = %v, want the calibrated TopK (20) for the pinned identity", options.CalibratedTopK)
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
	// codex r5 K1: CalibratedTopK shares M's own gate and must drop with it.
	if options.CalibratedTopK != 0 {
		t.Fatalf("CalibratedTopK = %v, want 0 -- gated alongside M, which is disabled here", options.CalibratedTopK)
	}
}

// TestEmbedderFromEnv_ExplicitFloorEqualToCalibratedTauKeepsM is CHAOS-3829
// codex r3 H1(a)'s core pinning test: an explicit
// ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR set to EXACTLY the calibrated
// tau (0.30) must NOT disable M -- the effective floor is unchanged, so M
// still measures the population it was calibrated against. The ORIGINAL
// presence-based G3 fix would have wrongly disabled M here; this test
// distinguishes the value-based fix from that.
func TestEmbedderFromEnv_ExplicitFloorEqualToCalibratedTauKeepsM(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvSimilarityFloor: "0.30", // exactly the calibrated tau for this identity
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.SimilarityFloor != 0.30 {
		t.Fatalf("SimilarityFloor = %v, want 0.30", options.SimilarityFloor)
	}
	if options.VectorMarginCommitThreshold <= 0 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want the calibrated positive M -- the explicit override equals the calibrated tau, so the effective floor is unchanged", options.VectorMarginCommitThreshold)
	}
	if options.CalibratedTopK != 20 {
		t.Fatalf("CalibratedTopK = %v, want 20 -- gated alongside M, which is enabled here", options.CalibratedTopK)
	}
}

// TestEmbedderFromEnv_ExplicitFloorDifferentFromCalibratedTauDropsM is the
// contrasting negative control, at a DIFFERENT explicit value than the
// disables-M test above (which uses 0.10) -- both must drop M, proving the
// value comparison, not merely presence, is what is being tested.
func TestEmbedderFromEnv_ExplicitFloorDifferentFromCalibratedTauDropsM(t *testing.T) {
	options, err := EmbedderFromEnv(fakeEmbedderEnv(map[string]string{
		embedprovider.EnvSimilarityFloor: "0.45",
	}))
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.SimilarityFloor != 0.45 {
		t.Fatalf("SimilarityFloor = %v, want the explicit override 0.45", options.SimilarityFloor)
	}
	if options.VectorMarginCommitThreshold != 0 {
		t.Fatalf("VectorMarginCommitThreshold = %v, want 0 -- 0.45 != the calibrated tau 0.30", options.VectorMarginCommitThreshold)
	}
	if options.CalibratedTopK != 0 {
		t.Fatalf("CalibratedTopK = %v, want 0 -- gated alongside M, which is disabled here", options.CalibratedTopK)
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
		CalibratedTopK:              15,
	})
	if adapter.vectorMarginCommitThreshold != 0.042 {
		t.Fatalf("adapter.vectorMarginCommitThreshold = %v, want 0.042", adapter.vectorMarginCommitThreshold)
	}
	if adapter.calibratedTopK != 15 {
		t.Fatalf("adapter.calibratedTopK = %v, want 15", adapter.calibratedTopK)
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
	if adapter.calibratedTopK != 0 {
		t.Fatalf("adapter.calibratedTopK = %v, want 0 (uncalibrated default)", adapter.calibratedTopK)
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
	//
	// codex r8 O1: both nodes carry an authorization_repositories attribute
	// admitting "acme/repo-x" -- required now that the principal below uses
	// the REAL production wildcard shape (["*"], non-empty), which routes
	// through AuthorizedAttributes' scope loop instead of skipping it (an
	// empty scope list, this test's PRE-O1 principal, bypassed that loop
	// entirely) -- a node with NO authorization attribute at all is denied
	// unconditionally regardless of scope value (scopeContainsAttr's own
	// "key absent -> deny" convention), so this attribute is required for
	// the candidates to form at all, independent of the M1 guard this test
	// is proving reachable.
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return []row{
				// distance 0.05 -> similarity 0.95 (decisive top-1).
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.05},
				// distance 0.30 -> similarity 0.70: above tau (0.55), so it
				// survives the floor and gives the carve-out (and the
				// ordinary top-of-two gate) a SECOND commit-eligible
				// candidate to consider, well below authsvc's similarity.
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.30},
			}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 1.0},
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
	adapter.calibratedTopK = 20                // codex r5 K1 -- required alongside M (newFakeAdapter's MaxResults=25, request max=10, both within [2,20])

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "authsvc" {
		t.Fatalf("resolution.Committed = %v, want exactly [authsvc] (the decisively higher-vector-similarity, corroborated subject)", resolution.Committed)
	}
}

// TestResolveSubjects_CommitPathCarveOutStaysDisabledForScopedPrincipal is
// codex r7 M1's end-to-end pinning test: the SAME fixture as the wiring
// proof above, both nodes now carrying an authorization_repositories
// attribute that ADMITS the scoped principal below (so both candidates
// still form normally -- this test isolates the RESCUE guard, not ordinary
// authorization filtering) -- but principal.RepositoryScopes is non-empty.
// The resolution must stay ambiguous: a scoped principal must never be able
// to observe, via commit-vs-clarification, whether a hidden closer
// competitor exists outside their scope (the scope-existence-oracle
// hazard M1's doc comment describes).
func TestResolveSubjects_CommitPathCarveOutStaysDisabledForScopedPrincipal(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.05},
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.30},
			}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 1.0},
			}, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	adapter.vectorMarginCommitThreshold = 0.10
	adapter.calibratedTopK = 20

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth"}}
	// principal.RepositoryScopes is non-empty (but AUTHORIZES both nodes
	// above, via their matching authorization_repositories attribute) --
	// candidates form exactly as in the unscoped test, so any refusal here
	// is specifically the M1 guard, not authorization filtering.
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/repo-x"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), principal, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- a scoped principal must never reach the rescue, even when authorized for every candidate involved", resolution.Committed)
	}
}

// TestResolveSubjects_CommitPathCarveOutFiresForWildcardScopedPrincipal is
// codex r8 O1's core pinning test (CRITICAL, production reachability): a
// principal scoped to the GLOBAL wildcard ["*"] -- the REAL shape a
// production org-wide credential is issued with (auth.NormalizeRepositoryScopes
// requires at least one scope; a real org-wide grant is ["*"], never an
// empty list, which no authenticated credential can ever present) -- must
// still reach the rescue. Proves scopesUnrestricted's wildcard recognition,
// not merely the now-dead empty-list case every PRIOR "unscoped" test in
// this file relied on (all switched to this same ["*"] shape by this
// commit).
func TestResolveSubjects_CommitPathCarveOutFiresForWildcardScopedPrincipal(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.05},
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.30},
			}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 1.0},
			}, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	adapter.vectorMarginCommitThreshold = 0.10
	adapter.calibratedTopK = 20

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth"}}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), principal, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "authsvc" {
		t.Fatalf("resolution.Committed = %v, want exactly [authsvc] -- a global-wildcard-scoped principal is authorization-equivalent to unscoped and must reach the rescue", resolution.Committed)
	}
}

// TestResolveSubjects_CommitPathCarveOutStaysDisabledForOwnerWildcardPrincipal
// proves the boundary O1's ruling drew explicitly: an OWNER-scoped partial
// wildcard ("acme/*") does NOT qualify as unrestricted -- ScopeMatch
// resolves that against one SPECIFIC owner (scope.go), so a node under a
// DIFFERENT owner stays hidden from this principal, and the M1
// existence-oracle hazard still applies. Both fixture nodes here are
// authorized under "acme/repo-x" (owner "acme"), which "acme/*" DOES admit
// -- isolating the refusal to the M1 guard specifically, exactly like the
// non-wildcard scoped-principal test above.
func TestResolveSubjects_CommitPathCarveOutStaysDisabledForOwnerWildcardPrincipal(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.05},
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.30},
			}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 1.0},
			}, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	adapter.vectorMarginCommitThreshold = 0.10
	adapter.calibratedTopK = 20

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth"}}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/*"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), principal, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- an owner-scoped partial wildcard must never reach the rescue, even though it authorizes every candidate involved", resolution.Committed)
	}
}

// TestResolveSubjects_CommitPathCarveOutStaysDisabledForRequestNarrowedScope
// is M1's second pinning test: the principal itself is UNSCOPED, but the
// REQUEST narrows visibility (RequestedScope.ProjectIDs) -- the other half
// of unscopedVisibility's four independent checks (authorize.go's
// AuthorizedAttributes reads all four the same way). Both nodes carry an
// authorization_projects attribute admitting the requested project, so
// candidates form normally; the resolution must still stay ambiguous.
func TestResolveSubjects_CommitPathCarveOutStaysDisabledForRequestNarrowedScope(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propAuthzProjects: []string{"proj-1"}}}, "score": 0.05},
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service", propAuthzProjects: []string{"proj-1"}}}, "score": 0.30},
			}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth", propAuthzProjects: []string{"proj-1"}}}, "score": 1.0},
			}, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	adapter.vectorMarginCommitThreshold = 0.10
	adapter.calibratedTopK = 20

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
		RequestedScope: contextfabric.RequestedScope{
			ProjectIDs: []string{"proj-1"}, // narrows visibility; principal itself is unscoped
		},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- a request-narrowed scope must never reach the rescue, even when authorized for every candidate involved", resolution.Committed)
	}
}

// TestResolveSubjects_CommitPathCarveOutStaysDisabledWhenMaxResultsCapNarrowsBelowTwo
// is codex r5 K2's end-to-end pinning test, REBUILT per codex r6 L4 (accepted):
// the original single-term version was VACUOUS -- vectorSearchNodesWithOverFetch's
// own `survivors[:returnCap]` slice (vector.go) already trims a SINGLE term's
// raw ANN rows down to returnCap=multiplier*limit=1*1=1 BEFORE
// mergeSearchResults ever sees a second row, so that fixture had NO
// competitor in vectorArmSimilarity REGARDLESS of whether the MaxResultsCap
// wiring under test was even present -- it was proving "no competitor",
// not "effective-limit guard fired".
//
// Fixed here with TWO interpreted subject terms, each its OWN Search call
// (and therefore its OWN independent returnCap=1 slice) contributing a
// DIFFERENT top-1 subject -- mergeSearchResults' cross-term side-map (keyed
// by subject, MAX similarity across every term, see its own doc comment)
// therefore ends up with the SAME >=2 DISTINCT entries a real cap=1
// deployment queried with >=2 terms would produce, giving vectorMarginCommit
// a genuine TOP+COMPETITOR pair to work with. The ONLY thing left to block
// the commit is effectiveSearchLimit(1) < 2 -- exactly the K2 guard this
// test exists to pin, isolated from K2's own F1 lower-bound sibling this
// time by construction, not by accident.
func TestResolveSubjects_CommitPathCarveOutStaysDisabledWhenMaxResultsCapNarrowsBelowTwo(t *testing.T) {
	vectorCalls, lexicalCalls := 0, 0
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			vectorCalls++
			switch vectorCalls {
			case 1:
				// term "auth": distance 0.05 -> similarity 0.95 (decisive top-1).
				return []row{
					{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.05},
				}, nil
			case 2:
				// term "identity": distance 0.30 -> similarity 0.70, above
				// tau (0.55) -- the cross-term competitor, from a DIFFERENT
				// Search call's own (independently returnCap=1-sliced)
				// result.
				return []row{
					{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.30},
				}, nil
			default:
				// A 3rd+ call is CHAOS-3838's question-level SearchQuestion
				// pass (reader.go wires it unconditionally; this fixture's
				// non-empty Question triggers it) -- codex r1 F3 passes nil
				// for vectorArmSimilarity on that call specifically, so
				// whatever it returns never touches the side-map this test
				// is exercising. Returning nothing keeps the fixture
				// minimal.
				return nil, nil
			}
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			lexicalCalls++
			if lexicalCalls == 1 {
				// term "auth": lexical also finds authsvc -- the ONLY
				// corroborated (Vector+Lexical) candidate, so it alone can
				// ever be TOP under vectorArmCorroborated's own narrowed
				// pairing (F4).
				return []row{
					{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 1.0},
				}, nil
			}
			// term "identity": no lexical hit -- identitysvc stays
			// vector-only (uncorroborated), present purely as the
			// cross-term competitor and the ordinary top-of-two gate's
			// second commit-eligible candidate.
			return nil, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second, MaxAttempts: 1,
		MaxResults: 1, // codex r5 K2's own scenario: cap=1 with a request max of 10
		PoolSize:   1, AllowInsecure: true,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: []float32{1, 0, 0, 0}}, SimilarityFloor: 0.55})
	adapter.vectorMarginCommitThreshold = 0.10 // calibrated M
	adapter.calibratedTopK = 20                // calibrated TopK -- both otherwise satisfied

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth", "identity"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if vectorCalls < 2 {
		t.Fatalf("vectorCalls = %d, want >=2 -- this test requires two INDEPENDENT Search calls to produce the cross-term side-map, not a single call returning two rows", vectorCalls)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- a config MaxResults cap of 1 must narrow the effective search limit below the carve-out's floor of 2, even though request.Options.MaxSubjectCandidates (10) alone would not, and even with a genuine cross-term competitor available", resolution.Committed)
	}
}

// TestResolveSubjects_CommitPathCarveOutFiresWithTwoTermsAtCapTwo is the
// positive control for the rebuilt L4 test above: the IDENTICAL two-term
// fixture, but MaxResults=2 (not 1) -- effectiveSearchLimit becomes 2,
// clearing the carve-out's lower bound, so the SAME cross-term competitor
// that blocked the commit above now lets it fire. Proves the L4 rebuild
// exercises a REAL, bidirectional guard (the cross-term side-map, TOP,
// and COMPETITOR are unchanged between this test and the one above -- only
// the cap differs), not a fixture that always refuses regardless of what
// MaxResultsCap wiring is present.
func TestResolveSubjects_CommitPathCarveOutFiresWithTwoTermsAtCapTwo(t *testing.T) {
	vectorCalls, lexicalCalls := 0, 0
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			vectorCalls++
			switch vectorCalls {
			case 1:
				return []row{
					{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.05},
				}, nil
			case 2:
				return []row{
					{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.30},
				}, nil
			default:
				// A 3rd+ call is CHAOS-3838's question-level SearchQuestion
				// pass (F3 excludes it from the side-map) -- see the
				// disabled-fixture test's identical comment.
				return nil, nil
			}
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			lexicalCalls++
			if lexicalCalls == 1 {
				return []row{
					{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 1.0},
				}, nil
			}
			return nil, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second, MaxAttempts: 1,
		MaxResults: 2, // the ONLY difference from the test above
		PoolSize:   1, AllowInsecure: true,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: []float32{1, 0, 0, 0}}, SimilarityFloor: 0.55})
	adapter.vectorMarginCommitThreshold = 0.10
	adapter.calibratedTopK = 20

	request := contextfabric.InvestigationRequest{
		Question: "who owns auth",
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
	}
	interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{"auth", "identity"}}
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "authsvc" {
		t.Fatalf("resolution.Committed = %v, want exactly [authsvc] -- MaxResults=2 clears the effective-limit floor for the SAME cross-term fixture", resolution.Committed)
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
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.05},
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "identitysvc", propLabel: "Identity Service", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 0.30},
			}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "authsvc", propLabel: "Auth Service", propSearchText: "auth", propAuthzRepos: []string{"acme/repo-x"}}}, "score": 1.0},
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
	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, interpreted)
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
