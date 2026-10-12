package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func memberStatusQuestionFrame(kind SubjectKind, value string) QuestionFrame {
	frame := pullRequestStatusQuestionFrame(value)
	frame.SubjectExpression.Scoped.MemberKind = kind
	return frame
}

// TestMemberStatusValuesTheFrameAdmits: phase A1 admits exactly the closed set
// of the member kind beside a status qualifier, refuses the question's own
// wording and another kind's value, and refuses every status qualifier on team
// members, which have no state.
func TestMemberStatusValuesTheFrameAdmits(t *testing.T) {
	sets := map[SubjectKind][]string{
		SubjectProject:  {"backlog", "planned", "started", "paused", "completed", "canceled"},
		SubjectIncident: {"active", "open", "acknowledged", "resolved", "closed", "suppressed"},
	}
	for kind, set := range sets {
		if got := MemberStatusVocabulary(kind); strings.Join(got, ",") != strings.Join(set, ",") {
			t.Fatalf("%s vocabulary = %v, want %v", kind, got, set)
		}
		for _, value := range set {
			if failure, bad := ValidateFramePhaseA1(memberStatusQuestionFrame(kind, value)); bad {
				t.Errorf("%s status %q of the closed set refused: %+v", kind, value, failure)
			}
		}
		if _, bad := ValidateFramePhaseA1(memberStatusQuestionFrame(kind, "")); bad {
			t.Errorf("%s: a status qualifier without a value refused", kind)
		}
		for _, value := range []string{"blocked", "in progress", "Open ", "merged", "awaiting_review"} {
			if InMemberStatusVocabulary(kind, value) {
				continue
			}
			failure, bad := ValidateFramePhaseA1(memberStatusQuestionFrame(kind, value))
			if !bad || failure.Invariant != FrameInvariantI5 || failure.Detail != FrameFailureMemberQualifierValueInvalid {
				t.Errorf("%s value %q outside the set: refused=%v failure=%+v", kind, value, bad, failure)
			}
		}
	}
	for _, value := range []string{"", "active", "open", "started"} {
		failure, bad := ValidateFramePhaseA1(memberStatusQuestionFrame(SubjectTeam, value))
		if !bad || failure.Invariant != FrameInvariantI5 || failure.Detail != FrameFailureMemberQualifierValueInvalid {
			t.Errorf("team status %q: refused=%v failure=%+v, want i5 %s", value, bad, failure, FrameFailureMemberQualifierValueInvalid)
		}
	}
	team := memberStatusQuestionFrame(SubjectTeam, "")
	team.SubjectExpression.Scoped.MemberQualifier = MemberQualifierAssignee
	team.SubjectExpression.Scoped.MemberQualifierValue = "Vesper"
	if _, bad := ValidateFramePhaseA1(team); bad {
		t.Errorf("an assignee qualifier on team members was refused")
	}
	operand := QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
		{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: SubjectTeam, MemberQualifier: MemberQualifierStatus, MemberQualifierValue: "active"}},
		{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"Marram"}}},
	}}}}
	if failure, bad := ValidateFramePhaseA1(operand); !bad || failure.Detail != FrameFailureOperandMemberQualifierValue {
		t.Errorf("a team status on an explicit-set operand: refused=%v failure=%+v", bad, failure)
	}
}

func TestMemberStatusFilterOfNamesTheKindAndValue(t *testing.T) {
	project := memberStatusQuestionFrame(SubjectProject, "paused")
	if kind, value := MemberStatusFilterOf(&project); kind != SubjectProject || value != "paused" {
		t.Fatalf("project filter = %q %q", kind, value)
	}
	for _, frame := range []QuestionFrame{
		memberStatusQuestionFrame(SubjectProject, ""),
		memberStatusQuestionFrame(SubjectProject, "blocked"),
		memberStatusQuestionFrame(SubjectPullRequest, "open"),
		memberStatusQuestionFrame(SubjectTeam, "active"),
	} {
		if kind, value := MemberStatusFilterOf(&frame); kind != "" || value != "" {
			t.Fatalf("filter %q %q for %+v, want none", kind, value, frame.SubjectExpression.Scoped)
		}
	}
	if kind, value := MemberStatusFilterOf(nil); kind != "" || value != "" {
		t.Fatalf("a nil frame has a filter")
	}
}

// TestServedMembersStateTheirStatusFilter: a document serving project or
// incident members states the status it was filtered by, once, in a sentence
// the contract recognises as service-authored; one whose question named a state
// no read applies says so; a document that serves another kind carries none.
func TestServedMembersStateTheirStatusFilter(t *testing.T) {
	for _, kind := range []SubjectKind{SubjectProject, SubjectIncident} {
		served := InvestigationResult{Cohort: &Cohort{Kind: kind}}
		for _, value := range MemberStatusVocabulary(kind) {
			frame := memberStatusQuestionFrame(kind, value)
			result := withMemberStatusDisclosure(withMemberStatusDisclosure(served, &frame), &frame)
			want := contractsv1.ContextFabricMemberStatusFilterLimitation(string(kind), value)
			if want == "" || len(result.Limitations) != 1 || result.Limitations[0] != want {
				t.Fatalf("%s status %q: limitations = %q, want exactly [%q]", kind, value, result.Limitations, want)
			}
			if !contractsv1.IsContextFabricServiceAuthoredLimitation(want) {
				t.Fatalf("%s status %q: the disclosure is not recognised as service-authored", kind, value)
			}
		}
		unapplied := memberStatusQuestionFrame(kind, "")
		result := withMemberStatusDisclosure(served, &unapplied)
		want := contractsv1.ContextFabricMemberStatusUnappliedLimitation(string(kind))
		if len(result.Limitations) != 1 || result.Limitations[0] != want || !contractsv1.IsContextFabricServiceAuthoredLimitation(want) {
			t.Fatalf("%s unapplied status: limitations = %q, want [%q]", kind, result.Limitations, want)
		}
		for _, value := range MemberStatusVocabulary(kind) {
			if !strings.Contains(want, strings.ReplaceAll(value, "_", " ")) {
				t.Fatalf("%s: the unapplied sentence %q does not name %q of the set", kind, want, value)
			}
		}
		other := InvestigationResult{Cohort: &Cohort{Kind: SubjectRepository}}
		frame := memberStatusQuestionFrame(kind, MemberStatusVocabulary(kind)[0])
		if got := withMemberStatusDisclosure(other, &frame).Limitations; len(got) != 0 {
			t.Fatalf("a repository cohort carried a %s disclosure: %q", kind, got)
		}
	}
}

// TestMembersOutsideTheKnownSetAreCounted: the count of members whose stored
// status is outside the set is a sentence of its own, once, the contract
// recognises as service-authored, and an empty count says nothing.
func TestMembersOutsideTheKnownSetAreCounted(t *testing.T) {
	cases := map[SubjectKind]string{
		SubjectProject:  "4 projects have a state outside the known set and were not matched.",
		SubjectIncident: "4 incidents have a status outside the known set and were not matched.",
	}
	for kind, want := range cases {
		outside := MemberStatusOutsideSet{Kind: kind, Count: 4}
		result := withMemberStatusOutsideSet(withMemberStatusOutsideSet(InvestigationResult{}, outside), outside)
		if len(result.Limitations) != 1 || result.Limitations[0] != want {
			t.Fatalf("%s: limitations = %q, want [%q]", kind, result.Limitations, want)
		}
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(want) {
			t.Fatalf("%s: the count sentence is not recognised as service-authored", kind)
		}
		if got := withMemberStatusOutsideSet(InvestigationResult{}, MemberStatusOutsideSet{Kind: kind}).Limitations; len(got) != 0 {
			t.Fatalf("%s: a zero count stated %q", kind, got)
		}
	}
	if got := withMemberStatusOutsideSet(InvestigationResult{}, MemberStatusOutsideSet{Kind: SubjectTeam, Count: 3}).Limitations; len(got) != 0 {
		t.Fatalf("a team count stated %q", got)
	}
}

// TestMemberStatusRecognizerMatchesOnlyTheGeneratedSentences: a lookalike a
// model could author (a status outside the closed set, a kind or article that
// does not match, a zero or padded count, a changed vocabulary) is not
// recognised as service-authored.
func TestMemberStatusRecognizerMatchesOnlyTheGeneratedSentences(t *testing.T) {
	for _, kind := range []SubjectKind{SubjectProject, SubjectIncident} {
		for _, status := range MemberStatusVocabulary(kind) {
			if !contractsv1.IsContextFabricMemberStatusLimitation(contractsv1.ContextFabricMemberStatusFilterLimitation(string(kind), status)) {
				t.Errorf("%s %q: the generated filter sentence is not recognised", kind, status)
			}
		}
		for _, count := range []int{1, 7, 25} {
			if !contractsv1.IsContextFabricMemberStatusLimitation(contractsv1.ContextFabricMemberStatusOutsideSetLimitation(string(kind), count)) {
				t.Errorf("%s %d: the generated count sentence is not recognised", kind, count)
			}
		}
		if !contractsv1.IsContextFabricMemberStatusLimitation(contractsv1.ContextFabricMemberStatusUnappliedLimitation(string(kind))) {
			t.Errorf("%s: the generated unapplied sentence is not recognised", kind)
		}
	}
	for _, lookalike := range []string{
		"Members are the projects whose current state is bogus; it is read as of now, over no period, and is not completion, readiness or health.",
		"Members are the projects whose current state is open; it is read as of now, over no period, and is not completion, readiness or health.",
		"Members are the incidents whose current status is started; it is read as of now, over no period, and is not completion, readiness or health.",
		"Members are the projects whose current status is started; it is read as of now, over no period, and is not completion, readiness or health.",
		"0 projects have a state outside the known set and were not matched.",
		"07 projects have a state outside the known set and were not matched.",
		"3 projects have a status outside the known set and were not matched.",
		"3 incidents have a state outside the known set and were not matched.",
		"3 teams have a state outside the known set and were not matched.",
		"Members are not filtered by state: the state the question names is not one the project read applies (any word at all).",
		"Members are not filtered by state: the state the question names is not one the incident read applies (backlog, planned, started, paused, completed or canceled).",
		" 3 projects have a state outside the known set and were not matched.",
	} {
		if contractsv1.IsContextFabricMemberStatusLimitation(lookalike) {
			t.Errorf("lookalike %q was recognised as service-authored", lookalike)
		}
	}
}
