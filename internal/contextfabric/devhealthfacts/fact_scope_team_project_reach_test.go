package devhealthfacts_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// capabilityOnlyProvider keeps a production provider's Capability and answers
// no facts, so the matrix measures PLANNING (pruned / disclosed / served)
// through the production capability set and the production eligibility table
// without a ClickHouse client.
type capabilityOnlyProvider struct{ capability contextfabric.FactCapability }

func (p capabilityOnlyProvider) Capability() contextfabric.FactCapability { return p.capability }

func (p capabilityOnlyProvider) ReadFacts(_ context.Context, _ storage.Principal, _ contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
}

// reachExpander records which policy each requirement was handed to. It
// admits nothing, so no provider is queried: the matrix measures planning.
type reachExpander struct {
	policies map[contextfabric.FactKind]contextfabric.FactScopePolicy
}

func (e *reachExpander) ExpandFactScope(_ context.Context, request contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	e.policies[request.RequirementKind] = request.Policy
	return contextfabric.FactScopeExpansionResult{}, nil
}

type reachOutcome string

const (
	reachServed    reachOutcome = "served"
	reachDirect    reachOutcome = "direct"
	reachDisclosed reachOutcome = "disclosed"
	reachPruned    reachOutcome = "pruned"
)

func reachObservations(t *testing.T, origin contextfabric.SubjectKind) (map[contextfabric.FactKind]reachOutcome, map[contextfabric.FactKind]contextfabric.FactScopePolicy) {
	t.Helper()
	var providers []contextfabric.FactProvider
	var kinds []contextfabric.FactKind
	directly := map[contextfabric.FactKind]bool{}
	for _, provider := range devhealthfacts.NewProviders(nil) {
		capability := provider.Capability()
		providers = append(providers, capabilityOnlyProvider{capability: capability})
		kinds = append(kinds, capability.Kind)
		for _, supported := range capability.SupportedSubjectKinds {
			if supported == origin {
				directly[capability.Kind] = true
			}
		}
	}
	expander := &reachExpander{policies: map[contextfabric.FactKind]contextfabric.FactScopePolicy{}}
	registry, err := contextfabric.NewFactCapabilityRegistry(providers, contextfabric.FactRegistryOptions{ScopeExpander: expander})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	subject := contextfabric.SubjectRef{Kind: origin, CanonicalID: string(origin) + ":reach-fixture", Label: "reach fixture"}
	out := map[contextfabric.FactKind]reachOutcome{}
	for _, kind := range kinds {
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.CanonicalFactRequest{
			Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
			Subjects:     []contextfabric.SubjectRef{subject},
			Requirements: []contextfabric.FactRequirement{{Kind: kind}},
		})
		if err != nil {
			t.Fatalf("ReadFacts(%s, %s): %v", origin, kind, err)
		}
		switch {
		case directly[kind]:
			out[kind] = reachDirect
		case len(bundle.Coverage.Sources) == 1 && bundle.Coverage.Sources[0].State == contextfabric.SourcePruned:
			out[kind] = reachPruned
		case len(bundle.Scope.Events) == 1 && bundle.Scope.Events[0].Policy == contextfabric.FactScopePolicyNone:
			out[kind] = reachDisclosed
		default:
			out[kind] = reachServed
		}
	}
	return out, expander.policies
}

// entityTreePolicies is the policy each previously pruned pair is handed to.
var entityTreePolicies = map[contextfabric.SubjectKind]map[contextfabric.FactKind]contextfabric.FactScopePolicy{
	contextfabric.SubjectTeam: {
		contextfabric.FactContinuousIntegration: contextfabric.FactScopePolicyTeamRepositoryOwnershipContinuousIntegration,
	},
	contextfabric.SubjectProject: {
		contextfabric.FactContinuousIntegration:   contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration,
		contextfabric.FactDeployments:             contextfabric.FactScopePolicyProjectIssuePullRequestDeployments,
		contextfabric.FactIncidents:               contextfabric.FactScopePolicyProjectIssuePullRequestIncidents,
		contextfabric.FactOperationalDeficiencies: contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies,
	},
}

// TestFactScopeTeamProjectReachMatrix executes, through the production
// capability set and eligibility table, what a team- or project-origin
// requirement of every fact kind becomes. A pair that is not served and has
// no direct provider is a gap the entity tree can reach.
func TestFactScopeTeamProjectReachMatrix(t *testing.T) {
	var gaps []string
	for _, origin := range []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectProject} {
		observed, policies := reachObservations(t, origin)
		for kind, want := range entityTreePolicies[origin] {
			if got := policies[kind]; got != want {
				t.Fatalf("origin=%s requirement=%s was handed policy %q, want %q", origin, kind, got, want)
			}
		}
		kinds := make([]string, 0, len(observed))
		for kind := range observed {
			kinds = append(kinds, string(kind))
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			outcome := observed[contextfabric.FactKind(kind)]
			t.Logf("origin=%s requirement=%s outcome=%s", origin, kind, outcome)
			if outcome == reachPruned || outcome == reachDisclosed {
				gaps = append(gaps, fmt.Sprintf("%s x %s = %s", origin, kind, outcome))
			}
		}
	}
	// source_health is organization-scoped: no team/project chain exists.
	// security_alerts is served for a repository only; a team or project
	// roll-up through ownership is not built yet.
	var unexpected []string
	for _, gap := range gaps {
		if gap == "team x source_health = pruned" || gap == "project x source_health = pruned" ||
			gap == "team x security_alerts = pruned" || gap == "project x security_alerts = pruned" ||
			gap == "team x file_hotspots = pruned" || gap == "project x file_hotspots = pruned" {
			continue
		}
		unexpected = append(unexpected, gap)
	}
	if len(unexpected) != 0 {
		t.Fatalf("team/project origin pairs with no served path: %v", unexpected)
	}
}
