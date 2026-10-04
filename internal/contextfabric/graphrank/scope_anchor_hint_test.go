package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// hintScopeAnchorBackend is the scope-anchor backend with the repository also
// resolvable by its exact canonical id, which is what an exact SubjectHint does.
func hintScopeAnchorBackend(authorization interface{}) *fakeGraphBackend {
	backend := scopeAnchorBackendFor(true, -1, authorization)
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID}
	node := backend.searchResults[scopeAnchorRepo][0]
	backend.exactHints[SubjectKey(repo)] = node
	return backend
}

func resolveWithRepositoryHint(t *testing.T, backend *fakeGraphBackend, principal storage.Principal, question string, frame *contextfabric.QuestionFrame) (contextfabric.SubjectResolution, *captureResolutionTracer, int) {
	t.Helper()
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	calls := 0
	deps.CensusFunc = scopeAnchorCensus(&calls)
	request := testRequest()
	request.Question = question
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: contextfabric.SubjectRepository, ID: scopeAnchorRepoID, Label: scopeAnchorRepo, Source: "workbench"},
	}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted(scopeAnchorRepo, "pull request 747"), deps, nil, nil, frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return resolution, tracer, calls
}

func TestResolveSubjects_ExactRepositoryHintDoesNotShadowTheNamedHandle(t *testing.T) {
	t.Parallel()
	resolution, tracer, _ := resolveWithRepositoryHint(t, hintScopeAnchorBackend("*"), storage.Principal{OrgID: "org_1"}, scopeAnchorQuestion, namedScopeAnchorFrame(nil))
	ids := scopeAnchorCommittedIDs(resolution)
	if len(ids) != 2 || ids[0] != scopeAnchorRepoID || ids[1] != scopeAnchorPRID {
		t.Fatalf("committed = %v, want the hinted repository then the census-attested pull request", ids)
	}
	var commits int
	for _, e := range tracer.eventsForStage("decision") {
		if e.Outcome == "committed" && e.Subject.CanonicalID == scopeAnchorPRID && e.CommitGate == "evidence_census" && e.CommitBasis == string(contextfabric.CommitBasisStatistical) {
			commits++
		}
	}
	if commits != 1 {
		t.Fatalf("evidence_census decision events for the pull request = %d, want 1", commits)
	}
}

func TestResolveSubjects_ExactRepositoryHintWithoutAHandleKeepsTheRepositoryAlone(t *testing.T) {
	t.Parallel()
	resolution, _, calls := resolveWithRepositoryHint(t, hintScopeAnchorBackend("*"), storage.Principal{OrgID: "org_1"}, "How is full-chaos/dev-health-acr doing over the last 30 days?", namedScopeAnchorFrame(nil))
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want the repository alone", ids)
	}
	if calls != 0 {
		t.Fatalf("census calls = %d, want 0 without a handle in the question", calls)
	}
}

func TestResolveSubjects_ExactRepositoryHintWithACohortFrameKeepsTheRepositoryAlone(t *testing.T) {
	t.Parallel()
	frame := &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionOrganizationScope},
	}
	resolution, _, calls := resolveWithRepositoryHint(t, hintScopeAnchorBackend("*"), storage.Principal{OrgID: "org_1"}, "How many pull requests does full-chaos/dev-health-acr have, for example pull request 747?", frame)
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want the repository alone: the handle is an example", ids)
	}
	_ = calls // the observation-only round may still run its census; what matters is that nothing is committed from it
}

func TestResolveSubjects_ExactRepositoryHintForARestrictedPrincipalNeverRunsTheHandleCensus(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{scopeAnchorRepo}}
	resolution, _, calls := resolveWithRepositoryHint(t, hintScopeAnchorBackend([]string{scopeAnchorRepo}), principal, scopeAnchorQuestion, namedScopeAnchorFrame(nil))
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want the hinted repository alone", ids)
	}
	if calls != 0 {
		t.Fatalf("census calls = %d, want 0 for a repository-restricted principal", calls)
	}
}

func TestResolveSubjects_TwoExactHintsDoNotBorrowAnAnchorForTheHandle(t *testing.T) {
	t.Parallel()
	backend := hintScopeAnchorBackend("*")
	otherRepo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-2"}
	backend.exactHints[SubjectKey(otherRepo)] = aliasCandidateNode(contextfabric.SubjectRepository, otherRepo.CanonicalID, "other/repo", -1, []string{"other/repo"}, nil, true)
	deps := backend.deps()
	calls := 0
	deps.CensusFunc = scopeAnchorCensus(&calls)
	request := testRequest()
	request.Question = scopeAnchorQuestion
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: contextfabric.SubjectRepository, ID: "repository:r-2", Label: "other/repo", Source: "workbench"},
		{Kind: contextfabric.SubjectRepository, ID: scopeAnchorRepoID, Label: scopeAnchorRepo, Source: "workbench"},
	}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(scopeAnchorRepo, "pull request 747"), deps, nil, nil, namedScopeAnchorFrame(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range scopeAnchorCommittedIDs(resolution) {
		if id == scopeAnchorPRID {
			t.Fatalf("committed = %v, want the pull request left out: two hinted anchors leave the handle without one anchor", scopeAnchorCommittedIDs(resolution))
		}
	}
}

func TestResolveSubjects_ExactRepositoryHintWithAnAnchorReceiptAndACohortFrameKeepsTheRepositoryAlone(t *testing.T) {
	t.Parallel()
	deps := hintScopeAnchorBackend("*").deps()
	calls := 0
	deps.CensusFunc = scopeAnchorCensus(&calls)
	request := testRequest()
	request.Question = "How many pull requests does full-chaos/dev-health-acr have, for example pull request 747?"
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: contextfabric.SubjectRepository, ID: scopeAnchorRepoID, Label: scopeAnchorRepo, Source: "workbench"},
	}
	frame := &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionOrganizationScope},
	}
	receipt := &contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectRepository, CanonicalID: scopeAnchorRepoID}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(scopeAnchorRepo), deps, nil, receipt, frame, "")
	if err != nil {
		t.Fatal(err)
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != scopeAnchorRepoID {
		t.Fatalf("committed = %v, want the repository alone: in a cohort question the handle is an example", ids)
	}
}
