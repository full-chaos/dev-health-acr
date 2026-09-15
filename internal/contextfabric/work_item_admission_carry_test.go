package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemFreshRefusalCannotBePromotedByPlanCarry(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	frame := prospectiveTupleFrame(GoalAssessState)
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.QuestionFrame = &frame
	sampled := &sampledRuntimeStub{perIdx: map[int]InterpretedQuestion{0: ensembleQuestion(ShapeSingleSubject, "s0"), 1: ensembleQuestion(ShapeDiscoveredCohort, "s1"), 2: ensembleQuestion(ShapeExplicitCohort, "s2")}, receipt: receipt}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}
	request := validInvestigationRequestWithConfirmedWindow()
	_, initial, err := interpreter.Interpret(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Source != QuestionFamilySourcePluralityRejected || initial.Frame == nil || !initial.Gate.Refuses() {
		t.Fatalf("fixture failed to produce an actual initial tuple refusal: %+v", initial)
	}
	t.Logf("actual interpreter family=%s source=%s gate=%s", initial.Family, initial.Source, initial.Gate.Observable())
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyScopedCohortStatus, "")
	request.ParentResultID = prior.ResultID
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: workItemTuplePayloadFixture(t).SubjectResolution}}
	reads := 0
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{Interpreter: interpreter, Graph: graph, Results: store, Telemetry: telemetry,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			reads++
			return nil, WorkItemMembershipResult{}, nil
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
	})
	_, _ = engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if len(telemetry.planCarryOutcomes) != 1 || telemetry.planCarryOutcomes[0].outcome != PlanCarryHit {
		t.Fatalf("fixture did not apply carry: %+v", telemetry.planCarryOutcomes)
	}
	if graph.resolveCalls != 0 || graph.discoverCalls != 0 || reads != 0 {
		t.Fatalf("initial refused tuple promoted by carry: Resolve=%d Discover=%d S1=%d", graph.resolveCalls, graph.discoverCalls, reads)
	}
}
