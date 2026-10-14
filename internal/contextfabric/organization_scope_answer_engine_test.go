package contextfabric

import (
	"context"
	"fmt"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// organizationMemberGraph discovers a member set only for a frame the cohort
// predicate admits, the way the falkorgraph reader does, so a frame no arm can
// build a cohort for reaches the engine with no subject.
type organizationMemberGraph struct {
	retrievalRecordingGraph
	discovered []*QuestionFrame
}

func (g *organizationMemberGraph) DiscoverContext(_ context.Context, _ storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	g.discovered = append(g.discovered, request.Frame)
	graph := g.context
	graph.Cohort = nil
	if request.Frame != nil {
		if kind, _, reason := CohortMemberKindForFrame(*request.Frame); reason == CohortDiscoverable {
			graph.Cohort = organizationMembers(kind, 3)
		}
	}
	return graph, nil
}

func organizationMembers(kind SubjectKind, size int) *Cohort {
	members := make([]CohortMember, 0, size)
	for index := 0; index < size; index++ {
		label := fmt.Sprintf("Member %c", rune('A'+index))
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: kind, CanonicalID: fmt.Sprintf("%s:org_member_%d", kind, index), Label: label},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	return &Cohort{Kind: kind, Rationale: "scope census match", Members: members, Complete: true}
}

type organizationAnswerRun struct {
	result   InvestigationResult
	graph    *organizationMemberGraph
	subjects [][]SubjectRef
}

// runOrganizationQuestion drives Engine.Investigate through the production
// interpreter for one organization-scope proposal and returns the SERVED
// result with what retrieval and the fact read received.
func runOrganizationQuestion(t *testing.T, receipt ModelExecutionReceipt, proposal QuestionFrame, facts []FactRequirement, allowClarification bool) organizationAnswerRun {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt.QuestionFrame = &proposal
	graph := &organizationMemberGraph{retrievalRecordingGraph: retrievalRecordingGraph{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}}}
	run := organizationAnswerRun{graph: graph}
	readFacts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		run.subjects = append(run.subjects, append([]SubjectRef(nil), request.Subjects...))
		return CanonicalFactBundle{
			Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
		}, nil
	})
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "the current state",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: facts,
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: interpretation, receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   registryDeriver{},
		},
		Graph: graph,
		Facts: readFacts,
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Assessed.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Assessed.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &staticResultStore{results: map[string]InvestigationResult{}},
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_organization_answer_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_organization_answer_01"
	request.Options.AllowClarification = allowClarification
	run.result, err = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_role"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return run
}

func organizationProposal(member *SubjectKind, goals ...InvestigationGoal) QuestionFrame {
	return QuestionFrame{
		Goals:             goals,
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{MemberKind: member}},
		Temporal:          TemporalIntentCurrent,
	}
}

// TestOrganizationScopeQuestionsAreAnsweredOverTheOrganizationsMembers drives
// the corpus rows the former organization-scope refusal named (ids only)
// through the production interpreter and the engine: an organization-scope
// question that counts nothing is answered as a survey of the organization's
// own members -- the frame's servable kind, else the requested kind, else
// teams -- and is never refused. A counting question keeps the member census.
func TestOrganizationScopeQuestionsAreAnsweredOverTheOrganizationsMembers(t *testing.T) {
	organization := SubjectOrganization
	project := SubjectProject
	health := []FactRequirement{{Kind: FactHealth}}
	for _, testCase := range []struct {
		cell               string
		requested          SubjectKind
		proposal           QuestionFrame
		allowClarification bool
		wantExpression     SubjectExpressionKind
		wantMember         SubjectKind
	}{
		{
			// corpus row cv-b5-org-health, second turn.
			cell: "cv-b5-org-health: organization member kind", requested: SubjectOrganization,
			proposal: organizationProposal(&organization, GoalAssessState), allowClarification: true,
			wantExpression: SubjectExpressionDiscoveredKind, wantMember: SubjectTeam,
		},
		{
			// corpus row cv-b5-org-health, first turn.
			cell:     "cv-b5-org-health: no member kind",
			proposal: organizationProposal(nil, GoalAssessState), allowClarification: true,
			wantExpression: SubjectExpressionDiscoveredKind, wantMember: SubjectTeam,
		},
		{
			// corpus row cv-c7-org-drivers.
			cell:     "cv-c7-org-drivers: a drivers question",
			proposal: organizationProposal(nil, GoalExplainDrivers), allowClarification: true,
			wantExpression: SubjectExpressionDiscoveredKind, wantMember: SubjectTeam,
		},
		{
			cell:     "a caller that declined clarification is answered the same way",
			proposal: organizationProposal(nil, GoalAssessState), allowClarification: false,
			wantExpression: SubjectExpressionDiscoveredKind, wantMember: SubjectTeam,
		},
		{
			cell:     "a count beside an assessment surveys the counted kind",
			proposal: organizationProposal(&project, GoalCountOrAggregate, GoalAssessState), allowClarification: true,
			wantExpression: SubjectExpressionDiscoveredKind, wantMember: SubjectProject,
		},
		{
			cell: "the requested kind decides when the frame names none", requested: SubjectRepository,
			proposal: organizationProposal(nil, GoalAssessState, GoalExplainDrivers), allowClarification: true,
			wantExpression: SubjectExpressionDiscoveredKind, wantMember: SubjectRepository,
		},
		{
			// The control: a counting question keeps the organization-scope
			// member census it is served by today.
			cell:     "a counting question keeps the member census",
			proposal: organizationProposal(&project, GoalCountOrAggregate), allowClarification: true,
			wantExpression: SubjectExpressionOrganizationScope, wantMember: SubjectProject,
		},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			receipt := validModelReceiptFixture(ModelOperationInterpret)
			receipt.RequestedSubjectKind = testCase.requested
			run := runOrganizationQuestion(t, receipt, testCase.proposal, health, testCase.allowClarification)
			result := run.result
			t.Logf("served status=%s cohort_kind=%s", result.Status, testCase.wantMember)
			if result.Status == InvestigationNoMatch || result.Status == InvestigationClarificationRequired || result.RefusalBasis != "" {
				t.Fatalf("status/basis = %q/%q, want an answered turn", result.Status, result.RefusalBasis)
			}
			if roleContains(result.Limitations, contractsv1.ContextFabricOrganizationScopeUnsupportedLimitation) {
				t.Fatalf("limitations = %#v carry the retired organization-scope refusal", result.Limitations)
			}
			if len(run.graph.discovered) == 0 {
				t.Fatal("discovery was never called")
			}
			for call, frame := range run.graph.discovered {
				if frame == nil || frame.SubjectExpression.Kind != testCase.wantExpression {
					t.Fatalf("discovery call %d received %+v, want a %s frame", call, frame, testCase.wantExpression)
				}
				if kind, ok := frame.SubjectExpression.MemberKind(); !ok || kind != testCase.wantMember {
					t.Fatalf("discovery call %d member kind = %q, want %q", call, kind, testCase.wantMember)
				}
			}
			if len(run.subjects) == 0 {
				t.Fatal("no fact read ran for the answered turn")
			}
			for _, subject := range run.subjects[0] {
				if subject.Kind != testCase.wantMember {
					t.Fatalf("fact read subjects = %+v, want the %s members", run.subjects[0], testCase.wantMember)
				}
			}
			if result.Cohort == nil || result.Cohort.Kind != testCase.wantMember || len(result.Cohort.Members) != 3 {
				t.Fatalf("served cohort = %+v, want the three %s members", result.Cohort, testCase.wantMember)
			}
		})
	}
}

// TestAnOrganizationInvestmentQuestionReadsTheOrganizationSubject holds that
// "where is our effort going" reads the organization's own investment fact:
// the organization subject, never a refusal and never a sum over members.
func TestAnOrganizationInvestmentQuestionReadsTheOrganizationSubject(t *testing.T) {
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	run := runOrganizationQuestion(t, receipt, organizationProposal(nil, GoalAllocateInvestment), []FactRequirement{{Kind: FactInvestment}}, true)
	result := run.result
	if result.Status == InvestigationNoMatch || result.Status == InvestigationClarificationRequired || result.RefusalBasis != "" {
		t.Fatalf("status/basis = %q/%q, want an answered turn", result.Status, result.RefusalBasis)
	}
	for call, frame := range run.graph.discovered {
		if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionOrganizationScope {
			t.Fatalf("discovery call %d received %+v, want the organization-scope frame", call, frame)
		}
	}
	want := SubjectRef{Kind: SubjectOrganization, CanonicalID: "organization:org_role", Label: "org_role"}
	if len(run.subjects) == 0 || len(run.subjects[0]) != 1 || run.subjects[0][0] != want {
		t.Fatalf("fact read subjects = %+v, want only %+v", run.subjects, want)
	}
}
