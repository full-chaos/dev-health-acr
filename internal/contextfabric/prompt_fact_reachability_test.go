package contextfabric_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

// The interpretation prompt's reachability sentences are derived from the
// capability registry plus the enabled scope-expansion rules, so the prompt
// cannot claim a team or project reaches a kind it does not (or the reverse).
func TestInterpretationPromptReachabilityMatchesRegistryAndScopeRules(t *testing.T) {
	t.Parallel()
	reachable := func(origin contextfabric.SubjectKind) map[string]bool {
		out := map[string]bool{}
		for _, provider := range devhealthfacts.NewProviders(nil) {
			capability := provider.Capability()
			for _, kind := range capability.SupportedSubjectKinds {
				if kind == origin {
					out[string(capability.Kind)] = true
				}
			}
		}
		for kind, origins := range contextfabric.FactScopeEnabledOriginsForTest() {
			for _, candidate := range origins {
				if candidate == origin {
					out[string(kind)] = true
				}
			}
		}
		return out
	}
	team, project := reachable(contextfabric.SubjectTeam), reachable(contextfabric.SubjectProject)
	if len(team) == 0 || len(project) == 0 {
		t.Fatal("derived an empty reachable set; nothing to check the prompt against")
	}
	prompt := genkitruntime.InterpretationSystemPrompt()

	// Sentence 1: "A team or project question also reaches the work-item
	// kinds (...) through its work items, and pull_requests and reviews
	// through its activity."
	for _, kind := range []string{"status", "work", "actual_completion", "blockers", "required_children", "identity", "membership", "pull_requests", "reviews"} {
		for name, set := range map[string]map[string]bool{"team": team, "project": project} {
			if !set[kind] {
				t.Errorf("the prompt says a %s question reaches %s, but the registry and scope rules do not reach it", name, kind)
			}
		}
	}
	if !strings.Contains(prompt, "A team or project question also reaches the work-item kinds (status, work, actual_completion, blockers, required_children, identity, membership) through its work items, and pull_requests and reviews through its activity.") {
		t.Error("prompt lost the work-item and activity reachability sentence this test derives")
	}

	// Sentence 2: "A team also has its own rollups of blockers, pull_requests,
	// incidents and deployments." Incidents and deployments are team-only.
	for _, kind := range []string{"blockers", "pull_requests", "incidents", "deployments"} {
		if !team[kind] {
			t.Errorf("the prompt says a team has its own %s rollup, but a team does not reach it", kind)
		}
	}
	for _, kind := range []string{"incidents", "deployments"} {
		if project[kind] {
			t.Errorf("%s is now reachable from a project; the prompt's team-only wording must change", kind)
		}
	}
	if !strings.Contains(prompt, "A team also has its own rollups of blockers, pull_requests, incidents and deployments.") {
		t.Error("prompt lost the team rollup sentence this test derives")
	}

	// Sentence 3: "continuous_integration and source_health are not reached
	// from a team or a project; operational_deficiencies is not reached from
	// a project."
	for _, kind := range []string{"continuous_integration", "source_health"} {
		if team[kind] || project[kind] {
			t.Errorf("%s is reachable from a team or project; the prompt says it is not", kind)
		}
	}
	if !team["operational_deficiencies"] || project["operational_deficiencies"] {
		t.Errorf("operational_deficiencies reachability changed (team=%v project=%v); the prompt says team yes, project no", team["operational_deficiencies"], project["operational_deficiencies"])
	}
	if !strings.Contains(prompt, "continuous_integration and source_health are not reached from a team or a project; operational_deficiencies is not reached from a project.") {
		t.Error("prompt lost the not-reached sentence this test derives")
	}
}
