package graphrank

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	scopeAnchorRepoID = "repository:r-1"
	scopeAnchorPRID   = "pull_request:r-1:747"
	scopeAnchorRepo   = "full-chaos/dev-health-acr"
)

func scopeAnchorBackend(searchTruncated bool, repoRelevance float64) *fakeGraphBackend {
	repoNode := aliasCandidateNode(contextfabric.SubjectRepository, scopeAnchorRepoID, scopeAnchorRepo, repoRelevance, []string{scopeAnchorRepo}, nil, true)
	prNode := candidateNode(contextfabric.SubjectPullRequest, scopeAnchorPRID, "PR #747", 0.5, "*")
	return &fakeGraphBackend{
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{scopeAnchorRepo: {repoNode}},
		aliasLookupComplete:  true,
		searchResults:        map[string][]CandidateNode{scopeAnchorRepo: {repoNode}},
		searchTruncated:      searchTruncated,
		exactHints: map[string]CandidateNode{
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: scopeAnchorPRID}): prNode,
		},
	}
}

func scopeAnchorCensus(calls *int) CensusFunc {
	return func(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
		*calls++
		if kind == contextfabric.SubjectPullRequest {
			return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: scopeAnchorPRID}, nil
		}
		return CensusOutcome{}, nil
	}
}

func resolveScopeAnchorQuestion(t *testing.T, backend *fakeGraphBackend, question string) (contextfabric.SubjectResolution, *captureResolutionTracer, int) {
	t.Helper()
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	calls := 0
	deps.CensusFunc = scopeAnchorCensus(&calls)
	request := testRequest()
	request.Question = question
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(scopeAnchorRepo, "pull request 747"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	return resolution, tracer, calls
}

func scopeAnchorCommittedIDs(r contextfabric.SubjectResolution) []string {
	var ids []string
	for _, s := range r.Committed {
		ids = append(ids, s.CanonicalID)
	}
	return ids
}

const scopeAnchorQuestion = "What is the state of pull request 747 in full-chaos/dev-health-acr over the last 30 days?"

func TestResolveSubjects_CommittedScopeAnchorDoesNotShadowTheNamedHandle(t *testing.T) {
	t.Parallel()
	resolution, tracer, _ := resolveScopeAnchorQuestion(t, scopeAnchorBackend(true, -1), scopeAnchorQuestion)
	ids := scopeAnchorCommittedIDs(resolution)
	if len(ids) != 2 || ids[0] != scopeAnchorRepoID || ids[1] != scopeAnchorPRID {
		t.Fatalf("committed = %v, want the scope anchor then the census-attested pull request", ids)
	}
	rounds := tracer.eventsForStage("evidence_round")
	if len(rounds) != 1 || rounds[0].ShadowTrigger != "committed_scope_anchor" || rounds[0].ShadowSurvivorExcludedReason != "scope_anchor" || rounds[0].ShadowOutcome != string(ShadowWouldCommit) {
		t.Fatalf("evidence_round events = %#v, want one committed_scope_anchor would_commit with survivor_excluded_reason=scope_anchor", rounds)
	}
}

func TestRunShadowEvidenceRound_ScopeAnchorIsNotASurvivorOfTheHandleCensus(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = scopeAnchorQuestion
	input.PooledKinds = []CensusKind{contextfabric.SubjectRepository}
	input.PooledSubjects = []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID}}
	input.AliasClaimants = map[string][]IdentityMatch{
		scopeAnchorRepo: {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID}, Mechanism: contextfabric.MatchAlias}},
	}
	var calls int
	input.CensusFunc = scopeAnchorCensus(&calls)
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome != ShadowWouldCommit || att.SurvivorExcludedReason != "scope_anchor" {
		t.Fatalf("attestation = %#v, want would_commit with survivor_excluded_reason=scope_anchor", att)
	}
}

func TestResolveSubjects_NoHandleInTheQuestionKeepsTheScopeAnchorAlone(t *testing.T) {
	t.Parallel()
	resolution, tracer, calls := resolveScopeAnchorQuestion(t, scopeAnchorBackend(true, -1), "How is full-chaos/dev-health-acr doing over the last 30 days?")
	ids := scopeAnchorCommittedIDs(resolution)
	if len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want only the repository", ids)
	}
	if calls != 0 || len(tracer.eventsForStage("evidence_round")) != 0 {
		t.Fatalf("census calls = %d, round events = %d, want the round not to run", calls, len(tracer.eventsForStage("evidence_round")))
	}
}

func TestRunShadowEvidenceRound_RealRivalNonCensusedCandidateStillClarifies(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = scopeAnchorQuestion
	input.PooledKinds = []CensusKind{contextfabric.SubjectRepository}
	input.PooledSubjects = []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:other"}}
	input.AliasClaimants = map[string][]IdentityMatch{
		scopeAnchorRepo: {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID}, Mechanism: contextfabric.MatchAlias}},
	}
	var calls int
	input.CensusFunc = scopeAnchorCensus(&calls)
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome == ShadowWouldCommit || att.SurvivorExcludedReason != "" {
		t.Fatalf("attestation = %#v, want would_clarify: a repository that is not the bound anchor is a real rival", att)
	}
	input.PooledSubjects = append(input.PooledSubjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID})
	if att := RunShadowEvidenceRound(context.Background(), input, nil); att.Outcome == ShadowWouldCommit {
		t.Fatalf("attestation = %#v, want would_clarify while any other non-censused candidate is pooled", att)
	}
}
