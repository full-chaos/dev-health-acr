package contextfabric

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestMemberQualifierVocabularyAndSanitizer(t *testing.T) {
	t.Parallel()
	wantVocabulary := [MemberQualifierCount]MemberQualifier{MemberQualifierStatus, MemberQualifierAssignee}
	if got := MemberQualifierVocabulary(); got != wantVocabulary {
		t.Fatalf("MemberQualifierVocabulary() = %v, want %v", got, wantVocabulary)
	}
	if !ValidMemberQualifier("") {
		t.Fatal("the empty qualifier must remain valid as unqualified membership")
	}
	for _, member := range wantVocabulary {
		if !ValidMemberQualifier(member) {
			t.Errorf("ValidMemberQualifier(%q) = false", member)
		}
		if got, unrecognized := SanitizeMemberQualifier(string(member)); got != member || unrecognized {
			t.Errorf("SanitizeMemberQualifier(%q) = %q/%v, want %q/false", member, got, unrecognized, member)
		}
	}
	for _, testCase := range []struct {
		raw          string
		want         MemberQualifier
		unrecognized bool
	}{
		{raw: "", want: ""},
		{raw: "  assignee  ", want: MemberQualifierAssignee},
		{raw: "future_filter", want: MemberQualifierUnrecognized, unrecognized: true},
		{raw: string(MemberQualifierUnrecognized), want: MemberQualifierUnrecognized, unrecognized: true},
	} {
		got, unrecognized := SanitizeMemberQualifier(testCase.raw)
		if got != testCase.want || unrecognized != testCase.unrecognized {
			t.Errorf("SanitizeMemberQualifier(%q) = %q/%v, want %q/%v", testCase.raw, got, unrecognized, testCase.want, testCase.unrecognized)
		}
	}
	if ValidMemberQualifier(MemberQualifier("future_filter")) {
		t.Fatal("an invented qualifier must not validate as a frame value")
	}
}

func TestMemberQualifierIsDeclaredAsAnOuterScopeInvariantInput(t *testing.T) {
	t.Parallel()
	for _, spec := range FrameInvariantSpecs() {
		if spec.ID != FrameInvariantI5 {
			continue
		}
		for _, field := range spec.Reads {
			if field == FrameFieldMemberQualifier {
				return
			}
		}
		t.Fatal("I5 does not declare member_qualifier among its model-emitted inputs")
	}
	t.Fatal("I5 is missing from the frame invariant table")
}

func TestMemberQualifierTelemetryPreservesPresence(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		qualifier MemberQualifier
	}{
		{name: "unqualified", qualifier: ""},
		{name: "status", qualifier: MemberQualifierStatus},
		{name: "unknown", qualifier: MemberQualifierUnrecognized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			state := semanticFixture(t)
			state.Frame.SubjectExpression = SubjectExpression{
				Kind: SubjectExpressionChildrenOfScope,
				Scoped: &ScopedSetExpression{
					AnchorTerms:     []string{"Project Alpha"},
					MemberKind:      SubjectWorkItem,
					MemberQualifier: testCase.qualifier,
				},
			}

			var buf strings.Builder
			sink := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, nil)))
			sink.RecordSemanticStatePersistence(context.Background(), acceptancePrincipal(), SemanticStatePersistenceEvent{
				ResultID: "qualifier-telemetry",
				Site:     BudgetAssertDecisive,
				Decision: SemanticStatePersisted,
				State:    state,
			})
			var line map[string]any
			if err := json.Unmarshal([]byte(buf.String()), &line); err != nil {
				t.Fatalf("telemetry JSON = %q: %v", buf.String(), err)
			}
			group, ok := line["state"].(map[string]any)
			if !ok {
				t.Fatalf("telemetry state group = %#v, want object", line["state"])
			}
			if got := group["subject_member_qualifier"]; got != string(testCase.qualifier) {
				t.Fatalf("subject_member_qualifier = %#v, want %q", got, testCase.qualifier)
			}
		})
	}
}

func TestMemberQualifierTelemetryPreservesScopedOperandPresence(t *testing.T) {
	t.Parallel()
	state := semanticFixture(t)
	state.Frame.SubjectExpression = SubjectExpression{
		Kind: SubjectExpressionExplicitSet,
		Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
			{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"Project Alpha"}}},
			{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{
				AnchorTerms:     []string{"Project Alpha"},
				MemberKind:      SubjectWorkItem,
				MemberQualifier: MemberQualifierStatus,
			}},
		}},
	}
	state.GroupKind = ""
	state.Roles = semanticRoleSlots(state.Frame.SubjectExpression)
	state.Requirements = []SemanticRequirement{}
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("EncodeSemanticState() error = %v", err)
	}
	var buf strings.Builder
	sink := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, nil)))
	sink.RecordSemanticStatePersistence(context.Background(), acceptancePrincipal(), SemanticStatePersistenceEvent{
		ResultID: "qualifier-operand-telemetry",
		Site:     BudgetAssertDecisive,
		Decision: SemanticStatePersisted,
		State:    state,
	})
	if !strings.Contains(buf.String(), "work_item:status") {
		t.Fatalf("telemetry = %s, want the scoped operand qualifier token", buf.String())
	}
}

func TestMemberQualifierTelemetryRejectsInventedToken(t *testing.T) {
	t.Parallel()
	if got := semanticMemberQualifierToken(MemberQualifier("invented_filter")); got != continuationTelemetryUnrecognised {
		t.Fatalf("semanticMemberQualifierToken() = %q, want %q", got, continuationTelemetryUnrecognised)
	}
}

func TestMemberQualifierCarrierSurvivesFrameValidationAndPersistence(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		raw       string
		qualifier MemberQualifier
	}{
		{name: "unqualified", qualifier: ""},
		{name: "status", qualifier: MemberQualifierStatus},
		{name: "assignee", qualifier: MemberQualifierAssignee},
		{name: "unknown", qualifier: MemberQualifierUnrecognized},
		{name: "ascii-whitespace", raw: "   ", qualifier: MemberQualifierUnrecognized},
		{name: "unicode-whitespace", raw: "\u2003\u2003", qualifier: MemberQualifierUnrecognized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.raw != "" {
				got, unrecognized := SanitizeMemberQualifier(testCase.raw)
				if got != testCase.qualifier || !unrecognized {
					t.Fatalf("SanitizeMemberQualifier(%q) = %q/%v, want %q/true", testCase.raw, got, unrecognized, testCase.qualifier)
				}
			}
			frame := QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{
					Kind: SubjectExpressionChildrenOfScope,
					Scoped: &ScopedSetExpression{
						AnchorTerms:     []string{"Project Alpha"},
						MemberKind:      SubjectWorkItem,
						MemberQualifier: testCase.qualifier,
					},
				},
				Temporal: TemporalIntentCurrent,
			}
			result := ValidateFrame(frame, []AnswerObligation{ObligationState}, ShapeDiscoveredCohort)
			if result.Outcome != FrameValidationOutcomeValid {
				t.Fatalf("ValidateFrame() = %q (%v), want valid", result.Outcome, result.Failure)
			}
			gotQualifier, present := result.Frame.SubjectExpression.MemberQualifier()
			if gotQualifier != testCase.qualifier || present != (testCase.qualifier != "") {
				t.Fatalf("validated qualifier = %q/%v, want %q/%v", gotQualifier, present, testCase.qualifier, testCase.qualifier != "")
			}

			state := semanticFixture(t)
			state.Frame = &result.Frame
			state.FramePresent = true
			state.GroupKind = ""
			state.Roles = semanticRoleSlots(result.Frame.SubjectExpression)
			state.Requirements = []SemanticRequirement{}
			encoded, err := EncodeSemanticState(state)
			if err != nil {
				t.Fatalf("EncodeSemanticState() error = %v", err)
			}
			decoded, status := DecodeSemanticState(encoded)
			if status != SemanticStateReadAvailable || decoded == nil {
				t.Fatalf("DecodeSemanticState() = %v/%q, want available", decoded, status)
			}
			decodedQualifier, decodedPresent := decoded.Frame.SubjectExpression.MemberQualifier()
			if decodedQualifier != testCase.qualifier || decodedPresent != (testCase.qualifier != "") {
				t.Fatalf("persisted qualifier = %q/%v, want %q/%v", decodedQualifier, decodedPresent, testCase.qualifier, testCase.qualifier != "")
			}
		})
	}
}

func TestMemberQualifierValidationRejectsUnknownDirectFrameValue(t *testing.T) {
	t.Parallel()
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms:     []string{"Project Alpha"},
				MemberKind:      SubjectWorkItem,
				MemberQualifier: MemberQualifier("invented_filter"),
			},
		},
		Temporal: TemporalIntentCurrent,
	}
	if failure, bad := ValidateFramePhaseA1(frame); bad {
		if failure.Invariant != FrameInvariantI5 || failure.Detail != FrameFailureMemberQualifierInvalid {
			t.Fatalf("ValidateFramePhaseA1() = %+v, want I5/member_qualifier_invalid", failure)
		}
	} else {
		t.Fatal("ValidateFramePhaseA1() accepted an invented member qualifier")
	}
}

func TestMemberQualifierValidationRejectsUnknownScopedOperandValue(t *testing.T) {
	t.Parallel()
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"Project Alpha"}}},
				{
					Kind: SubjectOperandScoped,
					Scoped: &ScopedSetExpression{
						AnchorTerms:     []string{"Project Alpha"},
						MemberKind:      SubjectWorkItem,
						MemberQualifier: MemberQualifier("invented_filter"),
					},
				},
			}},
		},
		Temporal: TemporalIntentCurrent,
	}
	if failure, bad := ValidateFramePhaseA1(frame); bad {
		if failure.Invariant != FrameInvariantI19 || failure.Detail != FrameFailureOperandMemberQualifier {
			t.Fatalf("ValidateFramePhaseA1() = %+v, want I19/operand_member_qualifier_invalid", failure)
		}
	} else {
		t.Fatal("ValidateFramePhaseA1() accepted an invented scoped operand qualifier")
	}
}

func TestMemberQualifierSemanticCodecRejectsInventedOuterValue(t *testing.T) {
	t.Parallel()
	state := semanticFixture(t)
	state.Frame.SubjectExpression = SubjectExpression{
		Kind: SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{
			AnchorTerms:     []string{"Project Alpha"},
			MemberKind:      SubjectWorkItem,
			MemberQualifier: MemberQualifier("invented_filter"),
		},
	}
	state.GroupKind = ""
	state.Roles = semanticRoleSlots(state.Frame.SubjectExpression)
	state.Requirements = []SemanticRequirement{}
	if _, err := EncodeSemanticState(state); err == nil || !strings.Contains(err.Error(), "scoped member_qualifier") {
		t.Fatalf("EncodeSemanticState() error = %v, want the outer scoped qualifier validity rejection", err)
	}
}

func TestMemberQualifierSemanticCodecRejectsInventedOperandValue(t *testing.T) {
	t.Parallel()
	state := semanticFixture(t)
	state.Frame.SubjectExpression = SubjectExpression{
		Kind: SubjectExpressionExplicitSet,
		Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
			{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"Project Alpha"}}},
			{
				Kind: SubjectOperandScoped,
				Scoped: &ScopedSetExpression{
					AnchorTerms:     []string{"Project Alpha"},
					MemberKind:      SubjectWorkItem,
					MemberQualifier: MemberQualifier("invented_filter"),
				},
			},
		}},
	}
	state.GroupKind = ""
	state.Roles = semanticRoleSlots(state.Frame.SubjectExpression)
	state.Requirements = []SemanticRequirement{}
	if _, err := EncodeSemanticState(state); err == nil || !strings.Contains(err.Error(), "operand scoped member_qualifier") {
		t.Fatalf("EncodeSemanticState() error = %v, want the scoped operand qualifier validity rejection", err)
	}
}

func TestScopedOperandWellFormedRejectsInventedQualifier(t *testing.T) {
	t.Parallel()
	operand := SubjectOperand{
		Kind: SubjectOperandScoped,
		Scoped: &ScopedSetExpression{
			AnchorTerms:     []string{"Project Alpha"},
			MemberKind:      SubjectWorkItem,
			MemberQualifier: MemberQualifier("invented_filter"),
		},
	}
	if subjectOperandWellFormed(operand) {
		t.Fatal("subjectOperandWellFormed() accepted an invented member qualifier")
	}
}
