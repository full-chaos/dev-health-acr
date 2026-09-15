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
//
// It also pins the promotion's own observability requirement:
// stripped_obligations on this SAME line must show what the promotion's
// in-place frame mutation removed (nil/empty when nothing did), never only
// inferable from the frame a later stage happens to read.
func TestWorkItemSurveyGoalTelemetryReflectsTheEnforcedGate(t *testing.T) {
	for _, tc := range []struct {
		name            string
		goals           []InvestigationGoal
		emphasis        []AnswerEmphasis
		wantGate        string
		wantOrdering    bool
		wantRefuseBasis string
		wantStripped    []string
	}{
		{"survey_no_ordering_admits", []InvestigationGoal{GoalRankOrSurvey}, nil, "passed", false, "none", []string{"ranking"}},
		{"survey_ordering_refuses", []InvestigationGoal{GoalRankOrSurvey}, []AnswerEmphasis{EmphasisPositiveOutliers}, "refused:member_kind_unservable", true, "member_kind_unservable", nil},
		{"assess_state_admits_nothing_to_strip", []InvestigationGoal{GoalAssessState}, nil, "passed", false, "none", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			logs := captureEngineLogger(t)
			frame := prospectiveTupleFrame(tc.goals...)
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
			got := stringSliceLogValue(t, line["stripped_obligations"])
			if !equalStringSlices(got, tc.wantStripped) {
				t.Fatalf("stripped_obligations = %v, want %v", got, tc.wantStripped)
			}
			for _, member := range got {
				if !ValidAnswerObligation(AnswerObligation(member)) {
					t.Fatalf("stripped_obligations carries %q, not a member of the closed AnswerObligation vocabulary", member)
				}
			}
		})
	}
}

// TestNonWorkItemRankingFrameNeverPredictsAStrip is the negative control
// workItemTupleObligationsToStrip's own gating needs beside the positive
// case above: a frame that legitimately carries ObligationRanking
// (GoalRankOrSurvey) but is not children_of_scope/work_item passes the
// base frame gate WITHOUT the tuple ever running (prospectiveWorkItemTupleAdmission
// returns not_applicable), so the prediction must never fire for it --
// resolveFrame's own gate is already FrameGatePassed BEFORE the tuple call,
// which is exactly the case an unconditional "gate ended up passed" read
// (instead of "THIS call promoted it") would get wrong.
func TestNonWorkItemRankingFrameNeverPredictsAStrip(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	logs := captureEngineLogger(t)
	frame := frameWith([]InvestigationGoal{GoalRankOrSurvey}, discoveredExpression(SubjectIncident), TemporalIntentCurrent, nil)
	if !frame.HasObligation(ObligationRanking) {
		t.Fatal("fixture frame does not carry the ranking obligation to begin with")
	}
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.QuestionFrame = &frame
	interpreted := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "rank", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}}, SubjectTerms: []string{"incidents"},
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
	if line["frame_gate"] != "passed" {
		t.Fatalf("frame_gate = %v, want \"passed\" (a discovered-kind incident frame is not this arm's concern)", line["frame_gate"])
	}
	if got := stringSliceLogValue(t, line["stripped_obligations"]); got != nil {
		t.Fatalf("stripped_obligations = %v, want nil: the work-item tuple never ran for this frame", got)
	}
}

// stringSliceLogValue decodes a captured JSON log field (a []any of
// strings, or absent/null) into a []string, nil for either absent form --
// the log line's own "nothing stripped" contract.
func stringSliceLogValue(t *testing.T, raw any) []string {
	t.Helper()
	if raw == nil {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("stripped_obligations field is %T, want a JSON array", raw)
	}
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("stripped_obligations element is %T, want a string", item)
		}
		out = append(out, s)
	}
	return out
}
