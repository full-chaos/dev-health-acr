package graphrank

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func projectWithProvider(id, label, provider string) contextfabric.SubjectCandidate {
	c := repoAliasCandidate(id, "chaos")
	c.Subject.Kind = contextfabric.SubjectProject
	c.Subject.Label = label
	c.Provider = provider
	return c
}

func TestClarificationPromptNamesProviderOnlyForCollidingLabels(t *testing.T) {
	got := ClarificationPrompt([]contextfabric.SubjectCandidate{
		projectWithProvider("a", "CHAOS", "jira"), projectWithProvider("b", "CHAOS", "linear"), projectWithProvider("c", "Other", "github"),
	})
	want := "Which subject did you mean: CHAOS (jira), CHAOS (linear), Other?"
	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestClarificationPromptUnchangedWhenLabelsDiffer(t *testing.T) {
	got := ClarificationPrompt([]contextfabric.SubjectCandidate{projectWithProvider("a", "A", "jira"), projectWithProvider("b", "B", "linear")})
	if strings.Contains(got, "(") {
		t.Fatalf("prompt = %q, want no provider cue", got)
	}
}

func TestNodeCandidateCarriesSingleProviderOnly(t *testing.T) {
	if got := ProviderAttribute(map[string]interface{}{"provider_jira": "1", "provider_aliases": []string{"x"}}); got != "jira" {
		t.Fatalf("provider = %q, want jira", got)
	}
	if got := ProviderAttribute(map[string]interface{}{"provider_jira": "1", "provider_linear": "2"}); got != "" {
		t.Fatalf("provider = %q, want empty for two providers", got)
	}
}

func TestStripUncollidedProvidersKeepsOnlyCollidingCues(t *testing.T) {
	c := []contextfabric.SubjectCandidate{projectWithProvider("a", "CHAOS", "jira"), projectWithProvider("b", "CHAOS", "linear"), projectWithProvider("c", "Solo", "github")}
	stripUncollidedProviders(c)
	if c[0].Provider != "jira" || c[1].Provider != "linear" || c[2].Provider != "" {
		t.Fatalf("providers = %q %q %q", c[0].Provider, c[1].Provider, c[2].Provider)
	}
}
