package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestWorkItemSurveyGoalTelemetryReflectsTheEnforcedGate drives ONE real
// interpretation -- RuntimeQuestionInterpreter.Interpret, its resolveFrame,
// its FrameTelemetry port -- and reads the one frame-validation line it
// emits, the same production call site TestWorkItemSurveyGoalDispatchesMembershipRead
// exercises at the engine layer.
//
// It pins the fix beside the admission widening: FrameValidationEventFrom
// re-derives DecideFrameGate on its own and cannot see the work-item tuple's
// refinement, so the line's frame_gate/refuse_basis must come from the SAME
// gate resolveFrame already decided (event.Gate = gate), not a second,
// pre-refinement reading of it -- otherwise an admitted, dispatched survey
// turn would log as refused.
func TestWorkItemSurveyGoalTelemetryReflectsTheEnforcedGate(t *testing.T) {
	for _, tc := range []struct {
		name            string
		emphasis        []AnswerEmphasis
		wantGate        string
		wantOrdering    bool
		wantRefuseBasis string
	}{
		{"no_ordering_admits", nil, "passed", false, "none"},
		{"ordering_refuses", []AnswerEmphasis{EmphasisPositiveOutliers}, "refused:member_kind_unservable", true, "member_kind_unservable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			logs := captureEngineLogger(t)
			frame := prospectiveTupleFrame(GoalRankOrSurvey)
			frame.Emphasis = tc.emphasis
			receipt := validModelReceiptFixture(ModelOperationInterpret)
			receipt.QuestionFrame = &frame
			interpreted := InterpretedQuestion{
				Shape: ShapeOpen, RequestedJudgment: "survey", TimeContext: TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}}, SubjectTerms: []string{"Project"},
			}
			interpreter := RuntimeQuestionInterpreter{
				Runtime:        fakeModelRuntime{interpreted: interpreted, receipt: receipt},
				Sink:           &fakeReceiptSink{},
				FrameTelemetry: logs.telemetry,
			}
			if _, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
				t.Fatalf("Interpret() error = %v", err)
			}
			lines := linesWithMessage(t, logs.configured.String(), frameValidationMessage)
			if len(lines) != 1 {
				t.Fatalf("frame-validation lines = %d, want exactly 1 per interpretation", len(lines))
			}
			line := lines[0]
			if line["frame_gate"] != tc.wantGate {
				t.Fatalf("frame_gate = %v, want %q", line["frame_gate"], tc.wantGate)
			}
			if line["ordering_present"] != tc.wantOrdering {
				t.Fatalf("ordering_present = %v, want %v", line["ordering_present"], tc.wantOrdering)
			}
			if line["refuse_basis"] != tc.wantRefuseBasis {
				t.Fatalf("refuse_basis = %v, want %q", line["refuse_basis"], tc.wantRefuseBasis)
			}
		})
	}
}
