package contextfabric

import (
	"strings"
	"testing"
	"unicode/utf8"

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

func TestClarificationPromptStaysWithinPublishedBoundAtMaxLengths(t *testing.T) {
	label := strings.Repeat("l", 512)
	cands := []SubjectCandidate{}
	for _, c := range []string{"a", "b", "c"} {
		cands = append(cands, promptCandidate(contractsv1.ContextFabricSubjectProject, strings.Repeat(c, 256), label, "jira"))
	}
	got := ClarificationPrompt(cands)
	if n := utf8.RuneCountInString(got); n > contractsv1.ContextFabricProjectedClarificationPromptMaxLength {
		t.Fatalf("prompt is %d runes, bound %d", n, contractsv1.ContextFabricProjectedClarificationPromptMaxLength)
	}
	if !strings.Contains(got, "#1") || !strings.Contains(got, "#2") || !strings.Contains(got, "#3") {
		t.Fatalf("identical choices not told apart: %q", got)
	}
}

func TestClarificationPromptKeepsIDCueWhenWithinBound(t *testing.T) {
	label := strings.Repeat("l", 100)
	got := ClarificationPrompt([]SubjectCandidate{
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-a", label, "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-b", label, "jira"),
	})
	if !strings.Contains(got, "id-a") || strings.Contains(got, "#") {
		t.Fatalf("id cue lost: %q", got)
	}
}

func TestClarificationPromptOrdinalFallbackLeavesUniqueLabelsAlone(t *testing.T) {
	label := strings.Repeat("l", 512)
	unique := strings.Repeat("p", 512)
	got := ClarificationPrompt([]SubjectCandidate{
		promptCandidate(contractsv1.ContextFabricSubjectProject, strings.Repeat("a", 256), label, "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectProject, strings.Repeat("b", 256), label, "jira"),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-c", unique, "jira"),
	})
	if utf8.RuneCountInString(got) > contractsv1.ContextFabricProjectedClarificationPromptMaxLength {
		t.Fatalf("prompt over bound: %d", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, ", "+unique+"?") {
		t.Fatalf("unique label altered: %q", got[len(got)-40:])
	}
}
