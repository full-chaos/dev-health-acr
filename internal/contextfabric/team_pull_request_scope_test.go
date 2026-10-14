package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var teamScopePullRequest = SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:github:api:1", Label: "PR #1"}

func teamPullRequestCapabilities() map[FactKind]FactCapability {
	return map[FactKind]FactCapability{
		FactPullRequests: planCapability(FactPullRequests, "pull_requests", SubjectTeam, SubjectProject, SubjectPullRequest),
	}
}

func TestTeamPullRequestsExpandOnlyWhenPerPullRequestFactsAreAsked(t *testing.T) {
	cases := []struct {
		name      string
		subject   SubjectRef
		perPR     bool
		wantCalls int
	}{
		{"team, default read: rollup stands alone", scopeTeam, false, 0},
		{"team, per pull request facts asked", scopeTeam, true, 1},
		{"project, default read keeps its additive expansion", scopeProject, false, 1},
	}
	for _, c := range cases {
		expander := &recordingScopeExpander{targets: []SubjectRef{teamScopePullRequest}}
		request := scopeRequest([]SubjectRef{c.subject}, []FactRequirement{{Kind: FactPullRequests}}, TemporalCurrent)
		request.PullRequestFacts = c.perPR
		scope := NewFactReadScopeResolver(expander).Resolve(
			context.Background(), storage.Principal{OrgID: "org_1"},
			newFactScopeResolveInput(request), teamPullRequestCapabilities(),
		)
		if expander.calls != c.wantCalls {
			t.Fatalf("%s: expander calls = %d, want %d (events %+v)", c.name, expander.calls, c.wantCalls, scope.Events)
		}
		if c.wantCalls == 0 && len(scope.DerivedSubjects) != 0 {
			t.Fatalf("%s: derived subjects admitted: %+v", c.name, scope.DerivedSubjects)
		}
	}
}

func TestPullRequestFactsWantedByFrame(t *testing.T) {
	if pullRequestFactsWanted(nil) {
		t.Fatal("no frame must not ask for per pull request facts")
	}
	if pullRequestFactsWanted(&QuestionFrame{}) {
		t.Fatal("a frame with no member kind must not ask for per pull request facts")
	}
	status := pullRequestStatusQuestionFrame(PullRequestStatusOpen)
	if !pullRequestFactsWanted(&status) {
		t.Fatal("a pull request status filter must ask for per pull request facts")
	}
	members := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: SubjectPullRequest},
		},
	}
	if !pullRequestFactsWanted(&members) {
		t.Fatal("pull requests as the member kind must ask for per pull request facts")
	}
	repositories := members
	repositories.SubjectExpression.Scoped = &ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: SubjectRepository}
	if pullRequestFactsWanted(&repositories) {
		t.Fatal("repository members must not ask for per pull request facts")
	}
}

// runTeamPullRequestEngine drives the real engine on a team subject with the
// pull requests requirement and returns the fact request it sent to the
// fact reader.
func runTeamPullRequestEngine(t *testing.T, frame *QuestionFrame) CanonicalFactRequest {
	t.Helper()
	graph := &anchorKindCapturingGraph{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{scopeTeam}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			EvidenceRefIDs: []string{},
			Coverage:       Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	interpreted := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "pull requests", TimeContext: TimeContext{Axis: TemporalCurrent},
		SubjectTerms: []string{"platform"}, FactRequirements: []FactRequirement{{Kind: FactPullRequests}},
	}
	var captured []CanonicalFactRequest
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: interpreted, outcome: anchorGateOutcome(frame, SubjectTeam)},
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			captured = append(captured, request)
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "ok",
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			}, nil
		}),
		Results: &resultStoreStub{},
	}, EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"},
		InvestigationRequest{
			SchemaVersion: InvestigationRequestSchemaV1, RequestID: "request_12345678",
			Question: "pull requests of the platform team", TimeContext: TimeContext{Axis: TemporalCurrent},
			Options:  InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer: ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		})
	if len(captured) == 0 {
		t.Fatal("the engine never read facts; this test proves nothing")
	}
	return captured[0]
}

// expandedPullRequestCalls runs the production scope resolver, with the
// default policy table, over the request the engine sent.
func expandedPullRequestCalls(request CanonicalFactRequest) int {
	expander := &recordingScopeExpander{targets: []SubjectRef{teamScopePullRequest}}
	NewFactReadScopeResolver(expander).Resolve(
		context.Background(), storage.Principal{OrgID: "org_1"},
		newFactScopeResolveInput(request), teamPullRequestCapabilities(),
	)
	return expander.calls
}

func teamQuestionFrame(memberKind SubjectKind, qualifier MemberQualifier, value string) *QuestionFrame {
	return &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: memberKind, MemberQualifier: qualifier, MemberQualifierValue: value},
		},
	}
}

func TestEngineTeamPullRequestQuestionsKeepPerPullRequestFactsOnlyWhenTheyAskForThem(t *testing.T) {
	cases := []struct {
		name      string
		frame     *QuestionFrame
		wantFacts bool
	}{
		{"which pull requests of the team are open (status)", teamQuestionFrame(SubjectPullRequest, MemberQualifierStatus, PullRequestStatusOpen), true},
		{"list the pull requests of the team (members)", teamQuestionFrame(SubjectPullRequest, "", ""), true},
		{"how are the team's repositories doing on pull requests (aggregate)", teamQuestionFrame(SubjectRepository, "", ""), false},
	}
	for _, c := range cases {
		request := runTeamPullRequestEngine(t, c.frame)
		if request.PullRequestFacts != c.wantFacts {
			t.Fatalf("%s: PullRequestFacts = %v, want %v", c.name, request.PullRequestFacts, c.wantFacts)
		}
		calls := expandedPullRequestCalls(request)
		if (calls > 0) != c.wantFacts {
			t.Fatalf("%s: scope expansion ran %d time(s), want expansion=%v", c.name, calls, c.wantFacts)
		}
	}
}
