package graphrank

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

func TestNodeCandidateFormsProviderOnTheCandidate(t *testing.T) {
	node := CandidateNode{UUID: "u", Name: "CHAOS", Attributes: map[string]interface{}{
		"subject_kind": "project", "canonical_id": "project.v2:a", "label": "CHAOS",
		"aliases": []string{"chaos"}, "provider_jira": "10", "authorization_repositories": "*",
	}}
	c, ok := NodeCandidate(storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, "chaos", node, func(contextfabric.SubjectRef) bool { return false }, true, nil, "")
	if !ok || c.Provider != "jira" {
		t.Fatalf("candidate = %+v ok=%v, want provider jira", c, ok)
	}
}

func TestClarificationPromptNamesTheKindWhenALabelIsHeldByTwoKinds(t *testing.T) {
	mk := func(kind contextfabric.SubjectKind, id, provider string) contextfabric.SubjectCandidate {
		return contextfabric.SubjectCandidate{Subject: contextfabric.SubjectRef{Kind: kind, CanonicalID: string(kind) + ":" + id, Label: "chaos"}, Provider: provider}
	}
	got := ClarificationPrompt([]contextfabric.SubjectCandidate{mk(contextfabric.SubjectTeam, "1", "linear"), mk(contextfabric.SubjectProject, "2", "jira")})
	if want := "Which subject did you mean: chaos (team), chaos (project)?"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	got = ClarificationPrompt([]contextfabric.SubjectCandidate{mk(contextfabric.SubjectTeam, "1", "linear"), mk(contextfabric.SubjectTeam, "3", "jira"), mk(contextfabric.SubjectProject, "2", "jira")})
	if want := "Which subject did you mean: chaos (team, linear), chaos (team, jira), chaos (project)?"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}
