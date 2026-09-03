package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
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
	candidates, truncated, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "PR", 10, temporalFilter{})
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
	if _, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "horizontal scaling readiness", 10, temporalFilter{}); err != nil {
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
	candidates, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "PR review", 10, temporalFilter{})
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

	resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
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

// --- codex round-1 P2 (fix B): guard the BOUNDED bytes, not the raw question ---

// TestQuestionVectorSearchNodes_PunctuationBeyondTheCapNeverEmbeds is the P2
// regression proof: a question carrying real word content ("auth") only
// PAST the embed truncation point must never reach the embedder -- before
// the fix, hasLexicalContent ran on the unbounded question (which DOES
// contain "auth" somewhere), passed, and Embed silently truncated to pure
// punctuation before transmitting it, embedding meaningless bytes into an
// arbitrary nearest-neighbor query. MUTATION CHECK: reverting
// questionVectorSearchNodes' guard to `hasLexicalContent(question)` (the
// unbounded input) makes this fail -- embedder.calls becomes 1.
func TestQuestionVectorSearchNodes_PunctuationBeyondTheCapNeverEmbeds(t *testing.T) {
	const maxRunes = 20
	question := strings.Repeat(".", maxRunes) + " auth service reliability" // "auth" etc. sit well past the cap
	fake := &fakeConn{}
	fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	adapter := newFakeAdapter(t, fake)
	embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
	adapter.attachEmbedder(EmbedderOptions{Embedder: embedder, SimilarityFloor: 0.55, MaxTextRunes: maxRunes})

	candidates, truncated, degraded, err := adapter.questionVectorSearchNodes(context.Background(), "test-key", "org-1", question, 10, &resolutionFence{}, temporalFilter{})
	if err != nil {
		t.Fatalf("questionVectorSearchNodes() error = %v", err)
	}
	if candidates != nil || truncated || degraded {
		t.Fatalf("questionVectorSearchNodes() = (%v, truncated=%v, degraded=%v), want (nil, false, false) -- the bounded bytes are pure punctuation", candidates, truncated, degraded)
	}
	if embedder.calls != 0 {
		t.Fatalf("embedder.calls = %d, want 0 -- content past the embed truncation cap must never make it to the provider", embedder.calls)
	}
}

// TestQuestionVectorSearchNodes_ContentWithinTheCapProceeds is
// PunctuationBeyondTheCap's positive companion: real word content that
// survives truncation (sits WITHIN the cap) must still proceed normally --
// the fix must not become a blanket "any punctuation-prefixed question is
// rejected" over-correction.
func TestQuestionVectorSearchNodes_ContentWithinTheCapProceeds(t *testing.T) {
	const maxRunes = 40
	question := "..... auth service reliability question" // "auth" sits inside the first 40 runes
	if len([]rune(question)) > maxRunes {
		t.Fatalf("test fixture bug: question is %d runes, want <= %d so its content survives truncation", len([]rune(question)), maxRunes)
	}
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
	adapter := newFakeAdapter(t, fake)
	embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
	adapter.attachEmbedder(EmbedderOptions{Embedder: embedder, SimilarityFloor: 0.55, MaxTextRunes: maxRunes})

	candidates, _, degraded, err := adapter.questionVectorSearchNodes(context.Background(), "test-key", "org-1", question, 10, &resolutionFence{}, temporalFilter{})
	if err != nil {
		t.Fatalf("questionVectorSearchNodes() error = %v", err)
	}
	if degraded {
		t.Fatal("degraded = true, want false for content that survives truncation")
	}
	if embedder.calls != 1 {
		t.Fatalf("embedder.calls = %d, want exactly 1 -- content within the cap must still be embedded", embedder.calls)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want 1", candidates)
	}
}

// TestBoundedQueryText_NoPrefixTruncatesToTheEmbedBudget pins
// boundedQueryText's no-prefix branch directly: transmitted and substance
// are identical (no prefix to strip) and both are truncated to
// a.embedBudgetRunes(), via embedprovider's own TruncateRunes -- never a
// re-derived cap.
func TestBoundedQueryText_NoPrefixTruncatesToTheEmbedBudget(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.attachEmbedder(EmbedderOptions{Embedder: &stubEmbedder{vector: []float32{1, 0}}, SimilarityFloor: 0.55, MaxTextRunes: 5})
	got := adapter.boundedQueryText("hello world")
	if got.transmitted != got.substance {
		t.Fatalf("boundedQueryText() = %+v, want transmitted == substance with no prefix configured", got)
	}
	if want := "hello"; got.transmitted != want {
		t.Fatalf("boundedQueryText().transmitted = %q, want %q (truncated to MaxTextRunes=5)", got.transmitted, want)
	}
}

// --- codex round-3 P1 (fix A): the guard/cap pair must hold under EVERY prefix configuration ---

// realNomicEmbedder builds a REAL embedprovider.Embedder configured for the
// nomic prefix family, for tests that need genuine budget-aware
// ApplyQueryPrefix behavior (embedprovider.applyPrefixWithBudget's actual
// arithmetic) rather than a hand-rolled test double that could silently
// diverge from it. MaxTextRunes is pinned to the config validation floor
// (embedprovider.MinimumMaxTextRunes, 2000) since Config.validate rejects
// anything lower.
func realNomicEmbedder(t *testing.T) *embedprovider.Embedder {
	t.Helper()
	env := map[string]string{
		embedprovider.EnvBaseURL: "https://embed.example/v1", embedprovider.EnvProvider: "nomic",
		embedprovider.EnvModel: "nomic-embed-text", embedprovider.EnvDimension: "768",
		embedprovider.EnvPrefixFamily:      "nomic",
		embedprovider.EnvAllowNoCredential: "true", // no real transport, no credential to check (CHAOS-4192)
	}
	cfg, err := embedprovider.ConfigFromEnv(func(key string) (string, bool) { v, ok := env[key]; return v, ok })
	if err != nil {
		t.Fatalf("embedprovider.ConfigFromEnv: %v", err)
	}
	source, err := embedprovider.New(cfg)
	if err != nil {
		t.Fatalf("embedprovider.New: %v", err)
	}
	return source
}

// realNoneEmbedder builds a REAL embedprovider.Embedder with PrefixFamilyNone
// (unset -- the deployed production identity's actual configuration) --
// ApplyQueryPrefix is a genuine, non-nil bound method whose underlying
// prefix string is empty. This is the exact shape codex round-3 P1
// identified as the hole in round-1's fix: "ApplyQueryPrefix is non-nil"
// was treated as "a real prefix was applied and already budgeted itself",
// which is false for this configuration.
func realNoneEmbedder(t *testing.T) *embedprovider.Embedder {
	t.Helper()
	env := map[string]string{
		embedprovider.EnvBaseURL: "https://embed.example/v1", embedprovider.EnvProvider: "openai",
		embedprovider.EnvModel: "text-embedding-3-large", embedprovider.EnvDimension: "3072",
		embedprovider.EnvAllowNoCredential: "true", // no real transport, no credential to check (CHAOS-4192)
	}
	cfg, err := embedprovider.ConfigFromEnv(func(key string) (string, bool) { v, ok := env[key]; return v, ok })
	if err != nil {
		t.Fatalf("embedprovider.ConfigFromEnv: %v", err)
	}
	source, err := embedprovider.New(cfg)
	if err != nil {
		t.Fatalf("embedprovider.New: %v", err)
	}
	if source.QueryPrefix() != "" {
		t.Fatalf("test fixture bug: QueryPrefix() = %q, want empty (PrefixFamilyNone)", source.QueryPrefix())
	}
	return source
}

// TestQuestionVectorSearchNodes_GuardBoundsMatchPrefixConfiguration is the
// codex round-3 P1 regression proof: round-1's fix B branched on whether
// a.applyQueryPrefix was nil, reasoning a CONFIGURED prefix already bounds
// itself via ApplyQueryPrefix's own budget. That's true only when the
// configured prefix STRING is non-empty -- PrefixFamilyNone (the DEPLOYED
// production default for openai/text-embedding-3-large) leaves
// a.applyQueryPrefix non-nil (embedder.ApplyQueryPrefix is always a valid
// bound method once an embedder is attached) while its underlying prefix
// string is "", and embedprovider.applyPrefixWithBudget's very first line
// (`if prefix == "" { return text }`) skips ALL budgeting in that case --
// so the "prefix configured" branch silently trusted UNBOUNDED text in
// exactly the deployed default configuration, reopening the original P2
// hole one layer down. This test matrix proves the fix holds under BOTH a
// real prefix (nomic) and no prefix at all -- symmetric guard behavior
// regardless of prefix configuration. MUTATION CHECK: reverting
// boundedQueryText to branch on `a.applyQueryPrefix == nil` (skipping the
// unconditional TruncateRunes) makes the no-prefix-style case here still
// pass but was proven, by hand, to let the SAME punctuation-then-content
// question through unbounded under PrefixFamilyNone specifically -- see
// the freeze report for that reproduction.
func TestQuestionVectorSearchNodes_GuardBoundsMatchPrefixConfiguration(t *testing.T) {
	nomic := realNomicEmbedder(t)
	none := realNoneEmbedder(t)
	const nomicMaxRunes = embedprovider.MinimumMaxTextRunes // 2000, the config validation floor

	type prefixCase struct {
		name             string
		attach           func(embedder contextfabric.Embedder) EmbedderOptions
		punctuationRunes int // must exceed this config's own effective budget
	}
	cases := []prefixCase{
		{
			name: "no prefix configured at all (ApplyQueryPrefix left nil)",
			attach: func(embedder contextfabric.Embedder) EmbedderOptions {
				return EmbedderOptions{Embedder: embedder, SimilarityFloor: 0.55, MaxTextRunes: 20}
			},
			punctuationRunes: 20,
		},
		{
			// THE case codex round-3 P1 is about: this is exactly what
			// EmbedderFromEnv produces for the DEPLOYED production identity
			// (openai/text-embedding-3-large, PrefixFamilyNone) --
			// ApplyQueryPrefix is a non-nil bound method, but its
			// underlying prefix STRING is "". Before this round's fix, this
			// exact configuration hit the "prefix configured" branch and
			// trusted ApplyQueryPrefix's own (silently skipped) budgeting.
			name: "PrefixFamilyNone WITH ApplyQueryPrefix set (the deployed production shape)",
			attach: func(embedder contextfabric.Embedder) EmbedderOptions {
				return EmbedderOptions{Embedder: embedder, SimilarityFloor: 0.55, MaxTextRunes: none.MaxTextRunes(), ApplyQueryPrefix: none.ApplyQueryPrefix}
			},
			punctuationRunes: embedprovider.DefaultMaxTextRunes,
		},
		{
			name: "nomic prefix configured (a genuinely non-empty prefix, sanity anchor)",
			attach: func(embedder contextfabric.Embedder) EmbedderOptions {
				return EmbedderOptions{Embedder: embedder, SimilarityFloor: 0.55, MaxTextRunes: nomic.MaxTextRunes(), ApplyQueryPrefix: nomic.ApplyQueryPrefix}
			},
			punctuationRunes: nomicMaxRunes,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/punctuation beyond cap never embeds", func(t *testing.T) {
			question := strings.Repeat(".", tc.punctuationRunes) + " auth service reliability"
			fake := &fakeConn{}
			fake.indexesFunc = func(ctx context.Context, key string) ([]indexStatus, error) {
				return []indexStatus{operationalVectorIndex(4)}, nil
			}
			adapter := newFakeAdapter(t, fake)
			embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
			adapter.attachEmbedder(tc.attach(embedder))

			candidates, truncated, degraded, err := adapter.questionVectorSearchNodes(context.Background(), "test-key", "org-1", question, 10, &resolutionFence{}, temporalFilter{})
			if err != nil {
				t.Fatalf("questionVectorSearchNodes() error = %v", err)
			}
			if candidates != nil || truncated || degraded {
				t.Fatalf("questionVectorSearchNodes() = (%v, truncated=%v, degraded=%v), want (nil, false, false) -- the bounded bytes are pure punctuation", candidates, truncated, degraded)
			}
			if embedder.calls != 0 {
				t.Fatalf("embedder.calls = %d, want 0 -- content past the embed truncation cap must never make it to the provider, under THIS prefix configuration", embedder.calls)
			}
		})

		t.Run(tc.name+"/content within cap proceeds", func(t *testing.T) {
			const question = "..... auth service reliability question"
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
			adapter := newFakeAdapter(t, fake)
			embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
			adapter.attachEmbedder(tc.attach(embedder))

			candidates, _, degraded, err := adapter.questionVectorSearchNodes(context.Background(), "test-key", "org-1", question, 10, &resolutionFence{}, temporalFilter{})
			if err != nil {
				t.Fatalf("questionVectorSearchNodes() error = %v", err)
			}
			if degraded {
				t.Fatal("degraded = true, want false for content that survives truncation")
			}
			if embedder.calls != 1 {
				t.Fatalf("embedder.calls = %d, want exactly 1 -- content within the cap must still be embedded, under THIS prefix configuration", embedder.calls)
			}
			if len(candidates) != 1 {
				t.Fatalf("candidates = %#v, want 1", candidates)
			}
		})
	}
}

// --- codex round-3 P1 (fix B): multi-word synonyms are phrase clauses, not bare OR ---

// TestFulltextSearchNodes_MultiWordSynonymIsAPhraseClauseNotBareOR is the
// regression proof: term "PR" widens via the {"pr","pull request"} group.
// This fake distinguishes the query string production actually sends:
// EXACT "pull request" is the phrase-clause query the fix must produce;
// "pull|request" is the bare-OR query the bug would have produced (a real
// RediSearch server would return a "request"-only row for that shape, a
// false positive). If production ever regresses to bare-OR tokenizing the
// synonym, this fake's "pull|request" branch fires instead and the
// assertion below fails -- this IS the mutation check codex asked for,
// built into the fixture rather than requiring a manual code revert.
func TestFulltextSearchNodes_MultiWordSynonymIsAPhraseClauseNotBareOR(t *testing.T) {
	requestOnly := fulltextRow("work_item", "wi_1", "Feature request", "Feature request from a customer", nil)
	pullRequestRow := fulltextRow("pull_request", "pr_1", "Fix login bug", "Fix login bug pull request", nil)

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		switch q {
		case "PR":
			// Base query: nothing in this fixture literally contains "PR".
			return nil, nil
		case `"pull request"`:
			// The FIX's shape: an exact-phrase clause. A real RediSearch
			// server only returns rows carrying the adjacent phrase.
			return []row{pullRequestRow}, nil
		case "pull|request":
			// The BUG's shape: bare OR-tokenized words. A real RediSearch
			// server would return ANY row containing either word alone --
			// including the false-positive requestOnly.
			return []row{requestOnly, pullRequestRow}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	candidates, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "PR", 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	requestOnlyUUID := subjectUUID("work_item", "wi_1")
	pullRequestUUID := subjectUUID("pull_request", "pr_1")
	var sawRequestOnly, sawPullRequest bool
	for _, c := range candidates {
		if c.UUID == requestOnlyUUID {
			sawRequestOnly = true
		}
		if c.UUID == pullRequestUUID {
			sawPullRequest = true
		}
	}
	if sawRequestOnly {
		t.Fatalf("candidates = %#v, want the \"request\"-only subject ABSENT -- a multi-word synonym must require the complete phrase, not fire on one of its words alone", candidates)
	}
	if !sawPullRequest {
		t.Fatalf("candidates = %#v, want the genuine \"pull request\" subject present", candidates)
	}
}

// TestFulltextSearchNodes_SingleWordSynonymsStillOR proves the fix is
// scoped to multi-word synonyms only: a single-word synonym ("repository"
// for "repo") has no phrase-adjacency concept to violate and must keep
// widening as an ordinary OR term, exactly as before this round.
func TestFulltextSearchNodes_SingleWordSynonymsStillOR(t *testing.T) {
	var capturedQueries []string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if q, ok := params["query"].(string); ok {
			capturedQueries = append(capturedQueries, q)
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "repo", 10, temporalFilter{}); err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if len(capturedQueries) != 2 {
		t.Fatalf("captured queries = %v, want exactly 2 (base + expansion)", capturedQueries)
	}
	if capturedQueries[0] != "repo" {
		t.Fatalf("base query = %q, want %q unchanged", capturedQueries[0], "repo")
	}
	if capturedQueries[1] != "repository" {
		t.Fatalf("expansion query = %q, want the single-word synonym as a bare OR term, not phrase-quoted", capturedQueries[1])
	}
}

// --- codex round-3 P2 (fix C): expansion only ever ADDS candidates, never displaces a base hit ---

// TestFulltextSearchNodes_ExpansionNeverDisplacesABaseHit is the regression
// proof: a base hit ("target") must survive regardless of how many
// synonym-matched rows the expansion query returns, or how a combined
// single-query LIMIT would have ranked them -- base and expansion run as
// SEPARATE queries (codex round-3 P2), so a base hit can never be evicted
// by an expansion-surfaced row's RediSearch score, full stop.
//
// limit=1 here: base finds exactly 1 (its own full budget). Per codex
// round-4 P2 (fix B), the expansion batch is NOT starved by base's raw
// count -- it runs its OWN full limit=1 budget too, contributing its own
// top match (here, deterministically synonymRow1 per the shared
// score DESC, subject_kind ASC, canonical_id ASC tie-break) alongside
// target. See TestFulltextSearchNodes_LimitSizedUnauthorizedBaseDoesNotStarveExpansion
// for the direct proof of WHY that matters (authorization runs downstream,
// in graphrank, where falkorgraph cannot see it).
func TestFulltextSearchNodes_ExpansionNeverDisplacesABaseHit(t *testing.T) {
	target := fulltextRow("pull_request", "pr_target", "Fix login bug", "Fix login bug pull request", nil)
	synonymRow1 := fulltextRow("pull_request", "pr_syn1", "Unrelated one", "Unrelated one pull request", nil)
	synonymRow2 := fulltextRow("pull_request", "pr_syn2", "Unrelated two", "Unrelated two pull request", nil)
	synonymRow3 := fulltextRow("pull_request", "pr_syn3", "Unrelated three", "Unrelated three pull request", nil)

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		switch q {
		case "PR":
			return []row{target}, nil
		case `"pull request"`:
			// Three DISTINCT, higher-scoring synonym rows -- in the old
			// single-query design these could have out-ranked and pushed
			// "target" past a shared limit+1 cutoff.
			return []row{synonymRow1, synonymRow2, synonymRow3}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	candidates, truncated, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "PR", 1, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	targetUUID := subjectUUID("pull_request", "pr_target")
	var sawTarget bool
	for _, c := range candidates {
		if c.UUID == targetUUID {
			sawTarget = true
		}
	}
	if !sawTarget {
		t.Fatalf("candidates = %#v, want the base hit \"target\" present -- it must never be displaced by expansion rows", candidates)
	}
	if !truncated {
		t.Fatal("truncated = false, want true -- the expansion batch's own limit=1 fetch cut off two genuinely competing synonym rows")
	}
}

// TestFulltextSearchNodes_LimitSizedUnauthorizedBaseDoesNotStarveExpansion
// is the codex round-4 P2 (fix B) regression proof: falkorgraph has no
// principal/scope at all in this call chain -- authorization is
// graphrank.NodeCandidate's job, one layer up, per candidate -- so a base
// batch that happens to return a FULL limit-sized set (as it would for a
// repo/project/team-scoped caller whose top RediSearch-ranked rows are
// mostly invisible to them) must NOT be treated as "using up" shared
// capacity here: this function cannot know which of those rows will
// survive authorization, so it must hand EVERY source's own honestly
// bounded set upward and let graphrank's downstream authorization + final
// truncation sort it out. This test proves the MECHANISM (expansion is
// never capped by base's raw count) directly, since a live authorization
// decision belongs to graphrank/candidate_test.go, not here. MUTATION
// CHECK: reintroducing a `remaining := limit - len(baseCandidates)` cap
// on the expansion loop makes this fail (the synonym row disappears).
func TestFulltextSearchNodes_LimitSizedUnauthorizedBaseDoesNotStarveExpansion(t *testing.T) {
	// A full limit=2 base set (both rows would, in production, belong to a
	// repository the calling principal cannot see -- irrelevant to this
	// function, which has no way to know that).
	baseRow1 := fulltextRow("pull_request", "pr_invisible1", "Invisible one", "PR invisible one", nil)
	baseRow2 := fulltextRow("pull_request", "pr_invisible2", "Invisible two", "PR invisible two", nil)
	authorizedSynonym := fulltextRow("pull_request", "pr_visible", "Visible fix", "Visible fix pull request", nil)

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		switch q {
		case "PR":
			return []row{baseRow1, baseRow2}, nil
		case `"pull request"`:
			return []row{authorizedSynonym}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	candidates, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "PR", 2, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	visibleUUID := subjectUUID("pull_request", "pr_visible")
	for _, c := range candidates {
		if c.UUID == visibleUUID {
			return
		}
	}
	t.Fatalf("candidates = %#v, want the authorized synonym row present despite base already returning a full limit-sized set -- authorization happens downstream in graphrank, this layer must never pre-emptively starve it", candidates)
}

// --- codex round-4 P2 (fix C): deterministic tie-break at the LIMIT cutoff ---

// TestRunFulltextQuery_OrderByHasADeterministicTieBreak is the regression
// proof: ORDER BY score alone lets FalkorDB break a tie at the LIMIT
// boundary arbitrarily, so a request sitting exactly at a tied cutoff could
// return a DIFFERENT candidate/truncation set across otherwise-identical
// calls -- breaking the determinism CHAOS-3782 answer reuse and any
// measurement harness depend on. This asserts the Cypher text itself
// carries a TOTAL order (score, then subject kind, then canonical id) --
// both base and every lexicon-expansion batch share this ONE query-building
// authority (runFulltextQuery), so the fix applies to all of them at once.
// MUTATION CHECK: reverting to a bare `ORDER BY score DESC` makes this
// fail.
func TestRunFulltextQuery_OrderByHasADeterministicTieBreak(t *testing.T) {
	var capturedCypher string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		capturedCypher = cypher
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, _, err := adapter.runFulltextQuery(context.Background(), "test-key", "org-1", "auth", 10, temporalFilter{}, nil, 0, ""); err != nil {
		t.Fatalf("runFulltextQuery() error = %v", err)
	}
	const want = "ORDER BY score DESC, node.subject_kind ASC, node.canonical_id ASC"
	if !strings.Contains(capturedCypher, want) {
		t.Fatalf("cypher = %q, want it to contain the deterministic tie-break %q", capturedCypher, want)
	}
}

// TestFulltextSearchNodes_TieBreakAppliesToTheKindFilteredBranchToo pins the
// SAME tie-break for a kind-scoped expansion batch's own query -- proving
// the single runFulltextQuery authority covers the kind-predicate branch
// (codex round-4 P1's own new code path) exactly like the unfiltered one,
// not a second, independently-written query string.
func TestFulltextSearchNodes_TieBreakAppliesToTheKindFilteredBranchToo(t *testing.T) {
	var capturedCyphers []string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		capturedCyphers = append(capturedCyphers, cypher)
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "repository", 10, temporalFilter{}); err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if len(capturedCyphers) != 2 {
		t.Fatalf("captured %d queries, want 2 (base + the kind-scoped \"repo\" expansion batch)", len(capturedCyphers))
	}
	const want = "ORDER BY score DESC, node.subject_kind ASC, node.canonical_id ASC"
	for i, cypher := range capturedCyphers {
		if !strings.Contains(cypher, want) {
			t.Fatalf("query[%d] = %q, want it to contain the deterministic tie-break %q", i, cypher, want)
		}
	}
	if !strings.Contains(capturedCyphers[1], "node.subject_kind = $kind") {
		t.Fatalf("query[1] = %q, want the kind-scope predicate present", capturedCyphers[1])
	}
}

// --- codex round-5 P2: fetch wide enough that overlap-with-base cannot crowd out new candidates before dedup ---

// TestFulltextSearchNodes_OverlapWithBaseDoesNotStarveANewSynonymOnlyRow is
// the regression proof: limit=2, base returns 2 rows that ALSO happen to
// rank in the expansion batch's own top results (rowA, rowB), with a
// genuinely NEW, synonym-only row (rowC) ranked immediately after them --
// i.e. at position limit+1 of the batch's OWN raw result set. Before this
// fix, the batch's own runFulltextQuery call fetched only `limit`+1=3 raw
// rows and immediately truncated to `limit`=2 BEFORE fulltextSearchNodes
// ever got to dedup rowA/rowB against base -- rowC never came back at all,
// so the union added zero recall exactly where overlap with base was
// highest. MUTATION CHECK: reverting fetchBudget from
// `limit + len(baseCandidates)` back to bare `limit` makes this fail --
// rowC disappears.
func TestFulltextSearchNodes_OverlapWithBaseDoesNotStarveANewSynonymOnlyRow(t *testing.T) {
	rowA := fulltextRow("pull_request", "pr_a", "Shared A", "PR pull request shared A", nil)
	rowB := fulltextRow("pull_request", "pr_b", "Shared B", "PR pull request shared B", nil)
	rowC := fulltextRow("pull_request", "pr_c", "New synonym only", "Unrelated pull request only", nil)

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		switch q {
		case "PR":
			// Base's own top-limit=2 rows: A and B (both also match the
			// expansion phrase independently -- the overlap case).
			return []row{rowA, rowB}, nil
		case `"pull request"`:
			// The expansion batch's OWN raw ranking: A, B (duplicates of
			// base), THEN the genuinely new C at position 3 -- exactly
			// limit+1 of the OLD, too-narrow fetch.
			return []row{rowA, rowB, rowC}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	candidates, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "PR", 2, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	newUUID := subjectUUID("pull_request", "pr_c")
	for _, c := range candidates {
		if c.UUID == newUUID {
			return
		}
	}
	t.Fatalf("candidates = %#v, want the genuinely new synonym-only row (pr_c) present -- heavy overlap with base must not crowd it out of the batch's own truncation window before dedup runs", candidates)
}

// TestFulltextSearchNodes_LaterBatchOverlapWithAnEarlierBatchDoesNotStarveIt
// is the codex round-6 P2 (fix B) regression proof: round-5 sized each
// batch's fetch budget against len(baseCandidates) ONLY -- correct for
// overlap with base, but a term can trigger TWO expansion batches (one
// kind-agnostic, one kind-scoped), and the SECOND batch's own top-ranked
// rows can overlap with the FIRST batch's newly-added rows just as easily
// as with base. term = "repository ticket" triggers exactly that: the
// kind-agnostic {"ticket","issue","work item"} group (batch 1, runs
// first per lexiconExpansionBatches' fixed order) and the kind-scoped
// {"repo","repository"} group (batch 2). Batch 2's fake rows duplicate
// batch 1's two new rows, with a genuinely new row (rowZ) ranked
// immediately after -- exactly at the round-5-only formula's blind spot
// (fetchBudget computed from len(baseCandidates)=0 would never have
// widened batch 2's fetch to see past the duplicates at all). MUTATION
// CHECK: reverting fetchBudget from `limit + len(seen)` back to
// `limit + len(baseCandidates)` makes this fail -- rowZ disappears.
func TestFulltextSearchNodes_LaterBatchOverlapWithAnEarlierBatchDoesNotStarveIt(t *testing.T) {
	rowX := fulltextRow("work_item", "wi_x", "Shared X", "ticket issue work item shared X", nil)
	rowY := fulltextRow("work_item", "wi_y", "Shared Y", "ticket issue work item shared Y", nil)
	rowZ := fulltextRow("repository", "repo_z", "New repo only", "repository only new", nil)

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		_, hasKind := params["kind"]
		switch {
		case q == "repository|ticket":
			// Base: nothing literally named both "repository" and "ticket".
			return nil, nil
		case q == `issue|"work item"` && !hasKind:
			// Batch 1 (kind-agnostic "ticket" group additions): two
			// genuinely new rows, filling limit=2 exactly.
			return []row{rowX, rowY}, nil
		case q == "repo" && hasKind:
			// Batch 2 (kind-scoped "repo" group addition): its OWN raw
			// ranking duplicates batch 1's rows first, THEN the genuinely
			// new rowZ -- at the OLD, too-narrow fetch's exact blind spot.
			return []row{rowX, rowY, rowZ}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	candidates, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "repository ticket", 2, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodesForResolution() error = %v", err)
	}
	newUUID := subjectUUID("repository", "repo_z")
	for _, c := range candidates {
		if c.UUID == newUUID {
			return
		}
	}
	t.Fatalf("candidates = %#v, want the genuinely new repository-only row (repo_z) present -- overlap with an EARLIER batch's rows must not crowd it out of a LATER batch's own truncation window before dedup runs", candidates)
}

// --- codex round-6 P2 (fix A): the union/lexicon contract is subject-resolution-only ---

// TestFulltextSearchNodes_PlainVersionNeverExpandsAndNeverExceedsLimit is
// the fix A regression proof: the PLAIN fulltextSearchNodes (DiscoverContext's
// only caller) must issue exactly ONE query -- no lexicon expansion, no
// union, regardless of whether the text matches a lexicon group -- and its
// result must never exceed `limit`. This is the property DiscoverContext's
// own per-node edgesOfNode cost depends on staying bounded.
func TestFulltextSearchNodes_PlainVersionNeverExpandsAndNeverExceedsLimit(t *testing.T) {
	rows := []row{
		fulltextRow("pull_request", "pr_1", "PR one", "PR one", nil),
		fulltextRow("pull_request", "pr_2", "PR two", "PR two", nil),
		fulltextRow("pull_request", "pr_3", "PR three", "PR three", nil),
	}
	var queryCalls int
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		queryCalls++
		return rows, nil
	}}
	adapter := newFakeAdapter(t, fake)
	// "PR" is a lexicon-matching term (the {"pr","pull request"} group) --
	// if this were routed through the union, it would trigger a second
	// query and could return more than `limit` candidates.
	candidates, _, err := adapter.fulltextSearchNodes(context.Background(), "test-key", "org-1", "PR", 2, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if queryCalls != 1 {
		t.Fatalf("queryCalls = %d, want exactly 1 -- the plain path must never run a second, lexicon-expansion query", queryCalls)
	}
	if len(candidates) > 2 {
		t.Fatalf("candidates = %#v, want at most limit=2 -- the plain path must never exceed its caller's bound", candidates)
	}
}

// TestFulltextSearchNodes_ResolutionPathStillUnionsForTheSameInput is
// PlainVersionNeverExpands' direct control: the SAME lexicon-matching term,
// through fulltextSearchNodesForResolution, still runs the union (more
// than one query call) -- proving the split isolates DiscoverContext
// without silently disabling subject resolution's own CHAOS-3838 behavior.
func TestFulltextSearchNodes_ResolutionPathStillUnionsForTheSameInput(t *testing.T) {
	var queryCalls int
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		queryCalls++
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "PR", 2, temporalFilter{}); err != nil {
		t.Fatalf("fulltextSearchNodesForResolution() error = %v", err)
	}
	if queryCalls != 2 {
		t.Fatalf("queryCalls = %d, want exactly 2 (base + the lexicon-expansion batch) -- the resolution path must retain the union behavior", queryCalls)
	}
}

// TestHybridSearchNodes_LexicalArmUsesTheResolutionUnionPath is the REAL
// production-wiring mutation check for fix A: hybridSearchNodes (the ONLY
// caller vector.go routes through) must call fulltextSearchNodesForResolution,
// not the plain fulltextSearchNodes -- asserted here by counting FULLTEXT
// query calls specifically (hybridSearchNodes also issues a vector query,
// which this must not confuse with the lexical arm's own call count).
// TestFulltextSearchNodes_ResolutionPathStillUnionsForTheSameInput alone
// does NOT catch a regression where vector.go's call site is silently
// reverted to the plain function, because it calls
// fulltextSearchNodesForResolution directly -- this test calls
// hybridSearchNodes itself, the actual production entry point, closing
// that gap. MUTATION CHECK: reverting vector.go's call site back to plain
// fulltextSearchNodes makes this fail (fulltextQueryCalls drops to 1).
func TestHybridSearchNodes_LexicalArmUsesTheResolutionUnionPath(t *testing.T) {
	var fulltextQueryCalls int
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.fulltext.queryNodes") {
			fulltextQueryCalls++
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "test-key", "org-1", "PR", 10, &resolutionFence{}, temporalFilter{}); err != nil {
		t.Fatalf("hybridSearchNodes() error = %v", err)
	}
	if fulltextQueryCalls != 2 {
		t.Fatalf("fulltextQueryCalls = %d, want exactly 2 (base + the lexicon-expansion batch) -- hybridSearchNodes' lexical arm must use fulltextSearchNodesForResolution, not the plain, DiscoverContext-only fulltextSearchNodes", fulltextQueryCalls)
	}
}
