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
	return scopeAnchorBackendFor(searchTruncated, repoRelevance, "*")
}

func scopeAnchorBackendFor(searchTruncated bool, repoRelevance float64, authorization interface{}) *fakeGraphBackend {
	repoNode := aliasCandidateNode(contextfabric.SubjectRepository, scopeAnchorRepoID, scopeAnchorRepo, repoRelevance, []string{scopeAnchorRepo}, nil, true)
	repoNode.Attributes["authorization_repositories"] = authorization
	prNode := candidateNode(contextfabric.SubjectPullRequest, scopeAnchorPRID, "PR #747", 0.5, authorization)
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
	return func(_ context.Context, _ string, kind CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorID string, anchorBound bool) (CensusOutcome, error) {
		*calls++
		if kind == contextfabric.SubjectPullRequest && handleBound && handleValue == "747" && anchorBound && anchorKind == contextfabric.SubjectRepository && anchorID == scopeAnchorRepoID {
			return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: scopeAnchorPRID}, nil
		}
		return CensusOutcome{}, nil
	}
}

func resolveScopeAnchorQuestion(t *testing.T, backend *fakeGraphBackend, question string) (contextfabric.SubjectResolution, *captureResolutionTracer, int) {
	t.Helper()
	return resolveScopeAnchorQuestionWith(t, backend, question, namedScopeAnchorFrame(nil), 0)
}

func namedScopeAnchorFrame(expected *contextfabric.SubjectKind) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{"pull request 747"}, ExpectedKind: expected},
		},
	}
}

func resolveScopeAnchorQuestionWith(t *testing.T, backend *fakeGraphBackend, question string, frame *contextfabric.QuestionFrame, maxCandidates int) (contextfabric.SubjectResolution, *captureResolutionTracer, int) {
	t.Helper()
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	calls := 0
	deps.CensusFunc = scopeAnchorCensus(&calls)
	request := testRequest()
	request.Question = question
	if maxCandidates > 0 {
		request.Options.MaxSubjectCandidates = maxCandidates
	}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(scopeAnchorRepo, "pull request 747"), deps, nil, nil, frame, "")
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
	var decisionCommits int
	for _, e := range tracer.eventsForStage("decision") {
		if e.Outcome == "committed" && e.Subject.CanonicalID == scopeAnchorPRID && e.CommitGate == "evidence_census" && e.CommitBasis == string(contextfabric.CommitBasisStatistical) && e.Index == 1 && e.Total == 1 && e.Pass >= 2 {
			decisionCommits++
		}
	}
	if decisionCommits != 1 {
		t.Fatalf("decision events for the pull request = %d, want exactly 1 evidence_census statistical commit", decisionCommits)
	}
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
	if att.Outcome != ShadowWouldClarify || att.SurvivorExcludedReason != "" {
		t.Fatalf("attestation = %#v, want would_clarify: a repository that is not the bound anchor is a real rival", att)
	}
	input.PooledSubjects = append(input.PooledSubjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID})
	if att := RunShadowEvidenceRound(context.Background(), input, nil); att.Outcome != ShadowWouldClarify {
		t.Fatalf("attestation = %#v, want would_clarify while any other non-censused candidate is pooled", att)
	}
}

func TestResolveSubjects_CohortFrameBindingAHandleKeepsTheScopeAnchorAlone(t *testing.T) {
	t.Parallel()
	backend := scopeAnchorBackend(true, -1)
	deps := backend.deps()
	var calls int
	deps.CensusFunc = scopeAnchorCensus(&calls)
	request := testRequest()
	request.Question = "How many pull requests does full-chaos/dev-health-acr have, for example pull request 747?"
	frame := &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionOrganizationScope},
	}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(scopeAnchorRepo), deps, nil, nil, frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	for _, id := range scopeAnchorCommittedIDs(resolution) {
		if id == scopeAnchorPRID {
			t.Fatalf("committed = %v, want the pull request left out: in a cohort question the handle is an example, not the subject", scopeAnchorCommittedIDs(resolution))
		}
	}
}

func TestAppendCensusAttestedCommitHonoursTheCandidateCap(t *testing.T) {
	t.Parallel()
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-2"}
	pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: scopeAnchorPRID}
	resolution := contextfabric.SubjectResolution{
		Candidates: []contextfabric.SubjectCandidate{
			{Subject: repo, State: contextfabric.ResolutionCommitted},
			{Subject: other, State: contextfabric.ResolutionProposed},
		},
		Committed: []contextfabric.SubjectRef{repo},
	}
	bases := contextfabric.CommitBasisSet{}
	digests := contextfabric.CommitDecisionDigestSet{}
	if !appendCensusAttestedCommit(&resolution, contextfabric.SubjectCandidate{Subject: pr}, 2, bases, digests, true, true) {
		t.Fatal("append = false, want the pull request committed by displacing the uncommitted candidate")
	}
	if len(resolution.Candidates) != 2 || len(resolution.Committed) != 2 {
		t.Fatalf("candidates = %d, committed = %d, want 2 and 2 under a cap of 2", len(resolution.Candidates), len(resolution.Committed))
	}
	if resolution.Candidates[0].Subject != repo || resolution.Candidates[1].Subject != pr || resolution.Candidates[1].State != contextfabric.ResolutionCommitted {
		t.Fatalf("candidates = %#v, want the committed repository kept and the pull request in place of the uncommitted candidate", resolution.Candidates)
	}
	full := contextfabric.SubjectResolution{
		Candidates: []contextfabric.SubjectCandidate{{Subject: repo, State: contextfabric.ResolutionCommitted}},
		Committed:  []contextfabric.SubjectRef{repo},
	}
	if appendCensusAttestedCommit(&full, contextfabric.SubjectCandidate{Subject: pr}, 1, bases, digests, true, true) || len(full.Candidates) != 1 {
		t.Fatalf("append over a cap of committed-only candidates must refuse, got %#v", full)
	}
}

func TestResolveSubjects_NamedRepositoryFrameKeepsTheScopeAnchorAlone(t *testing.T) {
	t.Parallel()
	repository := contextfabric.SubjectRepository
	resolution, _, _ := resolveScopeAnchorQuestionWith(t, scopeAnchorBackend(true, -1),
		"How is full-chaos/dev-health-acr doing over the last 30 days? Use pull request 747 as an example.", namedScopeAnchorFrame(&repository), 0)
	ids := scopeAnchorCommittedIDs(resolution)
	if len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want only the repository: the frame says the repository is the subject", ids)
	}
}

func TestResolveSubjects_CandidateCapRefusalIsRecordedAsADecision(t *testing.T) {
	t.Parallel()
	resolution, tracer, _ := resolveScopeAnchorQuestionWith(t, scopeAnchorBackend(true, -1), scopeAnchorQuestion, namedScopeAnchorFrame(nil), 1)
	ids := scopeAnchorCommittedIDs(resolution)
	if len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want the repository alone under a cap of one", ids)
	}
	var refused int
	for _, e := range tracer.eventsForStage("decision") {
		if e.Outcome == "no_commit" && e.CommitGate == "evidence_census" && e.Subject.CanonicalID == scopeAnchorPRID {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("no_commit evidence_census decision events for the pull request = %d, want 1", refused)
	}
}

func TestResolveSubjects_RepositoryNarrowedRequestStillAttestsTheNamedHandle(t *testing.T) {
	t.Parallel()
	backend := scopeAnchorBackend(true, -1)
	deps := backend.deps()
	var calls int
	deps.CensusFunc = scopeAnchorCensus(&calls)
	request := testRequest()
	request.Question = scopeAnchorQuestion
	request.RequestedScope.RepositorySlugs = []string{scopeAnchorRepo}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted(scopeAnchorRepo, "pull request 747"), deps, nil, nil, namedScopeAnchorFrame(nil), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	ids := scopeAnchorCommittedIDs(resolution)
	if len(ids) != 2 || ids[1] != scopeAnchorPRID {
		t.Fatalf("committed = %v, want the repository then the pull request: a caller's repository narrowing of an unrestricted principal hides nothing", ids)
	}
}

func TestResolveSubjects_RestrictedPrincipalNeverRunsTheHandleCensus(t *testing.T) {
	t.Parallel()
	backend := scopeAnchorBackendFor(true, -1, []string{scopeAnchorRepo})
	deps := backend.deps()
	var calls int
	deps.CensusFunc = scopeAnchorCensus(&calls)
	request := testRequest()
	request.Question = scopeAnchorQuestion
	request.RequestedScope.RepositorySlugs = []string{scopeAnchorRepo}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{scopeAnchorRepo}}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted(scopeAnchorRepo, "pull request 747"), deps, nil, nil, namedScopeAnchorFrame(nil), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want the repository committed so the trigger is reachable", ids)
	}
	if calls != 0 {
		t.Fatalf("census calls = %d, want 0 for a repository-restricted principal", calls)
	}
	for _, id := range scopeAnchorCommittedIDs(resolution) {
		if id == scopeAnchorPRID {
			t.Fatalf("committed = %v, want the pull request left out", scopeAnchorCommittedIDs(resolution))
		}
	}
}

func TestResolveSubjects_ScopeAnchorRoundPanicRecoveryTagsTheTrigger(t *testing.T) {
	t.Parallel()
	backend := scopeAnchorBackend(true, -1)
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		panic("simulated CensusFunc panic on the scope-anchor round")
	}
	request := testRequest()
	request.Question = scopeAnchorQuestion
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(scopeAnchorRepo, "pull request 747"), deps, nil, nil, namedScopeAnchorFrame(nil), "")
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v, want the panic isolated", err)
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want the scope anchor alone after a recovered panic", ids)
	}
	events := tracer.eventsForStage("evidence_round")
	if len(events) != 1 || events[0].ShadowReason != string(ReasonProbeError) {
		t.Fatalf("evidence_round events = %#v, want exactly 1 recovered probe_error event", events)
	}
	if events[0].ShadowTrigger != "committed_scope_anchor" {
		t.Fatalf("recovered event ShadowTrigger = %q, want %q", events[0].ShadowTrigger, "committed_scope_anchor")
	}
}
