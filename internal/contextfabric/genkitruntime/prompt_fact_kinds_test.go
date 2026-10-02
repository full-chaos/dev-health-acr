package genkitruntime

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestInterpretationPromptGlossaryNamesEveryKindOnce(t *testing.T) {
	t.Parallel()
	vocabulary := contractsv1.ContextFabricFactKindVocabulary()
	for _, kind := range vocabulary {
		line := "\n- " + string(kind) + ": "
		if got := strings.Count(interpretationSystemPrompt, line); got != 1 {
			t.Errorf("glossary line for kind %q appears %d times in the rendered prompt, want exactly 1", kind, got)
		}
	}
	// A kind added to the vocabulary without a glossary line, or a glossary
	// line for a kind that is not in the vocabulary, changes this count.
	if got := strings.Count(interpretationFactKindGlossary, "\n- "); got != len(vocabulary) {
		t.Errorf("glossary has %d kind lines, vocabulary has %d kinds", got, len(vocabulary))
	}
}

func TestInterpretationPromptStatesTheKindTraps(t *testing.T) {
	t.Parallel()
	for _, trap := range []string{
		"status: the status column of one work item",
		"Not completion.",
		"readiness: backlog estimate coverage",
		"Not release, ship or delivery readiness.",
		"membership: the organization that a repository belongs to",
		"Not team membership and not project membership.",
		"reviews: the state of one review",
		"never review time; review timing is flow",
		"work: the title of one work item and nothing else",
		"evidence: holds nothing and has no provider",
		"never list it",
	} {
		if strings.Count(interpretationSystemPrompt, trap) < 1 {
			t.Errorf("rendered prompt lacks the trap statement %q", trap)
		}
	}
}

func TestInterpretationPromptWordToKindRules(t *testing.T) {
	t.Parallel()
	for _, rule := range []string{
		"the burden composite, exactly flow, health, incidents, investment, workload",
		`"ready to release" and the noun form "release readiness": actual_completion, deployments. Never readiness.`,
		`"on track", "on schedule", "behind", "delayed", "miss targets": actual_completion, status.`,
		`"on-call load", "operational load": incidents. The word "load" never names workload.`,
		`"code ownership risk" named: metrics.`,
		"A blocking word together with a state word about work items",
		"status and blockers",
		`"stuck in review": blockers, pull_requests, reviews.`,
		`"needs attention" on work items or pull requests: blockers, status.`,
		`A counted noun that is itself a kind`,
		`An output measure with no kind word`,
		"Never list evidence.",
		`A repository "failure rate": continuous_integration, except "change failure rate", which is metrics and takes precedence over the failure-rate rule.`,
	} {
		if !strings.Contains(interpretationSystemPrompt, rule) {
			t.Errorf("rendered prompt lacks the word-to-kind rule %q", rule)
		}
	}
}

func TestInterpretationPromptDropsTheInferMayBeNeededContradiction(t *testing.T) {
	t.Parallel()
	for _, gone := range []string{
		"fact families that may be needed",
		"families the question actually needs",
		"time context, and canonical fact families that may be needed",
	} {
		if strings.Contains(interpretationSystemPrompt, gone) {
			t.Errorf("rendered prompt still carries the contradicted sentence %q", gone)
		}
	}
	if !strings.Contains(interpretationSystemPrompt, "canonical fact families that the question's words name") {
		t.Error("rendered prompt lacks the words-name-the-kinds instruction")
	}
}

func TestInterpretationPromptTeamServingMatchesCapabilities(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"(work_item, team)",
		"(pull_request, team)",
		"(deployment, repository, team)",
		"(incident, team)",
		"A team also has its own rollups of blockers, pull_requests, incidents and deployments.",
	} {
		if !strings.Contains(interpretationSystemPrompt, line) {
			t.Errorf("rendered prompt lacks the team-serving statement %q", line)
		}
	}
	for _, stale := range []string{
		"and not a repository or team fact",
		"Not an incident count for a team",
		"incidents, deployments, continuous_integration and source_health are not reached from a team",
	} {
		if strings.Contains(interpretationSystemPrompt, stale) {
			t.Errorf("rendered prompt still carries the stale team-serving statement %q", stale)
		}
	}
}
