package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// repositoryTupleReuseFixture is the project reuse fixture re-anchored on a
// repository: the stored answer, its reading and its census name the
// repository.
func repositoryTupleReuseFixture(t *testing.T) (storage.Principal, InvestigationRequest, StoredInvestigationResult, WorkItemMembershipResult) {
	t.Helper()
	principal, request, stored, current := tupleReuseFixture(t)
	request.RequestedScope.ProjectIDs = nil
	anchor := repositoryWorkItemAnchor
	candidate := stored.Result.SubjectResolution.Candidates[0]
	candidate.Subject = anchor
	candidate.MatchedTerms = []string{anchor.Label}
	stored.Result.SubjectResolution.Candidates = []SubjectCandidate{candidate}
	stored.Result.SubjectResolution.Committed = []SubjectRef{anchor}
	stored.Result.SubjectResolution.CommitDecisionDigests = identityProvenDigests(anchor)
	for index := range stored.Result.ClaimedFacts {
		if stored.Result.ClaimedFacts[index].Subject.Kind == SubjectProject {
			stored.Result.ClaimedFacts[index].Subject = anchor
		}
	}
	stored.SemanticState.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectRepository, Term: anchor.Label}
	stored.SemanticState.Frame.SubjectExpression.Scoped.AnchorTerms = []string{anchor.Label}
	return principal, request, stored, current
}

func TestAStoredRepositoryTupleIsReusedOnlyAfterTheRepositoryIsReChecked(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	principal, request, stored, current := repositoryTupleReuseFixture(t)
	if got := ClassifyWorkItemTuple(stored.Result, stored.SemanticState, stored.SemanticStateRead); got.Disposition != WorkItemTupleEligible {
		t.Fatalf("a stored repository tuple classified %s, want eligible", got.Disposition)
	}
	if err := ValidateWorkItemTuplePayload(stored.Result, principal); err != nil {
		t.Fatalf("a stored repository tuple payload is invalid: %v", err)
	}
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
	defer owner.Complete()
	var verifiedKinds []SubjectKind
	var readAnchors []SubjectRef
	engine := mustReuseTestEngine(t, EngineDependencies{ReuseGate: tupleReuseGate{stored}, CandidateVerifier: func(_ context.Context, _ storage.Principal, _ RequestedScope, _ ResolvedGraphBinding, kind SubjectKind, _ string) (bool, CandidateVerificationReason) {
		verifiedKinds = append(verifiedKinds, kind)
		return true, CandidateVerificationValid
	}, WorkItemMembership: tupleMembershipFunc(func(c context.Context, _ storage.Principal, r WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
		readAnchors = append(readAnchors, r.Anchor.Subject)
		lease, err := gate.Acquire(c)
		if err != nil {
			return nil, WorkItemMembershipResult{}, err
		}
		if err := owner.Retain(lease); err != nil {
			return nil, WorkItemMembershipResult{}, err
		}
		return lease, current, nil
	})})
	result, hit, tuple, reuseErr := engine.tryReuse(ctx, principal, request, TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{Epoch: 12})
	if reuseErr != nil {
		t.Fatal(reuseErr)
	}
	if !hit || !tuple || !result.Reused {
		t.Fatalf("hit=%t tuple=%t reused=%t, want a re-measured reuse", hit, tuple, result.Reused)
	}
	if len(verifiedKinds) != 1 || verifiedKinds[0] != SubjectRepository {
		t.Fatalf("the stored anchor was re-checked as %v, want the repository", verifiedKinds)
	}
	if len(readAnchors) != 1 || readAnchors[0] != repositoryWorkItemAnchor {
		t.Fatalf("membership re-read on %v, want the stored repository", readAnchors)
	}
}

func TestATupleWhoseCandidateAndAnchorDifferInKindIsNotATuple(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	_, _, stored, _ := repositoryTupleReuseFixture(t)
	stored.Result.SubjectResolution.Candidates[0].Subject = SubjectRef{Kind: SubjectProject, CanonicalID: repositoryWorkItemAnchor.CanonicalID, Label: repositoryWorkItemAnchor.Label}
	if err := ValidateWorkItemTuplePayload(stored.Result, storage.Principal{OrgID: "org-1"}); err == nil {
		t.Fatal("a payload whose candidate is a project and whose anchor is a repository validated")
	}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	stored.Result.SubjectResolution.Candidates[0].Subject = team
	stored.Result.SubjectResolution.Committed = []SubjectRef{team}
	if err := ValidateWorkItemTuplePayload(stored.Result, storage.Principal{OrgID: "org-1"}); err == nil {
		t.Fatal("a payload anchored on a team validated")
	}
	state := *stored.SemanticState
	state.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectTeam, Term: "Platform"}
	if workItemTupleSemanticState(&state) {
		t.Fatal("a reading anchored on a team classified as a work-item tuple")
	}
}

func TestTheAuthorizationGapNamesTheAnchorItWasReadOn(t *testing.T) {
	census := WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, CappedPopulation: 3, AuthorizedPopulation: 1, DeniedPopulation: 2}
	repository, ok := workItemAuthorizationGapOf(census, SubjectRepository)
	if !ok || !strings.HasPrefix(repository.Limitation(), "Work items exist in this repository that are outside") {
		t.Fatalf("repository gap = %q", repository.Limitation())
	}
	project, ok := workItemAuthorizationGapOf(census, SubjectProject)
	if !ok || !strings.HasPrefix(project.Limitation(), "Work items exist in this project that are outside") {
		t.Fatalf("project gap = %q", project.Limitation())
	}
	if !hasWorkItemAuthorizationGapLimitation([]string{repository.Limitation()}) || !hasWorkItemAuthorizationGapLimitation([]string{project.Limitation()}) {
		t.Fatal("a stored gap disclosure is not recognised for both anchors")
	}
}

func TestTheRepositoryNoMatchSentencesAreServiceAuthored(t *testing.T) {
	window := workItemMemberFilter{AnchorKind: SubjectRepository, Status: "todo", TimeRole: MemberTimeRoleCompleted}
	for _, sentence := range []string{
		workItemNoMatchDisclosure(SubjectRepository, "currently has status todo"),
		workItemMemberFilterNoMatchDisclosure(window),
	} {
		if !strings.Contains(sentence, "this repository") || !strings.Contains(sentence, "the repository's health") {
			t.Fatalf("sentence does not name the repository: %q", sentence)
		}
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("the repository no-match sentence is not recognised as service-authored: %q", sentence)
		}
	}
	if got := workItemNoMatchDisclosure("", "currently has status todo"); got != workItemStatusNoMatchDisclosure("todo") {
		t.Fatalf("the zero anchor kind no longer names the project: %q", got)
	}
}
