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
