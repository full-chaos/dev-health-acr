package interpretprompt

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func pullRequestStatusFrame(value string) contextfabric.QuestionFrame {
	return contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Vesper"}, MemberKind: contextfabric.SubjectPullRequest, MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: value},
		},
	}
}

// TestInterpretationPromptMapsEveryPullRequestStatusOntoTheSet: the prompt
// names each value of the closed set as a pull request status, and maps the
// review phrases it lists onto a value the frame admits.
func TestInterpretationPromptMapsEveryPullRequestStatusOntoTheSet(t *testing.T) {
	prompt := System()
	sentence := `For member_kind pull_request and member_qualifier "status" it is EXACTLY one of ` + strings.Join(contextfabric.PullRequestStatusVocabulary(), ", ") + ":"
	if !strings.Contains(prompt, sentence) {
		t.Fatalf("the interpretation prompt does not state the pull request status set as %q", sentence)
	}
	for phrase, value := range map[string]string{
		`"waiting on review"`:      contextfabric.PullRequestStatusAwaitingReview,
		`"no review yet"`:          contextfabric.PullRequestStatusAwaitingReview,
		`"changes requested"`:      contextfabric.PullRequestStatusChangesRequested,
		`"approved"`:               contextfabric.PullRequestStatusApproved,
		`"closed without merging"`: contextfabric.PullRequestStatusClosed,
	} {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("the prompt does not map the phrase %s", phrase)
		}
		if _, bad := contextfabric.ValidateFramePhaseA1(pullRequestStatusFrame(value)); bad {
			t.Errorf("the prompt maps %s onto %q, which the frame refuses", phrase, value)
		}
	}
}

// TestInterpretationPromptMapsProjectAndIncidentStatusOntoTheirSets: the prompt
// names each value of the closed set of a project and of an incident status,
// maps its phrases onto values the frame admits, and tells the model a team
// takes no status qualifier.
func TestInterpretationPromptMapsProjectAndIncidentStatusOntoTheirSets(t *testing.T) {
	prompt := System()
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectIncident} {
		sentence := `For member_kind ` + string(kind) + ` and member_qualifier "status" it is EXACTLY one of ` + strings.Join(contextfabric.MemberStatusVocabulary(kind), ", ") + ":"
		if !strings.Contains(prompt, sentence) {
			t.Fatalf("the interpretation prompt does not state the %s status set as %q", kind, sentence)
		}
	}
	for _, mapping := range []struct {
		kind   contextfabric.SubjectKind
		phrase string
		value  string
	}{
		{contextfabric.SubjectProject, `"in progress"`, "started"},
		{contextfabric.SubjectProject, `"on hold"`, "paused"},
		{contextfabric.SubjectProject, `"shipped"`, "completed"},
		{contextfabric.SubjectProject, `"dropped"`, "canceled"},
		{contextfabric.SubjectIncident, `"unresolved"`, "open"},
		{contextfabric.SubjectIncident, `"fixed"`, "resolved"},
		{contextfabric.SubjectIncident, `"muted"`, "suppressed"},
	} {
		if !strings.Contains(prompt, mapping.phrase) {
			t.Errorf("the prompt does not map the phrase %s", mapping.phrase)
		}
		frame := pullRequestStatusFrame(mapping.value)
		frame.SubjectExpression.Scoped.MemberKind = mapping.kind
		if _, bad := contextfabric.ValidateFramePhaseA1(frame); bad {
			t.Errorf("the prompt maps %s onto %q, which the frame refuses for %s", mapping.phrase, mapping.value, mapping.kind)
		}
	}
	if !strings.Contains(prompt, `Team members never take "status"`) {
		t.Errorf("the prompt does not tell the model a team takes no status qualifier")
	}
}
