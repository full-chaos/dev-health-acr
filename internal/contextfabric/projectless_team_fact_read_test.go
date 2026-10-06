package contextfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type projectlessTeamInterpreter struct{ requirements []FactRequirement }

func (i projectlessTeamInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	v := ValidateFrame(QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"the platform team"}, MemberKind: SubjectProject},
		},
		Temporal: TemporalIntentCurrent,
	}, nil, ShapeOpen)
	frame := v.Frame
	return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", FactRequirements: i.requirements, TimeContext: TimeContext{Axis: TemporalCurrent}},
		QuestionFamilyOutcome{
			Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame,
			Gate: FrameGate{Outcome: FrameGatePassed}, WinningSampleIndex: 0,
			WinningSample: FamilySample{ModelFamily: QuestionFamilyScopedCohortStatus, ScopeAnchorKind: SubjectTeam, ScopeAnchorTerm: "the platform team"},
			Version:       QuestionFamilyTableVersion,
		}, nil
}

func TestProjectlessTeamChildrenOfScopeDoesNotAbortFactRead(t *testing.T) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:00000000-0000-0000-0000-000000000001", Label: "Platform"}
	for _, tc := range []struct {
		name         string
		requirements []FactRequirement
		wantReads    int
	}{
		{"no requirement from the interpretation and no cohort", nil, 1},
		{"a requirement from the interpretation still reads", []FactRequirement{{Kind: FactMembership}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry, err := NewFactCapabilityRegistry(nil, FactRegistryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			reads := 0
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{
					resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{team}},
					bases:      provenCommitBases(team),
				},
				Facts: factReaderFunc(func(ctx context.Context, p storage.Principal, r CanonicalFactRequest) (CanonicalFactBundle, error) {
					reads++
					return registry.ReadFacts(ctx, p, r)
				}),
				Interpreter: projectlessTeamInterpreter{requirements: tc.requirements},
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return validInvestigationResult(), nil
				}),
			})
			_, err = engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
			if errors.Is(err, ErrFactReadAborted) {
				t.Fatalf("fact read aborted: %v", err)
			}
			if err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			if reads != tc.wantReads {
				t.Fatalf("fact reads = %d, want %d", reads, tc.wantReads)
			}
		})
	}
}

func TestEmptyFactRequirementsAreClassifiedAndLogged(t *testing.T) {
	registry, err := NewFactCapabilityRegistry(nil, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.ReadFacts(context.Background(), acceptancePrincipal(), CanonicalFactRequest{
		Subjects: []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:x", Label: "x"}},
	})
	if !errors.Is(err, ErrNoFactRequirements) {
		t.Fatalf("err = %v, want ErrNoFactRequirements", err)
	}
}
