package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func pullRequestStatusQuestionFrame(value string) QuestionFrame {
	return QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: SubjectPullRequest, MemberQualifier: MemberQualifierStatus, MemberQualifierValue: value},
		},
	}
}

// TestPullRequestStatusValuesTheFrameAdmits: phase A1 admits exactly the
// closed pull request status set beside a status qualifier, and refuses the
// question's own wording of a state, however short.
func TestPullRequestStatusValuesTheFrameAdmits(t *testing.T) {
	for _, value := range PullRequestStatusVocabulary() {
		if failure, bad := ValidateFramePhaseA1(pullRequestStatusQuestionFrame(value)); bad {
			t.Errorf("status %q of the closed set refused: %+v", value, failure)
		}
	}
	if _, bad := ValidateFramePhaseA1(pullRequestStatusQuestionFrame("")); bad {
		t.Errorf("a status qualifier without a value refused")
	}
	for _, value := range []string{"waiting on review", "no review yet", "stuck in review", "in review", "behind", "draft", "Open"} {
		failure, bad := ValidateFramePhaseA1(pullRequestStatusQuestionFrame(value))
		if !bad || failure.Invariant != FrameInvariantI5 || failure.Detail != FrameFailureMemberQualifierValueInvalid {
			t.Errorf("value %q outside the set: refused=%v failure=%+v, want i5 %s", value, bad, failure, FrameFailureMemberQualifierValueInvalid)
		}
	}
	project := pullRequestStatusQuestionFrame("open")
	project.SubjectExpression.Scoped.MemberKind = SubjectProject
	if _, bad := ValidateFramePhaseA1(project); !bad {
		t.Errorf("a pull request status value was admitted beside project members")
	}
}

func TestPullRequestStatusMatches(t *testing.T) {
	approve := PullRequestReviewRow{State: "APPROVED", SubmittedNs: 10}
	request := PullRequestReviewRow{State: "CHANGES_REQUESTED", SubmittedNs: 20}
	cases := []struct {
		name    string
		value   string
		state   string
		reviews []PullRequestReviewRow
		want    bool
	}{
		{"open state", PullRequestStatusOpen, "open", nil, true},
		{"open state compares exactly, no case folding", PullRequestStatusOpen, " OPEN ", nil, false},
		{"merged is not open", PullRequestStatusOpen, "merged", nil, false},
		{"merged state", PullRequestStatusMerged, "merged", nil, true},
		{"closed state", PullRequestStatusClosed, "closed", nil, true},
		{"merged is not closed", PullRequestStatusClosed, "merged", nil, false},
		{"awaiting: open with no review", PullRequestStatusAwaitingReview, "open", nil, true},
		{"awaiting: a pending review is not submitted", PullRequestStatusAwaitingReview, "open", []PullRequestReviewRow{{State: "PENDING", SubmittedNs: 5}}, true},
		{"awaiting: a comment is a review", PullRequestStatusAwaitingReview, "open", []PullRequestReviewRow{{State: "COMMENTED", SubmittedNs: 5}}, false},
		{"awaiting: a dismissed review is a review", PullRequestStatusAwaitingReview, "open", []PullRequestReviewRow{{State: "DISMISSED", SubmittedNs: 5}}, false},
		{"awaiting: not open", PullRequestStatusAwaitingReview, "merged", nil, false},
		{"approved: latest deciding review approves", PullRequestStatusApproved, "open", []PullRequestReviewRow{{State: "CHANGES_REQUESTED", SubmittedNs: 5}, approve}, true},
		{"approved: a later request wins", PullRequestStatusApproved, "open", []PullRequestReviewRow{approve, request}, false},
		{"approved: a later comment does not decide", PullRequestStatusApproved, "open", []PullRequestReviewRow{approve, {State: "COMMENTED", SubmittedNs: 30}}, true},
		{"approved: a later dismissal does not decide", PullRequestStatusApproved, "open", []PullRequestReviewRow{approve, {State: "DISMISSED", SubmittedNs: 30}}, true},
		{"approved: not open", PullRequestStatusApproved, "merged", []PullRequestReviewRow{approve}, false},
		{"approved: no deciding review", PullRequestStatusApproved, "open", []PullRequestReviewRow{{State: "COMMENTED", SubmittedNs: 30}}, false},
		{"changes requested: latest deciding review requests", PullRequestStatusChangesRequested, "open", []PullRequestReviewRow{approve, request}, true},
		{"changes requested: lower case state", PullRequestStatusChangesRequested, "open", []PullRequestReviewRow{{State: "changes_requested", SubmittedNs: 1}}, true},
		{"a tie at the latest instant decides nothing", PullRequestStatusApproved, "open", []PullRequestReviewRow{{State: "APPROVED", SubmittedNs: 30}, {State: "CHANGES_REQUESTED", SubmittedNs: 30}}, false},
		{"a tie decides nothing either way", PullRequestStatusChangesRequested, "open", []PullRequestReviewRow{{State: "CHANGES_REQUESTED", SubmittedNs: 30}, {State: "APPROVED", SubmittedNs: 30}}, false},
		{"a value outside the set", "behind", "open", nil, false},
	}
	for _, tc := range cases {
		if got := PullRequestStatusMatches(tc.value, tc.state, tc.reviews); got != tc.want {
			t.Errorf("%s: PullRequestStatusMatches(%q, %q, %v) = %v, want %v", tc.name, tc.value, tc.state, tc.reviews, got, tc.want)
		}
	}
}

func TestPullRequestStatusFilterOf(t *testing.T) {
	frame := pullRequestStatusQuestionFrame(PullRequestStatusAwaitingReview)
	if got := PullRequestStatusFilterOf(&frame); got != PullRequestStatusAwaitingReview {
		t.Fatalf("filter = %q, want %q", got, PullRequestStatusAwaitingReview)
	}
	project := pullRequestStatusQuestionFrame(PullRequestStatusOpen)
	project.SubjectExpression.Scoped.MemberKind = SubjectProject
	if got := PullRequestStatusFilterOf(&project); got != "" {
		t.Fatalf("a project status was taken as a pull request filter: %q", got)
	}
	empty := pullRequestStatusQuestionFrame("")
	if got := PullRequestStatusFilterOf(&empty); got != "" || !PullRequestStatusUnappliedOf(&empty) {
		t.Fatalf("a status without a value: filter %q, unapplied %v; want no filter and unapplied", got, PullRequestStatusUnappliedOf(&empty))
	}
	if PullRequestStatusUnappliedOf(&frame) {
		t.Fatalf("an applied status reported as unapplied")
	}
	set := QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
		{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: SubjectPullRequest, MemberQualifier: MemberQualifierStatus, MemberQualifierValue: PullRequestStatusOpen}},
	}}}}
	if PullRequestStatusFilterOf(&set) != "" || !PullRequestStatusUnappliedOf(&set) {
		t.Fatalf("an operand status: want no filter and unapplied")
	}
}

// TestServedPullRequestMembersStateTheirStatusFilter: a document serving pull
// request members states the status it was filtered by, once, in a sentence
// the contract recognises as service-authored; one whose question named a
// state no read applies says so.
func TestServedPullRequestMembersStateTheirStatusFilter(t *testing.T) {
	served := InvestigationResult{Cohort: &Cohort{Kind: SubjectPullRequest}}
	for _, value := range PullRequestStatusVocabulary() {
		frame := pullRequestStatusQuestionFrame(value)
		result := withMemberStatusDisclosure(withMemberStatusDisclosure(served, &frame), &frame)
		want := contractsv1.ContextFabricPullRequestStatusFilterLimitation(value)
		if want == "" || len(result.Limitations) != 1 || result.Limitations[0] != want {
			t.Fatalf("status %q: limitations = %q, want exactly [%q]", value, result.Limitations, want)
		}
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(want) {
			t.Fatalf("status %q: the disclosure is not recognised as service-authored", value)
		}
	}
	unapplied := pullRequestStatusQuestionFrame("")
	result := withMemberStatusDisclosure(served, &unapplied)
	if len(result.Limitations) != 1 || result.Limitations[0] != contractsv1.ContextFabricPullRequestStatusUnappliedLimitation {
		t.Fatalf("unapplied status: limitations = %q", result.Limitations)
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(contractsv1.ContextFabricPullRequestStatusUnappliedLimitation) {
		t.Fatalf("the unapplied disclosure is not recognised as service-authored")
	}
	operands := QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
		{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: SubjectPullRequest, MemberQualifier: MemberQualifierStatus, MemberQualifierValue: PullRequestStatusAwaitingReview}},
		{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"Marram"}}},
	}}}}
	for _, document := range []InvestigationResult{{}, {Cohort: &Cohort{Kind: SubjectTeam}}} {
		got := withMemberStatusDisclosure(document, &operands).Limitations
		if len(got) != 1 || got[0] != contractsv1.ContextFabricPullRequestStatusUnappliedLimitation {
			t.Fatalf("an explicit-set operand status on a document with cohort %+v: limitations = %q, want the unapplied sentence", document.Cohort, got)
		}
	}
	frame := pullRequestStatusQuestionFrame(PullRequestStatusOpen)
	projects := InvestigationResult{Cohort: &Cohort{Kind: SubjectProject}}
	if got := withMemberStatusDisclosure(projects, &frame).Limitations; len(got) != 0 {
		t.Fatalf("a project cohort carried a pull request disclosure: %q", got)
	}
}
