package contextfabric

import (
	"context"
	"reflect"
	"testing"
)

// CHAOS-4451: through the real Engine.Investigate, the class-default gate
// decides each turn on an inferred window once and writes one line naming the
// decision and its basis. A repository status question with no stated window
// draws an inferred default (the pick below is not state_snapshot), so the
// gate runs; what the offers-only pass returns and the frame's period shape
// decide.
func TestInferredWindowGateDecidesAndRecordsEachBasis(t *testing.T) {
	t.Parallel()
	pick := handleStateTestPicks[0]
	if pick.class == WindowClassStateSnapshot {
		t.Fatalf("fixture: pick %q draws no default window", pick.name)
	}
	for _, tc := range []struct {
		name     string
		material StructureOfferMaterial
		temporal TemporalIntent
		want     recordedInferredWindowGate
		gated    bool
	}{
		{"window only", StructureOfferMaterial{}, TemporalIntentCurrent, recordedInferredWindowGate{InferredWindowGateAnswered, InferredWindowGateBasisWindowOnly}, false},
		{"a question beyond the window", chaos4234GatedMaterial(), TemporalIntentCurrent, recordedInferredWindowGate{InferredWindowGateGated, InferredWindowGateBasisBeyondWindow}, true},
		{"a series frame", StructureOfferMaterial{}, TemporalIntentTimeSeries, recordedInferredWindowGate{InferredWindowGateGated, InferredWindowGateBasisPeriodShape}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			interpreter := &countingInterpreter{
				interpretation: singleSubjectInterpretation(pick.class, pick.confidence),
				family:         QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Frame: handleStateFrame(SubjectRepository, []InvestigationGoal{GoalAssessState}, tc.temporal)},
			}
			request := validInvestigationRequest()
			request.Question = "What is the status of full-chaos/dev-health-web?"
			engine, graph := handleStateEngine(t, interpreter, telemetry)
			graph.material = tc.material
			result, _ := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if !reflect.DeepEqual(telemetry.inferredWindowGates, []recordedInferredWindowGate{tc.want}) {
				t.Fatalf("gate lines = %+v, want exactly %+v", telemetry.inferredWindowGates, tc.want)
			}
			if gated := result.Status == InvestigationClarificationRequired; gated != tc.gated {
				t.Fatalf("status = %q, want gated=%v", result.Status, tc.gated)
			}
		})
	}
}

// A turn whose window is stated never reaches the gate, and writes no line.
func TestAStatedWindowWritesNoInferredWindowGateLine(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	interpreter := &countingInterpreter{
		interpretation: singleSubjectInterpretation(handleStateTestPicks[0].class, handleStateTestPicks[0].confidence),
		family:         QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Frame: handleStateFrame(SubjectRepository, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent)},
	}
	request := validInvestigationRequestWithConfirmedWindow()
	engine, _ := handleStateEngine(t, interpreter, telemetry)
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Logf("Investigate() error = %v (not under test)", err)
	}
	if len(telemetry.inferredWindowGates) != 0 {
		t.Fatalf("gate lines = %+v, want none on a stated window", telemetry.inferredWindowGates)
	}
}
