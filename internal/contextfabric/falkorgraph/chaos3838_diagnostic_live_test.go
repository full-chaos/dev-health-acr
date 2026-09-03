package falkorgraph

import (
	"context"
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAmbiguityBenchmarkDiagnosesWhyNoCommitDominates is a CHAOS-3838
// diagnostic companion to TestAmbiguityBenchmarkMeasuresTheHybridLift: when
// the AC-3778-2 lift stays flat, "no-commit dominates" alone does not say
// WHY -- arms found nothing at all, or arms found something that never
// reached a commit gate. This reports ONLY aggregate counts (never a
// question, a candidate label, or any other corpus-derived text) so it
// respects the withheld-corpus boundary while still being diagnostic.
//
// Same env contract as the benchmark (ACR_TEST_*); skips under the same
// conditions.
func TestAmbiguityBenchmarkDiagnosesWhyNoCommitDominates(t *testing.T) {
	corpus := loadAmbiguityCorpus(t)
	address := os.Getenv("ACR_TEST_FALKOR_ADDR")
	if address == "" {
		t.Skip("ACR_TEST_FALKOR_ADDR is not set; this benchmark measures against live data")
	}
	orgID := os.Getenv("ACR_TEST_AMBIGUITY_ORG")
	if orgID == "" {
		t.Skip("ACR_TEST_AMBIGUITY_ORG is not set")
	}
	principal := storage.Principal{OrgID: orgID}
	ctx := context.Background()

	graphConfig, err := ConfigFromEnv(benchmarkLookup)
	if err != nil {
		t.Fatalf("graph configuration: %v", err)
	}
	embedderOptions, err := EmbedderFromEnv(benchmarkLookup)
	if err != nil {
		t.Fatalf("embedder configuration: %v", err)
	}
	if embedderOptions.Embedder == nil {
		t.Fatal("ACR_TEST_EMBED_BASE_URL is not set; nothing to diagnose without the hybrid arm")
	}
	adapter, err := NewWithEmbedder(graphConfig, embedderOptions)
	if err != nil {
		t.Fatalf("hybrid adapter: %v", err)
	}

	var (
		noCandidatesAtAll                  int // neither arm proposed anything
		candidatesNoCommit                 int // something was proposed, nothing committed
		corroboratedNoCommit               int // >=2 distinct mechanisms on some candidate, still no commit
		corroboratedAloneNoCommit          int // corroborated AND the only candidate in the resolution (searchTruncated is the only remaining explanation for the miss)
		corroboratedWithCompetitorNoCommit int // corroborated but NOT alone (the [0.72,0.86] band structurally cannot clear the 0.88 top-of-two gate against ANY competitor -- CHAOS-3829 territory, not a T8 bug)
		vectorOnlyTopCandidate             int // top candidate's ONLY mechanism is MatchVector (AC-3778-3's exact scenario)
		retrievalDegraded                  int
		committed                          int
	)
	for _, testCase := range corpus {
		request := contextfabric.InvestigationRequest{
			Question: testCase.Question,
			Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, AllowClarification: true},
		}
		interpreted := contextfabric.InterpretedQuestion{SubjectTerms: testCase.effectiveSubjectTerms()}
		resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil)
		if err != nil {
			t.Fatalf("ResolveSubjects: %v", err)
		}
		if resolution.RetrievalDegraded {
			retrievalDegraded++
		}
		if len(resolution.Committed) > 0 {
			committed++
			continue
		}
		if len(resolution.Candidates) == 0 {
			noCandidatesAtAll++
			continue
		}
		candidatesNoCommit++
		anyCorroborated := false
		for _, c := range resolution.Candidates {
			if graphrank.DistinctMechanismCount(c.MatchMechanisms) >= 2 {
				anyCorroborated = true
			}
		}
		if anyCorroborated {
			corroboratedNoCommit++
			if len(resolution.Candidates) == 1 {
				corroboratedAloneNoCommit++
			} else {
				corroboratedWithCompetitorNoCommit++
			}
		}
		top := resolution.Candidates[0]
		if len(top.MatchMechanisms) == 1 && top.MatchMechanisms[0] == contextfabric.MatchVector {
			vectorOnlyTopCandidate++
		}
	}
	t.Logf("CHAOS-3838 diagnostic (n=%d): committed=%d no_candidates_at_all=%d candidates_no_commit=%d corroborated_but_no_commit=%d corroborated_alone_no_commit=%d corroborated_with_competitor_no_commit=%d vector_only_top_candidate=%d retrieval_degraded=%d",
		len(corpus), committed, noCandidatesAtAll, candidatesNoCommit, corroboratedNoCommit, corroboratedAloneNoCommit, corroboratedWithCompetitorNoCommit, vectorOnlyTopCandidate, retrievalDegraded)
}
