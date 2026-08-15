package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file holds the CHAOS-3838 (embed-text spec v2 §5 L11/L13, §6 T8)
// tests: read-side union vector search over the question (L11) and
// closed-vocabulary lexicon expansion feeding both retrieval arms (L13).

// TestFulltextSearchNodes_LexiconWidensTheQueryWithoutChangingConfidence is
// the L13 lexical-arm proof: a candidate whose OWN search_text carries only
// the synonym phrase ("pull request"), never the caller's literal term
// ("PR"), must still be FOUND -- but at the fulltext relevance FLOOR, not a
// score inflated by the expansion, because matched-term coverage is keyed
// to the ORIGINAL term only (queries.go's fulltextSearchNodes doc comment).
func TestFulltextSearchNodes_LexiconWidensTheQueryWithoutChangingConfidence(t *testing.T) {
	synonymOnlyHit := fulltextRow("pull_request", "pr_42", "Fix login bug", "Fix login bug pull request", nil)
	var capturedQuery string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if q, ok := params["query"].(string); ok {
			capturedQuery = q
		}
		return []row{synonymOnlyHit}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	candidates, truncated, err := adapter.fulltextSearchNodes(context.Background(), "test-key", "org-1", "PR", 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if truncated {
		t.Fatal("a single row well under the limit must not read as truncated")
	}
	if !strings.Contains(strings.ToLower(capturedQuery), "pull") {
		t.Fatalf("query %q sent to FalkorDB, want the lexicon-widened OR-list to include \"pull\"", capturedQuery)
	}
	if len(candidates) != 1 {
		t.Fatalf("fulltextSearchNodes() returned %d candidates, want 1 (found via the synonym)", len(candidates))
	}
	if got := candidates[0].Relevance.Float(); got != fulltextRelevanceFloor {
		t.Fatalf("candidate relevance = %v, want exactly the floor %v -- coverage of the ORIGINAL term \"PR\" (absent from this candidate's own text) must not be inflated by the expansion that found it", got, fulltextRelevanceFloor)
	}
}

// TestFulltextSearchNodes_UnmatchedTermBuildsTheIdenticalQuery pins the
// byte-identity guarantee for the overwhelming majority of terms: when
// expandWithLexicon has nothing to add, the RediSearch query string sent to
// FalkorDB must be EXACTLY what it was before this ticket.
func TestFulltextSearchNodes_UnmatchedTermBuildsTheIdenticalQuery(t *testing.T) {
	var capturedQuery string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if q, ok := params["query"].(string); ok {
			capturedQuery = q
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, _, err := adapter.fulltextSearchNodes(context.Background(), "test-key", "org-1", "horizontal scaling readiness", 10, temporalFilter{}); err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	const want = "horizontal|scaling|readiness"
	if capturedQuery != want {
		t.Fatalf("query = %q, want %q unchanged (no lexicon phrase matches this term)", capturedQuery, want)
	}
}

// TestFulltextSearchNodes_LexiconDoesNotInflateAFullOriginalTermMatch is the
// precision-guard companion: a candidate that ALREADY fully matches the
// original (unexpanded) term must keep scoring at the fulltext ceiling,
// exactly as it did before this ticket -- expansion must never demote OR
// promote an existing match's confidence.
func TestFulltextSearchNodes_LexiconDoesNotInflateAFullOriginalTermMatch(t *testing.T) {
	strongHit := fulltextRow("pull_request", "pr_1", "PR review", "PR review", nil)
	fake := fixedRowsFulltextConn([]row{strongHit})
	adapter := newFakeAdapter(t, fake)
	candidates, _, err := adapter.fulltextSearchNodes(context.Background(), "test-key", "org-1", "PR review", 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("fulltextSearchNodes() returned %d candidates, want 1", len(candidates))
	}
	if got := candidates[0].Relevance.Float(); got != fulltextRelevanceCeiling {
		t.Fatalf("candidate relevance = %v, want the ceiling %v -- a full 2-of-2-original-term match must score exactly as it did before lexicon expansion", got, fulltextRelevanceCeiling)
	}
}

// TestHybridSearchNodes_EmbedsTheLexiconExpandedTerm is the L13 dual-arm
// proof: the vector arm's per-term embed call receives the lexicon-widened
// text, not the bare term, when applyLexiconToVectorArm is on.
func TestHybridSearchNodes_EmbedsTheLexiconExpandedTerm(t *testing.T) {
	if !applyLexiconToVectorArm {
		t.Skip("applyLexiconToVectorArm is off -- see lexicon.go for the measured decision")
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
	adapter := vectorAdapter(t, fake, embedder, 0.55)
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "test-key", "org-1", "PR", 10, &resolutionFence{}, temporalFilter{}); err != nil {
		t.Fatalf("hybridSearchNodes() error = %v", err)
	}
	if embedder.calls != 1 {
		t.Fatalf("embedder.calls = %d, want exactly 1", embedder.calls)
	}
}

// TestHybridSearchNodes_UnmatchedTermEmbedsUnchangedText pins the
// embedcache-friendly byte-identity guarantee on the vector side too: a
// term the lexicon has no opinion about must be embedded exactly as before
// this ticket, so a repeat of that exact term is still an embedcache hit.
func TestHybridSearchNodes_UnmatchedTermEmbedsUnchangedText(t *testing.T) {
	if got, want := vectorQueryText("horizontal scaling readiness"), "horizontal scaling readiness"; got != want {
		t.Fatalf("vectorQueryText(%q) = %q, want unchanged", want, got)
	}
}

// TestQuestionVectorSearchNodes_NoEmbedderIsANoOp proves the CHAOS-3838
// question-level pass shares hybridSearchNodes' "nothing was expected"
// posture when vector retrieval is not configured for this deployment.
func TestQuestionVectorSearchNodes_NoEmbedderIsANoOp(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	candidates, truncated, degraded, err := adapter.questionVectorSearchNodes(context.Background(), "test-key", "org-1", "what is driving this incident", 10, &resolutionFence{}, temporalFilter{})
	if err != nil {
		t.Fatalf("questionVectorSearchNodes() error = %v", err)
	}
	if candidates != nil || truncated || degraded {
		t.Fatalf("questionVectorSearchNodes() = (%v, %v, %v), want (nil, false, false) with no embedder configured", candidates, truncated, degraded)
	}
}

// TestQuestionVectorSearchNodes_DegenerateQuestionNeverEmbeds proves a
// punctuation-only (or otherwise meaningless) question is never handed to
// the embedder -- same AC-3778-4-adjacent reasoning hybridSearchNodes'
// hasLexicalContent guard uses: meaningless input must not manufacture
// arbitrary nearest-neighbor candidates, and must not spend a provider
// call on nothing.
func TestQuestionVectorSearchNodes_DegenerateQuestionNeverEmbeds(t *testing.T) {
	embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
	adapter := vectorAdapter(t, &fakeConn{}, embedder, 0.55)
	candidates, truncated, degraded, err := adapter.questionVectorSearchNodes(context.Background(), "test-key", "org-1", `??? "..." !!`, 10, &resolutionFence{}, temporalFilter{})
	if err != nil {
		t.Fatalf("questionVectorSearchNodes() error = %v", err)
	}
	if candidates != nil || truncated || degraded {
		t.Fatalf("questionVectorSearchNodes() = (%v, %v, %v), want (nil, false, false) for a punctuation-only question", candidates, truncated, degraded)
	}
	if embedder.calls != 0 {
		t.Fatalf("embedder.calls = %d, want 0 -- a meaningless question must never be embedded", embedder.calls)
	}
}

// TestQuestionVectorSearchNodes_HistoricalAxisSkipsAndDegrades mirrors
// hybridSearchNodes' identical guard: the vector index has no validity
// window, so a historical-axis question skips the vector step and reports
// degraded=true (a mechanism WAS expected and is withheld, unlike the
// no-embedder case).
func TestQuestionVectorSearchNodes_HistoricalAxisSkipsAndDegrades(t *testing.T) {
	embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
	adapter := vectorAdapter(t, &fakeConn{}, embedder, 0.55)
	temporal := temporalFilter{active: true}
	candidates, _, degraded, err := adapter.questionVectorSearchNodes(context.Background(), "test-key", "org-1", "what changed last quarter", 10, &resolutionFence{}, temporal)
	if err != nil {
		t.Fatalf("questionVectorSearchNodes() error = %v", err)
	}
	if candidates != nil || !degraded {
		t.Fatalf("questionVectorSearchNodes() = (%v, degraded=%v), want (nil, true) on a historical axis", candidates, degraded)
	}
	if embedder.calls != 0 {
		t.Fatalf("embedder.calls = %d, want 0 -- the vector step must be skipped entirely before ever embedding", embedder.calls)
	}
}

// TestQuestionVectorSearchNodes_FindsCandidatesAndTagsMatchVector is the
// success-path proof: a configured embedder on the current axis with a
// readable fence returns real candidates, every one tagged MatchVector
// (never MatchLexical -- L11 is vector-only, see the function's own doc
// comment for why).
func TestQuestionVectorSearchNodes_FindsCandidatesAndTagsMatchVector(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return nil, nil
		}
		return []row{{
			"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "Auth"}},
			"score": 0.10,
		}}, nil
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.55)
	candidates, _, degraded, err := adapter.questionVectorSearchNodes(context.Background(), "test-key", "org-1", "who is driving auth reliability", 10, &resolutionFence{}, temporalFilter{})
	if err != nil {
		t.Fatalf("questionVectorSearchNodes() error = %v", err)
	}
	if degraded {
		t.Fatal("degraded = true, want false for a clean successful search")
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want 1", candidates)
	}
	if candidates[0].Mechanism != contextfabric.MatchVector {
		t.Fatalf("candidates[0].Mechanism = %q, want MatchVector", candidates[0].Mechanism)
	}
}

// TestResolveSubjects_QuestionUnionPlusLexiconExpansionCorroboratesIntoCommit
// is the CHAOS-3838 end-to-end proof this whole ticket exists for: the
// extracted subject term ("PR") does NOT literally appear in the target
// subject's own indexed text (which only carries the synonym "pull
// request") -- WITHOUT lexicon expansion the lexical arm proposes nothing
// for it (this fake's fulltext branch only returns the row when the query
// text itself carries "pull", proving expansion is what makes the
// difference) -- but WITH L13's lexicon-widened query it does, and the
// vector arm (reached via both the per-term and the L11 question-level
// embed calls) independently proposes the same subject. Two DISTINCT
// mechanisms (MatchLexical + MatchVector) corroborate it into graphrank's
// [0.72, 0.86] auto-commit band, exactly as AC-3778-3 requires (a vector
// hit alone never commits) and AC-3778-2 asks for (a real committed-answer
// lift on a paraphrase-shaped question).
func TestResolveSubjects_QuestionUnionPlusLexiconExpansionCorroboratesIntoCommit(t *testing.T) {
	target := &node{Properties: map[string]interface{}{
		propKind: "pull_request", propCanonicalID: "pr_99", propLabel: "Fix login bug",
		propSearchText: "Fix login bug pull request",
	}}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.vector.queryNodes"):
			return []row{{"node": target, "score": 0.20}}, nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			q, _ := params["query"].(string)
			if strings.Contains(strings.ToLower(q), "pull") {
				return []row{{"node": target}}, nil
			}
			return nil, nil
		default:
			return nil, nil
		}
	}}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := newFakeAdapter(t, fake)
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: []float32{1, 0, 0, 0}}, SimilarityFloor: 0.55})

	request := contextfabric.InvestigationRequest{
		Question: "who fixed the PR for the login bug",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"PR"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}

	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "pr_99" {
		t.Fatalf("resolution.Committed = %#v, want pr_99 auto-committed via question-vector-union + lexicon-expansion corroboration", resolution.Committed)
	}
	var candidate *contextfabric.SubjectCandidate
	for i := range resolution.Candidates {
		if resolution.Candidates[i].Subject.CanonicalID == "pr_99" {
			candidate = &resolution.Candidates[i]
		}
	}
	if candidate == nil {
		t.Fatal("committed subject missing from resolution.Candidates")
	}
	if !graphrank.HasMechanism(candidate.MatchMechanisms, contextfabric.MatchVector) || !graphrank.HasMechanism(candidate.MatchMechanisms, contextfabric.MatchLexical) {
		t.Fatalf("candidate.MatchMechanisms = %v, want BOTH MatchVector (question union) and MatchLexical (lexicon expansion) present -- that pairing is the whole corroboration mechanism this ticket delivers", candidate.MatchMechanisms)
	}
	if candidate.Confidence < graphrank.CorroboratedFloor {
		t.Fatalf("candidate.Confidence = %v, want >= %v (the corroborated commit gate)", candidate.Confidence, graphrank.CorroboratedFloor)
	}
}
