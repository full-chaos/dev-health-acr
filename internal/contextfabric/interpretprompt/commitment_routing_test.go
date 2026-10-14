package interpretprompt

import (
	"strings"
	"testing"
)

func TestScheduleQuestionsAreRoutedToCommitment(t *testing.T) {
	t.Parallel()
	const want = `"on track", "on schedule", "behind", "delayed", "miss targets", "at risk": actual_completion, status, commitment.`
	if !strings.Contains(interpretationFactRequirementRules, want) {
		t.Fatalf("the interpretation prompt does not route an on track, behind or at risk question to commitment beside actual_completion and status")
	}
	if !strings.Contains(interpretationSystemPrompt, want) {
		t.Fatal("the rendered interpretation prompt does not carry the commitment routing row")
	}
}
