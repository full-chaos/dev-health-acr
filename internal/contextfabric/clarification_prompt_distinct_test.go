package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func promptCandidate(kind contractsv1.ContextFabricSubjectKind, id, label, provider string) SubjectCandidate {
	return SubjectCandidate{
		Subject:  contractsv1.ContextFabricSubjectRef{Kind: kind, CanonicalID: id, Label: label},
		Provider: provider,
	}
}

func TestClarificationPromptSeparatesSameKindSameProviderSameLabel(t *testing.T) {
	got := ClarificationPrompt([]SubjectCandidate{
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-a", "payments", "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-b", "payments", "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-c", "platform", "jira"),
	})
	want := "Which subject did you mean: payments (jira, id-a), payments (jira, id-b), platform?"
	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestClarificationPromptSameCanonicalIDRepeatStaysOneLabel(t *testing.T) {
	got := ClarificationPrompt([]SubjectCandidate{
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-a", "payments", "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-a", "payments", "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-c", "platform", "jira"),
	})
	if strings.Contains(got, "id-a") {
		t.Fatalf("same-id repeat got a distinguishing cue: %q", got)
	}
}

func TestClarificationPromptUniqueLabelsUnchanged(t *testing.T) {
	got := ClarificationPrompt([]SubjectCandidate{
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-a", "payments", "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectTeam, "id-c", "platform", "github"),
	})
	if want := "Which subject did you mean: payments, platform?"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestClarificationPromptProviderCueStillSeparatesWithoutID(t *testing.T) {
	got := ClarificationPrompt([]SubjectCandidate{
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-a", "payments", "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-b", "payments", "github"),
	})
	if want := "Which subject did you mean: payments (jira), payments (github)?"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}
