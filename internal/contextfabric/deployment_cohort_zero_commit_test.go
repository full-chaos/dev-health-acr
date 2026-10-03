package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type zeroCommitRun struct {
	discoverCalls int
	status        contractsv1.ContextFabricInvestigationStatus
	basis         contractsv1.ContextFabricRefusalBasis
	candidates    int
}

func runDeploymentCohortWithNoCommit(t *testing.T, declared SubjectKind, candidateKinds ...SubjectKind) zeroCommitRun {
	t.Helper()
	frame := deploymentScopedFrame(GoalAssessState)
	gate := DecideFrameGate(ValidateFrame(frame, nil, ""), true)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: gate, WinningSample: FamilySample{ScopeAnchorKind: declared, ScopeAnchorTerm: "Anchor"}}
	resolution := workItemTuplePayloadFixture(t).SubjectResolution
	template := resolution.Candidates[0]
	resolution.Committed = []SubjectRef{}
	resolution.CommitDecisionDigests = nil
	resolution.Candidates = []SubjectCandidate{}
	for i, kind := range candidateKinds {
		candidate := template
		candidate.ReceiptID = template.ReceiptID + "_" + string(rune('a'+i))
		candidate.Subject = SubjectRef{Kind: kind, CanonicalID: "anchor-" + string(rune('a'+i)), Label: "Anchor"}
		resolution.Candidates = append(resolution.Candidates, candidate)
	}
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution, bases: provenCommitBases()}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "deployments", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactDeployments}}}, outcome: outcome},
		Graph:       graph,
		Results:     &staticResultStore{},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_anchor_kind_001" }})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
	if runErr != nil {
		t.Fatalf("investigate: %v", runErr)
	}
	return zeroCommitRun{discoverCalls: graph.discoverCalls, status: result.Status, basis: result.RefusalBasis, candidates: len(result.SubjectResolution.Candidates)}
}

func TestZeroCommitDeploymentFrameWithOnlyUnservableAnchorKindsIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name       string
		declared   SubjectKind
		candidates []SubjectKind
	}{
		{"declared project, no candidate", SubjectProject, nil},
		{"declared project, one repository candidate", SubjectProject, []SubjectKind{SubjectRepository}},
		{"declared project, one project candidate", SubjectProject, []SubjectKind{SubjectProject}},
		{"no declared kind, one project candidate", "", []SubjectKind{SubjectProject}},
		{"no declared kind, only unservable candidates", "", []SubjectKind{SubjectProject, SubjectProject}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runDeploymentCohortWithNoCommit(t, tc.declared, tc.candidates...)
			if run.basis != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
				t.Fatalf("refusal basis = %q (status %q), want member_kind_unservable", run.basis, run.status)
			}
			if run.discoverCalls != 0 || run.candidates != 0 {
				t.Fatalf("discover calls = %d, candidates kept = %d, want 0 and 0", run.discoverCalls, run.candidates)
			}
		})
	}
}

func TestZeroCommitDeploymentFrameKeepsItsTerminalWhenAServableKindCouldStillHelp(t *testing.T) {
	for _, tc := range []struct {
		name       string
		declared   SubjectKind
		candidates []SubjectKind
		status     contractsv1.ContextFabricInvestigationStatus
		basis      contractsv1.ContextFabricRefusalBasis
		kept       int
	}{
		{"no declared kind, one repository candidate keeps the offer", "", []SubjectKind{SubjectRepository}, "clarification_required", "", 1},
		{"no declared kind, mixed candidates keep the offer", "", []SubjectKind{SubjectProject, SubjectTeam}, "clarification_required", "", 2},
		{"no declared kind, nothing found", "", nil, "no_match", "", 0},
		{"declared repository, one repository candidate", SubjectRepository, []SubjectKind{SubjectRepository}, "clarification_required", "", 1},
		{"declared repository, one project candidate", SubjectRepository, []SubjectKind{SubjectProject}, "no_match", "declared_kind_unmatched", 1},
		{"declared repository, nothing found", SubjectRepository, nil, "no_match", "", 0},
		{"declared team, nothing found", SubjectTeam, nil, "no_match", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := runDeploymentCohortWithNoCommit(t, tc.declared, tc.candidates...)
			if before.basis == contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
				t.Fatalf("a servable or unknown anchor kind must not end member_kind_unservable: %+v", before)
			}
			t.Logf("terminal: status=%q basis=%q candidates=%d", before.status, before.basis, before.candidates)
			if string(before.status) != string(tc.status) || string(before.basis) != string(tc.basis) || before.candidates != tc.kept {
				t.Fatalf("terminal = status %q basis %q candidates %d, want %q %q %d", before.status, before.basis, before.candidates, tc.status, tc.basis, tc.kept)
			}
		})
	}
}
