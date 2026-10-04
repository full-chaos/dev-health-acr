package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// anotherAnchorInterpreter reads a team-members question whose anchor term is
// "bravo", declares the anchor kind repository, and names the alpha
// repository as a subject term.
type anotherAnchorInterpreter struct{}

func (anotherAnchorInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"bravo"}, MemberKind: contextfabric.SubjectTeam},
		},
		Temporal: contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
	}, nil)
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "team",
		SubjectTerms: []string{routeOwnedSlug}, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{},
	}, contextfabric.QuestionFamilyOutcome{
		Frame: &frame, FrameObligations: frame.Obligations,
		Family: contextfabric.QuestionFamilyScopedCohortStatus, Source: contextfabric.QuestionFamilySourceModel,
		Gate:          contextfabric.DecideFrameGate(contextfabric.ValidateFrame(frame, nil, ""), true),
		WinningSample: contextfabric.FamilySample{ScopeAnchorKind: contextfabric.SubjectRepository, ScopeAnchorTerm: "bravo"},
	}, nil
}

// TestADeclaredRepositoryKindDoesNotRouteOwnershipForARepositoryTheAnchorDoesNotName:
// the question's anchor names another subject; a repository another term of
// the question committed is not the ownership anchor, whatever kind the model
// declared.
func TestADeclaredRepositoryKindDoesNotRouteOwnershipForARepositoryTheAnchorDoesNotName(t *testing.T) {
	s := seedOwnedRepository()
	telemetry := &recordingTelemetry{}
	graph := &routeBasisRecorder{Adapter: newFakeAdapterWithTelemetry(t, s.conn(), telemetry)}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: anotherAnchorInterpreter{}, Graph: graph, Facts: emptyFactReader{},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: routeNoModelRuntime{t: t}, Options: contextfabric.RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "p", QueryVersion: "q"}, ClientSynthesis: synthesisprompt.ClientAssembly()},
		Results:     discardingResultStore{}, Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_86130001" }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := contextfabric.WithSynthesisInputCollector(context.Background())
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_86130001",
		Question: "which teams work on bravo", SynthesisMode: contextfabric.SynthesisModeClient,
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
		Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
		Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.committed) != 1 || graph.committed[0].CanonicalID != routeOwnedRepository || graph.bases.For(graph.committed[0]) != contextfabric.CommitBasisStatistical {
		t.Fatalf("resolver committed %+v, want the alpha repository on a statistical basis: the fixture must reach the case", graph.committed)
	}
	if len(telemetry.ownershipRoutings) != 1 || telemetry.ownershipRoutings[0].Outcome != OwnershipRoutingNotRouted {
		t.Fatalf("ownership decisions = %+v, want one not_routed decision", telemetry.ownershipRoutings)
	}
	if result.Cohort != nil {
		for _, m := range result.Cohort.Members {
			if m.Subject.CanonicalID == "team:owner" && len(m.InclusionReasons) == 1 && m.InclusionReasons[0] == ownershipInclusionReason {
				t.Fatalf("the alpha repository's owner was served as an owner of the anchor the question names")
			}
		}
	}
}
