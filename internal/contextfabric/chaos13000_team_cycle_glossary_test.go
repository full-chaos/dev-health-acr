package contextfabric_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
)

// CHAOS-13000: the fact-kind glossary told the interpreter that a team's
// `metrics` fact held commit counts and commit ratios only, while the
// registry declares a team-window pull request cycle median. The interpreter
// followed the glossary, listed flow and blockers for "highest cycle time", and
// the plan never read the team's cycle fact. The glossary is checked against
// the declared fields, so the two cannot drift apart again.
func TestTheMetricsGlossaryNamesTheTeamCycleTheRegistryDeclares(t *testing.T) {
	t.Parallel()
	declared := false
	for _, provider := range devhealthfacts.NewProviders(nil) {
		capability := provider.Capability()
		if capability.Kind != contextfabric.FactMetrics {
			continue
		}
		for _, field := range capability.Fields {
			if field.Name != "window_pr_cycle_hours_median" {
				continue
			}
			for _, kind := range field.SubjectKinds {
				declared = declared || kind == contextfabric.SubjectTeam
			}
		}
	}
	if !declared {
		t.Fatal("the metrics capability no longer declares window_pr_cycle_hours_median for a team; this test has nothing to pin")
	}

	var line string
	for _, candidate := range strings.Split(interpretprompt.FactKindGlossary, "\n") {
		if strings.HasPrefix(candidate, "- metrics:") {
			line = candidate
		}
	}
	_, team, found := strings.Cut(line, "For a team:")
	if !found {
		t.Fatalf("the metrics glossary line has no team clause: %q", line)
	}
	if !strings.Contains(team, "pull request cycle time") {
		t.Errorf("the metrics glossary says a team has %q, but the registry declares the team window pull request cycle median", team)
	}

	prompt := genkitruntime.InterpretationSystemPrompt()
	if !strings.Contains(prompt, `The "cycle time" of a team, and`) {
		t.Error("the fact_requirements rules do not route a team cycle time to metrics")
	}
	// The registry declares the field for a team and a repository only, so the
	// rule must send a project's cycle time to flow (codex round 1).
	if !strings.Contains(prompt, "The cycle time of a project, ") || strings.Contains(prompt, `"cycle time" of a team or a project`) {
		t.Error("a project's cycle time is not routed to flow, or the rule still sends it to metrics")
	}
	for _, provider := range devhealthfacts.NewProviders(nil) {
		capability := provider.Capability()
		if capability.Kind != contextfabric.FactMetrics {
			continue
		}
		for _, field := range capability.Fields {
			if field.Name != "window_pr_cycle_hours_median" {
				continue
			}
			for _, kind := range field.SubjectKinds {
				if kind == contextfabric.SubjectProject {
					t.Error("the registry now declares the window pull request cycle for a project; the rule and glossary must follow")
				}
			}
		}
	}
}
