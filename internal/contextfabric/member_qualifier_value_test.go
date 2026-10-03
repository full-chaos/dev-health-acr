package contextfabric

import (
	"strings"
	"testing"
)

func scopedFrameWithValue(kind SubjectKind, qualifier MemberQualifier, value string) QuestionFrame {
	return QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms: []string{"Project Alpha"}, MemberKind: kind,
				MemberQualifier: qualifier, MemberQualifierValue: value,
			},
		},
		Temporal: TemporalIntentCurrent,
	}
}

func TestWorkItemStatusVocabularyIsTheClosedSetOfEight(t *testing.T) {
	t.Parallel()
	want := []string{"backlog", "todo", "in_progress", "in_review", "blocked", "done", "canceled", "unknown"}
	got := WorkItemStatusVocabulary()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("vocabulary = %v, want %v", got, want)
	}
	for _, member := range want {
		if !InWorkItemStatusVocabulary(member) {
			t.Errorf("%q not in vocabulary", member)
		}
	}
	for _, other := range []string{"", "open", "cancelled", "In Progress", "in progress"} {
		if InWorkItemStatusVocabulary(other) {
			t.Errorf("%q must not be in vocabulary", other)
		}
	}
}

func TestValidMemberQualifierValueMatrix(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		kind      SubjectKind
		qualifier MemberQualifier
		value     string
		want      bool
	}{
		{"absent is valid with no qualifier", SubjectWorkItem, "", "", true},
		{"absent is valid with status", SubjectWorkItem, MemberQualifierStatus, "", true},
		{"value without qualifier", SubjectWorkItem, "", "done", false},
		{"value with unrecognized qualifier", SubjectWorkItem, MemberQualifierUnrecognized, "done", false},
		{"work item status in set", SubjectWorkItem, MemberQualifierStatus, "in_progress", true},
		{"work item status outside set", SubjectWorkItem, MemberQualifierStatus, "stuck", false},
		{"work item status wrong case", SubjectWorkItem, MemberQualifierStatus, "Done", false},
		{"pull request status verbatim word", SubjectPullRequest, MemberQualifierStatus, "merged", true},
		{"project status verbatim word", SubjectProject, MemberQualifierStatus, "open", true},
		{"status word of two words", SubjectProject, MemberQualifierStatus, "on hold", true},
		{"status sentence", SubjectProject, MemberQualifierStatus, "please show only those still open", false},
		{"status word with capital", SubjectProject, MemberQualifierStatus, "Open", false},
		{"status word with punctuation", SubjectProject, MemberQualifierStatus, "open!", false},
		{"status word over its bound", SubjectProject, MemberQualifierStatus, strings.Repeat("a", MemberStatusWordMaxRunes+1), false},
		{"assignee name", SubjectWorkItem, MemberQualifierAssignee, "Vesper", true},
		{"assignee name is not checked against the status set", SubjectWorkItem, MemberQualifierAssignee, "stuck", true},
		{"padded value", SubjectProject, MemberQualifierAssignee, " Alice", false},
		{"control character", SubjectProject, MemberQualifierAssignee, "Al\nice", false},
		{"at the length bound", SubjectProject, MemberQualifierAssignee, strings.Repeat("a", MemberQualifierValueMaxRunes), true},
		{"over the length bound", SubjectProject, MemberQualifierAssignee, strings.Repeat("a", MemberQualifierValueMaxRunes+1), false},
	} {
		if got := ValidMemberQualifierValue(c.kind, c.qualifier, c.value); got != c.want {
			t.Errorf("%s: ValidMemberQualifierValue(%q,%q,%q) = %v, want %v", c.name, c.kind, c.qualifier, c.value, got, c.want)
		}
	}
}

func TestSanitizeMemberQualifierValue(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		raw          string
		want         string
		unrecognized bool
	}{
		{"", "", false},
		{"  in_progress ", "in_progress", false},
		{"   ", "", true},
		{"a\x00b", "", true},
		{strings.Repeat("x", MemberQualifierValueMaxRunes+1), "", true},
	} {
		got, unrecognized := SanitizeMemberQualifierValue(c.raw)
		if got != c.want || unrecognized != c.unrecognized {
			t.Errorf("SanitizeMemberQualifierValue(%q) = %q/%v, want %q/%v", c.raw, got, unrecognized, c.want, c.unrecognized)
		}
	}
}

func TestFrameValidationRefusesAnInvalidMemberQualifierValue(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		frame QuestionFrame
	}{
		{"value without qualifier", scopedFrameWithValue(SubjectWorkItem, "", "done")},
		{"work item status outside set", scopedFrameWithValue(SubjectWorkItem, MemberQualifierStatus, "stuck")},
	} {
		failure, bad := ValidateFramePhaseA1(c.frame)
		if !bad || failure.Invariant != FrameInvariantI5 || failure.Detail != FrameFailureMemberQualifierValueInvalid {
			t.Errorf("%s: ValidateFramePhaseA1() = %+v/%v, want I5/member_qualifier_value_invalid", c.name, failure, bad)
		}
	}
	if failure, bad := ValidateFramePhaseA1(scopedFrameWithValue(SubjectWorkItem, MemberQualifierStatus, "in_review")); bad {
		t.Fatalf("a closed-set value was refused: %+v", failure)
	}
}

func TestFrameValidationRefusesAnInvalidScopedOperandValue(t *testing.T) {
	t.Parallel()
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"Project Alpha"}}},
				{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"Project Alpha"}, MemberKind: SubjectWorkItem, MemberQualifier: MemberQualifierStatus, MemberQualifierValue: "stuck"}},
			}},
		},
		Temporal: TemporalIntentCurrent,
	}
	failure, bad := ValidateFramePhaseA1(frame)
	if !bad || failure.Invariant != FrameInvariantI19 || failure.Detail != FrameFailureOperandMemberQualifierValue {
		t.Fatalf("ValidateFramePhaseA1() = %+v/%v, want I19/operand_member_qualifier_value_invalid", failure, bad)
	}
}

func TestMemberQualifierValueSurvivesPersistedSemanticState(t *testing.T) {
	t.Parallel()
	frame := scopedFrameWithValue(SubjectWorkItem, MemberQualifierStatus, "blocked")
	result := ValidateFrame(frame, []AnswerObligation{ObligationState}, ShapeDiscoveredCohort)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("ValidateFrame() = %q (%v)", result.Outcome, result.Failure)
	}
	state := semanticFixture(t)
	state.Frame = &result.Frame
	state.FramePresent = true
	state.GroupKind = ""
	state.Roles = semanticRoleSlots(result.Frame.SubjectExpression)
	state.Requirements = []SemanticRequirement{}
	encoded, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("EncodeSemanticState: %v", err)
	}
	decoded, status := DecodeSemanticState(encoded)
	if status != SemanticStateReadAvailable || decoded == nil {
		t.Fatalf("DecodeSemanticState = %v/%q", decoded, status)
	}
	if got := decoded.Frame.SubjectExpression.Scoped.MemberQualifierValue; got != "blocked" {
		t.Fatalf("persisted value = %q, want blocked", got)
	}

	// A stored state whose value no longer validates is not read as available.
	bad := strings.Replace(string(encoded), `"member_qualifier_value":"blocked"`, `"member_qualifier_value":"stuck"`, 1)
	if bad == string(encoded) {
		t.Fatalf("encoded state carries no member_qualifier_value: %s", encoded)
	}
	if _, status := DecodeSemanticState([]byte(bad)); status == SemanticStateReadAvailable {
		t.Fatal("DecodeSemanticState accepted a stored work_item status outside the closed set")
	}
}
