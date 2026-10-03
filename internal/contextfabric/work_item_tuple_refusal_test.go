package contextfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemTupleRefusalLeavesNoStaleResolutionState(t *testing.T) {
	for _, name := range []string{"team", "organization", "multiple"} {
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
			graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution, bases: provenCommitBases(resolution.Committed...)}}
			engine := mustReuseTestEngine(t, EngineDependencies{Interpreter: familyInterpreter{interpreted: question, outcome: outcome}, Graph: graph,
				CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
					return true, ""
				},
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return InvestigationResult{}, errors.New("a refused tuple must not reach synthesis")
				}),
			})
			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
			if err != nil {
				t.Fatalf("a refused tuple anchor must end on a valid terminal result, got %v", err)
			}
			if !result.SubjectResolution.RetrievalDegraded {
				t.Fatalf("a refusal must keep the resolution's retrieval-degraded marker: %+v", result.SubjectResolution)
			}
			if len(result.SubjectResolution.Committed) != 0 || len(result.SubjectResolution.Candidates) != 0 || len(result.SubjectResolution.CommitDecisionDigests) != 0 {
				t.Fatalf("a refused tuple anchor must leave no committed subject, candidate or digest: %+v", result.SubjectResolution)
			}
		})
	}
}
