package contextfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemTupleRefusalLeavesNoStaleResolutionState(t *testing.T) {
	for _, name := range []string{"team", "organization", "multiple", "unauthorized"} {
		t.Run(name, func(t *testing.T) {
			frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState), nil, "").Frame
			question := InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}}
			outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), question.TimeContext), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
			resolution := workItemTuplePayloadFixture(t).SubjectResolution
			anchor := resolution.Committed[0]
			switch name {
			case "team":
				resolution.Committed[0].Kind = SubjectTeam
			case "organization":
				resolution.Committed[0].Kind = SubjectOrganization
			case "multiple":
				resolution.Committed = append(resolution.Committed, anchor)
			}
			resolution.CommitDecisionDigests = identityProvenDigests(resolution.Committed...)
			resolution.RetrievalDegraded = true
			priorResult := validInvestigationResult()
			priorResult.ResultID = "result_prior_1"
			priorResult.SubjectResolution = SubjectResolution{
				Candidates: []SubjectCandidate{{ReceiptID: "receipt_abc12345", Subject: anchor, State: ResolutionCommitted, MatchReasons: []string{"exact"}, Confidence: 1}},
				Committed:  []SubjectRef{anchor},
			}
			graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution, bases: provenCommitBases(resolution.Committed...)}}
			engine := mustReuseTestEngine(t, EngineDependencies{Interpreter: familyInterpreter{interpreted: question, outcome: outcome}, Graph: graph,
				Results: &staticResultStore{results: map[string]InvestigationResult{"result_prior_1": priorResult}},
				CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
					return name != "unauthorized", ""
				},
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return InvestigationResult{}, errors.New("a refused tuple must not reach synthesis")
				}),
			})
			request := validInvestigationRequestWithConfirmedWindow()
			request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}
			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if err != nil {
				t.Fatalf("a refused tuple anchor must end on a valid terminal result, got %v", err)
			}
			got := result.SubjectResolution
			if len(got.Committed) != 0 || len(got.Candidates) != 0 || len(got.CommitDecisionDigests) != 0 {
				t.Fatalf("a refused tuple anchor must leave no committed subject, candidate or digest: %+v", got)
			}
			if len(got.PriorSubjectReceiptDispositions) != 1 || got.PriorSubjectReceiptDispositions[0].ReceiptID != "receipt_abc12345" {
				t.Fatalf("the supplied receipt must still be disclosed: %+v", got.PriorSubjectReceiptDispositions)
			}
			if !got.RetrievalDegraded || !result.Coverage.Partial {
				t.Fatalf("a degraded retrieval must still read as partial coverage: degraded=%v partial=%v", got.RetrievalDegraded, result.Coverage.Partial)
			}
		})
	}
}
