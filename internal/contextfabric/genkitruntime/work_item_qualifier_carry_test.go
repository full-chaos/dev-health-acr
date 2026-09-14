package genkitruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This test is intentionally run against the untouched baseline before the
// carrier is implemented. The real JSON decoder must preserve a qualifier
// emitted by the model; dropping the unknown field makes the assertion fail.
func TestParseInterpretationOutputSignalsCarriesMemberQualifier(t *testing.T) {
	rawOutput := rawInterpretationOutputWithMemberQualifier(t, "assignee")

	_, capture, err := ParseInterpretationOutputSignals(rawOutput, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatalf("ParseInterpretationOutputSignals() error = %v", err)
	}
	encodedFrame, err := json.Marshal(capture.Frame.Frame)
	if err != nil {
		t.Fatalf("json.Marshal(frame) error = %v", err)
	}
	if !strings.Contains(string(encodedFrame), `"member_qualifier":"assignee"`) {
		t.Fatalf("sanitized frame = %s, want the emitted member qualifier carried through decoding", encodedFrame)
	}
	if capture.Frame.MemberQualifierUnrecognized {
		t.Fatal("recognized member qualifier was marked unrecognized")
	}
}

func TestParseInterpretationOutputSignalsKeepsUnknownMemberQualifierPresent(t *testing.T) {
	rawOutput := rawInterpretationOutputWithMemberQualifier(t, "future_filter")

	_, capture, err := ParseInterpretationOutputSignals(rawOutput, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatalf("ParseInterpretationOutputSignals() error = %v", err)
	}
	if capture.Frame.Frame.SubjectExpression.Scoped == nil {
		t.Fatal("sanitized frame has no scoped expression")
	}
	if got := capture.Frame.Frame.SubjectExpression.Scoped.MemberQualifier; got != contextfabric.MemberQualifierUnrecognized {
		t.Fatalf("sanitized member qualifier = %q, want explicit unrecognized marker", got)
	}
	if !capture.Frame.MemberQualifierUnrecognized {
		t.Fatal("unknown member qualifier was not recorded on the transport capture")
	}

	receipt := contextfabric.ModelExecutionReceipt{}
	ApplyInterpretationCapture(&receipt, InterpretationOutputCapture{Frame: capture.Frame})
	if receipt.QuestionFrame == nil || receipt.QuestionFrame.SubjectExpression.Scoped == nil {
		t.Fatal("ApplyInterpretationCapture dropped the scoped frame")
	}
	if got := receipt.QuestionFrame.SubjectExpression.Scoped.MemberQualifier; got != contextfabric.MemberQualifierUnrecognized {
		t.Fatalf("receipt member qualifier = %q, want explicit unrecognized marker", got)
	}
	if !receipt.FrameMemberQualifierUnrecognized {
		t.Fatal("receipt did not carry the unknown-qualifier signal")
	}
}

func TestParseInterpretationOutputSignalsCarriesScopedOperandQualifier(t *testing.T) {
	output := validInterpretationOutput()
	output.QuestionFrame = &questionFrameOutput{
		Goals: []string{"compare"},
		SubjectExpression: &subjectExpressionOutput{
			Kind: "explicit_set",
			Operands: []subjectOperandOutput{{
				Kind:            "children_of_scope",
				AnchorTerms:     []string{"Project Alpha"},
				MemberKind:      "work_item",
				MemberQualifier: "status",
			}},
		},
		Temporal: "current",
	}
	rawOutput, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	_, capture, err := ParseInterpretationOutputSignals(rawOutput, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatalf("ParseInterpretationOutputSignals() error = %v", err)
	}
	if len(capture.Frame.Frame.SubjectExpression.Explicit.Operands) != 1 {
		t.Fatalf("sanitized explicit operands = %d, want 1", len(capture.Frame.Frame.SubjectExpression.Explicit.Operands))
	}
	operand := capture.Frame.Frame.SubjectExpression.Explicit.Operands[0]
	if operand.Scoped == nil {
		t.Fatal("sanitized explicit operand has no scoped variant")
	}
	if got := operand.Scoped.MemberQualifier; got != contextfabric.MemberQualifierStatus {
		t.Fatalf("scoped operand qualifier = %q, want %q", got, contextfabric.MemberQualifierStatus)
	}
	if capture.Frame.MemberQualifierUnrecognized {
		t.Fatal("recognized scoped operand qualifier was marked unrecognized")
	}
}

func TestInterpretQuestionPreservesUnknownMemberQualifierOnReceipt(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFrame = &questionFrameOutput{
		Goals: []string{"assess_state"},
		SubjectExpression: &subjectExpressionOutput{
			Kind:            "children_of_scope",
			AnchorTerms:     []string{"Project Alpha"},
			MemberKind:      "work_item",
			MemberQualifier: "future_filter",
		},
		Temporal: "current",
	}

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.QuestionFrame == nil || receipt.QuestionFrame.SubjectExpression.Scoped == nil {
		t.Fatal("InterpretQuestion() dropped the scoped frame")
	}
	if got := receipt.QuestionFrame.SubjectExpression.Scoped.MemberQualifier; got != contextfabric.MemberQualifierUnrecognized {
		t.Fatalf("receipt member qualifier = %q, want explicit unrecognized marker", got)
	}
	if !receipt.FrameMemberQualifierUnrecognized {
		t.Fatal("InterpretQuestion() did not carry the unknown-qualifier signal onto the receipt")
	}
}

func rawInterpretationOutputWithMemberQualifier(t *testing.T, qualifier string) []byte {
	t.Helper()
	rawOutput, err := json.Marshal(validInterpretationOutput())
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rawOutput, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	payload["question_frame"] = map[string]any{
		"goals": []string{"assess_state"},
		"subject_expression": map[string]any{
			"kind":             "children_of_scope",
			"anchor_terms":     []string{"Project Alpha"},
			"member_kind":      "work_item",
			"member_qualifier": qualifier,
		},
		"temporal": "current",
	}
	rawOutput, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal(payload) error = %v", err)
	}
	return rawOutput
}
