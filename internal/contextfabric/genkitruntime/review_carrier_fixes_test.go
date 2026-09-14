package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestFallbackReceiptCarriesCompleteFrameCapture(t *testing.T) {
	t.Parallel()

	t.Run("recognized", func(t *testing.T) {
		t.Parallel()
		output := validInterpretationOutput()
		output.QuestionFrame = &questionFrameOutput{
			Goals: []string{"assess_state"},
			SubjectExpression: &subjectExpressionOutput{
				Kind:            "children_of_scope",
				AnchorTerms:     []string{"Project Alpha"},
				MemberKind:      "work_item",
				MemberQualifier: "status",
			},
			Temporal: "current",
		}
		fallback := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
		primary := mustRuntime(t, &generatorStub{interpretErr: errors.New("primary unavailable")}, Config{
			MaxAttempts: 1,
			Fallback:    fallback,
		})

		_, receipt, err := primary.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
		if err != nil {
			t.Fatalf("InterpretQuestion() error = %v", err)
		}
		if receipt.Outcome != "fallback" || !receipt.FallbackUsed {
			t.Fatalf("receipt outcome/fallback = %q/%t, want fallback/true", receipt.Outcome, receipt.FallbackUsed)
		}
		if receipt.QuestionFrame == nil || receipt.QuestionFrame.SubjectExpression.Scoped == nil {
			t.Fatalf("fallback receipt lost the returned question frame: %#v", receipt)
		}
		got := receipt.QuestionFrame.SubjectExpression.Scoped.MemberQualifier
		if got != contextfabric.MemberQualifierStatus {
			t.Fatalf("fallback receipt qualifier = %q, want status", got)
		}
	})

	t.Run("unrecognized", func(t *testing.T) {
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
		fallback := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
		primary := mustRuntime(t, &generatorStub{interpretErr: errors.New("primary unavailable")}, Config{
			MaxAttempts: 1,
			Fallback:    fallback,
		})

		_, receipt, err := primary.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
		if err != nil {
			t.Fatalf("InterpretQuestion() error = %v", err)
		}
		if receipt.QuestionFrame == nil || receipt.QuestionFrame.SubjectExpression.Scoped == nil {
			t.Fatalf("fallback receipt lost the returned question frame: %#v", receipt)
		}
		got := receipt.QuestionFrame.SubjectExpression.Scoped.MemberQualifier
		if got != contextfabric.MemberQualifierUnrecognized || !receipt.FrameMemberQualifierUnrecognized {
			t.Fatalf("fallback receipt qualifier = %q/unknown=%t, want unrecognized/true", got, receipt.FrameMemberQualifierUnrecognized)
		}
	})
}

func TestMergeFallbackReceiptCarriesFrameSiblingsAndPreservesPrimaryProvenance(t *testing.T) {
	t.Parallel()
	primary := validReceipt(contextfabric.ModelOperationInterpret)
	primary.Provider = "primary-provider"
	primary.Model = "primary-model"
	primary.ModelVersion = "primary-version"
	primary.StartedAt = time.Date(2026, 9, 14, 1, 2, 3, 0, time.UTC)
	primary.CompletedAt = time.Date(2026, 9, 14, 1, 2, 4, 0, time.UTC)
	primary.Attempts = 3
	primary.InputDigest = strings.Repeat("1", 64)
	primary.Usage = contextfabric.ModelUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}

	fallback := validReceipt(contextfabric.ModelOperationInterpret)
	fallback.Provider = "fallback-provider"
	fallback.Model = "fallback-model"
	fallback.ModelVersion = "fallback-version"
	fallback.OutputDigest = strings.Repeat("2", 64)
	fallback.Usage = contextfabric.ModelUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18}
	fallback.QuestionFrame = &contextfabric.QuestionFrame{}
	fallback.FrameOutcome = contextfabric.FrameValidationOutcomeRepaired
	fallback.FrameFailedInvariant = contextfabric.FrameInvariantI17
	fallback.FrameGateOutcome = contextfabric.FrameGateRefusedBasis
	fallback.FrameGateRefuseBasis = contextfabric.CohortMemberKindUnservable
	fallback.FrameGateDeclaredMemberKind = contextfabric.SubjectWorkItem
	fallback.FrameGoalsDropped = 1
	fallback.FrameTermsTruncated = 2
	fallback.FrameKindUnrecognized = true
	fallback.RequirementCellsDerived = 3
	fallback.RequirementCellsUnserved = 4
	fallback.RequirementDerivationVersion = "requirements-v2"
	fallback.FrameTemporalUnrecognized = true
	fallback.FrameEmphasisDropped = 5
	fallback.FrameDimensionsDropped = 6
	fallback.FrameMemberKindUnrecognized = true
	fallback.FrameGroupKindUnrecognized = true
	fallback.FrameMemberQualifierUnrecognized = true

	got := mergeFallbackReceipt(primary, fallback)
	if !reflect.DeepEqual(got.QuestionFrame, fallback.QuestionFrame) {
		t.Fatalf("QuestionFrame = %#v, want fallback capture %#v", got.QuestionFrame, fallback.QuestionFrame)
	}
	if got.FrameOutcome != fallback.FrameOutcome || got.FrameFailedInvariant != fallback.FrameFailedInvariant ||
		got.FrameGateOutcome != fallback.FrameGateOutcome || got.FrameGateRefuseBasis != fallback.FrameGateRefuseBasis ||
		got.FrameGateDeclaredMemberKind != fallback.FrameGateDeclaredMemberKind || got.FrameGoalsDropped != fallback.FrameGoalsDropped ||
		got.FrameTermsTruncated != fallback.FrameTermsTruncated || got.FrameKindUnrecognized != fallback.FrameKindUnrecognized ||
		got.RequirementCellsDerived != fallback.RequirementCellsDerived || got.RequirementCellsUnserved != fallback.RequirementCellsUnserved ||
		got.RequirementDerivationVersion != fallback.RequirementDerivationVersion || got.FrameTemporalUnrecognized != fallback.FrameTemporalUnrecognized ||
		got.FrameEmphasisDropped != fallback.FrameEmphasisDropped || got.FrameDimensionsDropped != fallback.FrameDimensionsDropped ||
		got.FrameMemberKindUnrecognized != fallback.FrameMemberKindUnrecognized || got.FrameGroupKindUnrecognized != fallback.FrameGroupKindUnrecognized ||
		got.FrameMemberQualifierUnrecognized != fallback.FrameMemberQualifierUnrecognized {
		t.Fatalf("frame capture was not copied completely: got %#v, fallback %#v", got, fallback)
	}
	if got.Provider != fallback.Provider || got.Model != fallback.Model || got.ModelVersion != fallback.ModelVersion ||
		got.OutputDigest != fallback.OutputDigest || !got.FallbackUsed {
		t.Fatalf("fallback identity/output merge = %#v", got)
	}
	if !got.StartedAt.Equal(primary.StartedAt) || !got.CompletedAt.Equal(primary.CompletedAt) || got.Attempts != primary.Attempts || got.InputDigest != primary.InputDigest {
		t.Fatalf("primary timing/attempt/input provenance changed: got %#v, primary %#v", got, primary)
	}
	wantUsage := contextfabric.ModelUsage{InputTokens: 9, OutputTokens: 14, TotalTokens: 23}
	if got.Usage != wantUsage {
		t.Fatalf("usage = %#v, want sum %#v", got.Usage, wantUsage)
	}
}

func TestParseInterpretationOutputSignalsMemberQualifierDomain(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"outer", "scoped_operand"} {
		form := form
		t.Run(form, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name        string
				value       *string
				want        contextfabric.MemberQualifier
				wantPresent bool
				wantUnknown bool
			}{
				{name: "absent", value: nil},
				{name: "empty", value: stringPtr("")},
				{name: "recognized", value: stringPtr("status"), want: contextfabric.MemberQualifierStatus, wantPresent: true},
				{name: "unknown", value: stringPtr("future_filter"), want: contextfabric.MemberQualifierUnrecognized, wantPresent: true, wantUnknown: true},
			} {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					raw := rawOutputWithQualifierValue(t, form, tc.value)
					_, capture, err := ParseInterpretationOutputSignals(raw, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
					if err != nil {
						t.Fatalf("ParseInterpretationOutputSignals() error = %v", err)
					}
					got, present := qualifierFromCapture(t, capture, form)
					if got != tc.want || present != tc.wantPresent || capture.Frame.MemberQualifierUnrecognized != tc.wantUnknown {
						t.Fatalf("qualifier = %q/present=%t/unknown=%t, want %q/%t/%t", got, present, capture.Frame.MemberQualifierUnrecognized, tc.want, tc.wantPresent, tc.wantUnknown)
					}
				})
			}
		})
	}
}

func TestParseInterpretationOutputSignalsRejectsExplicitNullMemberQualifier(t *testing.T) {
	t.Parallel()
	variants := []struct {
		name  string
		field string
	}{
		{name: "exact_null", field: `"member_qualifier":null`},
		{name: "case_insensitive_null", field: `"MEMBER_QUALIFIER":null`},
		{name: "null_then_string", field: `"member_qualifier":null,"member_qualifier":"status"`},
		{name: "string_then_null", field: `"member_qualifier":"status","member_qualifier":null`},
		{name: "mixed_case_null_then_string", field: `"MeMbEr_QuAlIfIeR":null,"member_qualifier":"status"`},
	}
	for _, form := range []string{"outer", "scoped_operand"} {
		form := form
		for _, variant := range variants {
			variant := variant
			t.Run(form+"/"+variant.name, func(t *testing.T) {
				raw := rawOutputWithQualifierFields(t, form, variant.field)
				if _, _, err := ParseInterpretationOutputSignals(raw, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}); err == nil {
					t.Fatalf("ParseInterpretationOutputSignals() accepted explicit null qualifier for %s", variant.name)
				}
			})
		}
	}
}

func TestParseInterpretationOutputSignalsRetainsWrongTypeRejections(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"outer", "scoped_operand"} {
		form := form
		for _, value := range []string{"123", `{}`, `[]`} {
			value := value
			t.Run(form+"/"+value, func(t *testing.T) {
				raw := rawOutputWithQualifierFields(t, form, `"member_qualifier":`+value)
				if _, _, err := ParseInterpretationOutputSignals(raw, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}); err == nil {
					t.Fatalf("ParseInterpretationOutputSignals() accepted wrong qualifier type %s", value)
				}
			})
		}
	}
}

func rawOutputWithQualifierValue(t *testing.T, form string, value *string) []byte {
	t.Helper()
	field := ""
	if value != nil {
		encoded, err := json.Marshal(*value)
		if err != nil {
			t.Fatalf("json.Marshal(qualifier) error = %v", err)
		}
		field = `"member_qualifier":` + string(encoded)
	}
	return rawOutputWithQualifierFields(t, form, field)
}

func rawOutputWithQualifierFields(t *testing.T, form, qualifierFields string) []byte {
	t.Helper()
	var expression string
	if form == "outer" {
		expression = `{"kind":"children_of_scope","anchor_terms":["Project Alpha"],"member_kind":"work_item"`
		if qualifierFields != "" {
			expression += "," + qualifierFields
		}
		expression += `}`
	} else {
		operand := `{"kind":"children_of_scope","anchor_terms":["Project Alpha"],"member_kind":"work_item"`
		if qualifierFields != "" {
			operand += "," + qualifierFields
		}
		operand += `}`
		expression = `{"kind":"explicit_set","operands":[{"kind":"named_subject","terms":["Project Alpha"]},` + operand + `]}`
	}
	frame := `{"goals":["assess_state"],"subject_expression":` + expression + `,"temporal":"current"}`
	base, err := json.Marshal(validInterpretationOutput())
	if err != nil {
		t.Fatalf("json.Marshal(base output) error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(base, &fields); err != nil {
		t.Fatalf("json.Unmarshal(base output) error = %v", err)
	}
	fields["question_frame"] = json.RawMessage(frame)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("json.Marshal(output fields) error = %v", err)
	}
	return encoded
}

func qualifierFromCapture(t *testing.T, capture InterpretationOutputCapture, form string) (contextfabric.MemberQualifier, bool) {
	t.Helper()
	if capture.Frame.Frame.SubjectExpression == (contextfabric.SubjectExpression{}) {
		t.Fatal("capture has no subject expression")
	}
	if form == "outer" {
		return capture.Frame.Frame.SubjectExpression.MemberQualifier()
	}
	operands := capture.Frame.Frame.SubjectExpression.Explicit.Operands
	if len(operands) != 2 || operands[1].Scoped == nil {
		t.Fatalf("scoped operands = %#v", operands)
	}
	return operands[1].MemberQualifier()
}

func stringPtr(value string) *string { return &value }
