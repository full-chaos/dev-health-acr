package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const frameValidationMessage = "context fabric frame validation"

func boundaryFrame(expression SubjectExpression) QuestionFrame {
	return QuestionFrame{
		Version:           QuestionFrameVersion,
		SubjectExpression: expression,
		Obligations:       []AnswerObligation{ObligationState},
		Goals:             []InvestigationGoal{GoalAssessState},
		Temporal:          TemporalIntentCurrent,
	}
}

// interpretThroughTheBoundary runs ONE real interpretation -- the production
// RuntimeQuestionInterpreter, its resolveFrame, its FrameTelemetry port --
// with the engine telemetry on a CONFIGURED logger, and returns the one
// frame-validation line that interpretation emitted.
//
// The process default logger is captured separately and must stay empty:
// slog.Default() never reaches acr-api's JSON handler, so a line that lands
// there does not exist in the deployed service.
func interpretThroughTheBoundary(t *testing.T, groupHint, memberHint SubjectKind, frame QuestionFrame) map[string]any {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.GroupKind = groupHint
	receipt.RequestedSubjectKind = memberHint
	receipt.QuestionFrame = &frame
	interpreter := RuntimeQuestionInterpreter{
		Runtime:        fakeModelRuntime{interpreted: groupedInterpretation(), receipt: receipt},
		Sink:           &fakeReceiptSink{},
		FrameTelemetry: logs.telemetry,
	}
	if _, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	lines := linesWithMessage(t, logs.configured.String(), frameValidationMessage)
	if len(lines) != 1 {
		t.Fatalf("frame-validation lines on the configured logger = %d, want exactly 1 per interpretation", len(lines))
	}
	if stray := linesWithMessage(t, logs.fallback.String(), frameValidationMessage); len(stray) != 0 {
		t.Fatalf("%d frame-validation line(s) reached the PROCESS DEFAULT logger, which acr-api's JSON handler never reads", len(stray))
	}
	line := lines[0]
	if line["level"] != "INFO" {
		t.Fatalf("level = %v, want INFO -- the boundary is a routine per-turn decision and must be readable about a turn that already happened", line["level"])
	}
	return line
}

// assertBoundaryLine compares against LITERALS, not the producer's own
// constants: an expectation spelled with the thing under test cannot fail
// when that thing is renamed or reordered.
func assertBoundaryLine(t *testing.T, line map[string]any, want map[string]string) {
	t.Helper()
	t.Logf("boundary line: requested_group_hint=%v requested_member_hint=%v proposed_kind=%v proposed_group_kind=%v proposed_member_kind=%v outcome=%v failed_invariant=%v failure_detail=%v frame_gate=%v group_axis=%v",
		line["requested_group_hint"], line["requested_member_hint"], line["proposed_kind"], line["proposed_group_kind"],
		line["proposed_member_kind"], line["outcome"], line["failed_invariant"], line["failure_detail"], line["frame_gate"], line["group_axis"])
	for key, value := range want {
		got, present := line[key]
		if !present {
			t.Errorf("%s is ABSENT from the line -- missing is not zero, and the decision graph cannot be rebuilt without it", key)
			continue
		}
		if got != value {
			t.Errorf("%s = %v, want %q", key, got, value)
		}
	}
}

// TestTheBoundaryShowsAGroupingTheFrameDropped is the laundering signature
// CHAOS-5390 was found by, and it is now REFUSED rather than only named.
//
// The model's own hint asked for a grouping by team; the frame it proposed is
// a flat discovered_kind frame over teams. That frame validates on its own
// terms, and the gate used to PASS it -- this pin once asserted exactly that,
// with `dropped_at_interpretation` as the only trace of an axis the question
// asked for and the answer silently lost (round 2, P1-1). The ruling is that
// the requested axis is kept or refused under i6 with its basis; a dropped
// axis is neither, so the gate refuses it.
func TestTheBoundaryShowsAGroupingTheFrameDropped(t *testing.T) {
	line := interpretThroughTheBoundary(t, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject,
		boundaryFrame(discoveredExpression(contractsv1.ContextFabricSubjectTeam)))
	assertBoundaryLine(t, line, map[string]string{
		"requested_group_hint":  "team",
		"requested_member_hint": "project",
		"proposed_kind":         string(SubjectExpressionDiscoveredKind),
		"proposed_group_kind":   "not_applicable",
		"proposed_member_kind":  "team",
		"outcome":               string(FrameValidationOutcomeRefusedInvalid),
		"failed_invariant":      string(FrameInvariantI6),
		"failure_detail":        "requested_group_axis_not_expressed",
		"frame_gate":            "rejected:" + string(FrameInvariantI6),
		"group_axis":            "refused",
	})
}

// TestTheBoundaryShowsASelfGroupRefusedWithItsInvariant is the path the prompt
// change is for: the grouping expressed as asked, the kind grouped by itself,
// and the server -- not the model -- refusing it under i6.
func TestTheBoundaryShowsASelfGroupRefusedWithItsInvariant(t *testing.T) {
	line := interpretThroughTheBoundary(t, contractsv1.ContextFabricSubjectTeam, "",
		boundaryFrame(groupedExpression(contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectTeam)))
	assertBoundaryLine(t, line, map[string]string{
		"requested_group_hint":  "team",
		"requested_member_hint": "absent",
		"proposed_kind":         string(SubjectExpressionGroupedMembers),
		"proposed_group_kind":   "team",
		"proposed_member_kind":  "team",
		"outcome":               string(FrameValidationOutcomeRefusedInvalid),
		"failed_invariant":      string(FrameInvariantI6),
		"failure_detail":        string(FrameFailureGroupEqualsMember),
		"frame_gate":            "rejected:" + string(FrameInvariantI6),
		"group_axis":            "refused",
	})
}

// TestTheBoundaryShowsALegalGroupingKept is the DISCRIMINATING CONTROL for
// both pins above: a legal grouping (projects by team) is kept, the group and
// member kinds DIFFER on the line -- so an emitter that swapped the two slots
// would fail here -- and the hint-absent member reads `absent`, not empty.
func TestTheBoundaryShowsALegalGroupingKept(t *testing.T) {
	line := interpretThroughTheBoundary(t, contractsv1.ContextFabricSubjectTeam, "",
		boundaryFrame(groupedExpression(contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectTeam))) // (member, group)
	assertBoundaryLine(t, line, map[string]string{
		"requested_group_hint":  "team",
		"requested_member_hint": "absent",
		"proposed_group_kind":   "team",
		"proposed_member_kind":  "project",
		"outcome":               string(FrameValidationOutcomeValid),
		"frame_gate":            string(FrameGatePassed),
		"group_axis":            "kept",
	})
}

// TestTheBoundaryShowsAFlatQuestionAsNotRequested is the other control: no
// hint and no grouping is the ordinary flat question, and the line says so
// with explicit tokens rather than empty strings.
func TestTheBoundaryShowsAFlatQuestionAsNotRequested(t *testing.T) {
	line := interpretThroughTheBoundary(t, "", "",
		boundaryFrame(discoveredExpression(contractsv1.ContextFabricSubjectProject)))
	assertBoundaryLine(t, line, map[string]string{
		"requested_group_hint":  "absent",
		"requested_member_hint": "absent",
		"proposed_group_kind":   "not_applicable",
		"proposed_member_kind":  "project",
		"frame_gate":            string(FrameGatePassed),
		"group_axis":            "not_requested",
	})
}
