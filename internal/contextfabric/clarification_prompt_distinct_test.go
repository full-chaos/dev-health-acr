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
	want := "Which subject did you mean: payments (jira, #1), payments (jira, #2), platform?"
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

// A canonical id means nothing to a reader, and a fragment of one less
// (CHAOS-12468): two subjects with one label take an ordinal.
func TestClarificationPromptNeverShowsAnIDFragment(t *testing.T) {
	cases := []struct {
		name, a, b, label, provider, want string
	}{
		{"ci run", "ci_pipeline_run.v2:abc:de5%3A4242", "ci_pipeline_run.v2:abd:ff1%3A4242", "CI 4242", "", "CI 4242 (#1), CI 4242 (#2)"},
		{"work item", "work_item.v2:0000:a%3ACHAOS-70", "work_item.v2:0000:b%3ACHAOS-70", "Split the job", "", "Split the job (#1), Split the job (#2)"},
		{"project", "jira:project:0000000012345678", "jira:project:0000000099995678", "payments", "jira", "payments (jira, #1), payments (jira, #2)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClarificationPrompt([]SubjectCandidate{
				promptCandidate(contractsv1.ContextFabricSubjectProject, tc.a, tc.label, tc.provider),
				promptCandidate(contractsv1.ContextFabricSubjectProject, tc.b, tc.label, tc.provider),
			})
			if want := "Which subject did you mean: " + tc.want + "?"; got != want {
				t.Fatalf("prompt = %q, want %q", got, want)
			}
			for _, id := range []string{tc.a, tc.b} {
				if tail := id[len(id)-6:]; strings.Contains(got, tail) {
					t.Fatalf("id fragment %q shown: %q", tail, got)
				}
			}
		})
	}
}

// The same subject twice is one subject, so it keeps one ordinal.
func TestClarificationPromptOrdinalFollowsTheSubject(t *testing.T) {
	got := ClarificationPrompt([]SubjectCandidate{
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-a", "payments", ""),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-b", "payments", ""),
		promptCandidate(contractsv1.ContextFabricSubjectProject, "id-a", "payments", ""),
	})
	if want := "Which subject did you mean: payments (#1), payments (#2), payments (#1)?"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestClarificationPromptStaysWithinPublishedBoundAtMaxLengths(t *testing.T) {
	label := strings.Repeat("l", 512)
	cands := []SubjectCandidate{}
	for _, c := range []string{"a", "b", "c"} {
		cands = append(cands, promptCandidate(contractsv1.ContextFabricSubjectProject, strings.Repeat("x", 248)+strings.Repeat(c, 8), label, "jira"))
	}
	got := ClarificationPrompt(cands)
	if n := utf8.RuneCountInString(got); n > contractsv1.ContextFabricProjectedClarificationPromptMaxLength {
		t.Fatalf("prompt is %d runes, bound %d", n, contractsv1.ContextFabricProjectedClarificationPromptMaxLength)
	}
	if !strings.Contains(got, "#1") || !strings.Contains(got, "#2") || !strings.Contains(got, "#3") {
		t.Fatalf("identical choices not told apart: %q", got[len(got)-60:])
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
