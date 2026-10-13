package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// derivedRouteProvider answers one fact per subject of its target kind; a
// team rollup it also supports answers nothing, as on prod.
type derivedRouteProvider struct {
	kind      contextfabric.FactKind
	target    contextfabric.SubjectKind
	supported []contextfabric.SubjectKind
}

func (p derivedRouteProvider) Capability() contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: p.kind, Name: string(p.kind) + "_route_test", Version: "test.v1",
		SupportedSubjectKinds: p.supported,
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:                []contextfabric.FactFieldDeclaration{{Name: "n", Type: contextfabric.FactFieldInteger}},
	}
}

func (p derivedRouteProvider) ReadFacts(_ context.Context, _ storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	n := int64(1)
	result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
	for _, subject := range query.Subjects {
		if subject.Kind == p.target {
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: p.kind, Subject: subject, Fields: map[string]contextfabric.FactValue{"n": {Integer: &n}}})
		}
	}
	return result, nil
}

type rootedExpander struct{}

func (rootedExpander) ExpandFactScope(_ context.Context, request contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	target := contextfabric.SubjectRef{Kind: request.TargetKind, CanonicalID: string(request.TargetKind) + ":derived", Label: "derived"}
	return contextfabric.FactScopeExpansionResult{
		Targets:    []contextfabric.SubjectRef{target},
		TargetRoot: map[string]contextfabric.SubjectRef{contextfabric.FactSubjectKey(target): request.Origins[0]},
		Counts:     contextfabric.FactScopeExpansionCounts{CandidateCount: 1},
	}, nil
}

// Kinds a requested team or project holds only through scope expansion are
// served on read_facts through the production direct fact source, each fact
// naming the requested subject it was reached from.
func TestFactsRouteServesKindsReachedByScopeExpansion(t *testing.T) {
	team := contractsv1.ContextFabricSubjectTeam
	cases := []struct {
		name, root string
		provider   derivedRouteProvider
	}{
		{"team blockers", "team", derivedRouteProvider{contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectWorkItem, team}}},
		{"project deployments", "project", derivedRouteProvider{contextfabric.FactDeployments, contractsv1.ContextFabricSubjectRepository, []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository}}},
		{"project continuous integration", "project", derivedRouteProvider{contextfabric.FactContinuousIntegration, contractsv1.ContextFabricSubjectRepository, []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository, team}}},
		{"project incidents", "project", derivedRouteProvider{contextfabric.FactIncidents, contractsv1.ContextFabricSubjectIncident, []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectIncident}}},
		{"project operational deficiencies", "project", derivedRouteProvider{contextfabric.FactOperationalDeficiencies, team, []contextfabric.SubjectKind{team}}},
		{"project pull requests", "project", derivedRouteProvider{contextfabric.FactPullRequests, contractsv1.ContextFabricSubjectPullRequest, []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectPullRequest}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newChaos7071Harness(t, 100)
			token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
			registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{tc.provider}, contextfabric.FactRegistryOptions{ScopeExpander: rootedExpander{}})
			if err != nil {
				t.Fatal(err)
			}
			h.app.runtime.DirectReadGate = directread.NewSubjectGate(reachingGraph{}, nil)
			h.app.runtime.DirectFactReader = directread.NewFactReader(registry.DirectReadFactSource())
			root := tc.root + ":a"
			response := h.postFacts(token, `{"kinds":["`+string(tc.provider.kind)+`"],"subjects":[{"kind":"`+tc.root+`","canonical_id":"`+root+`"}]}`)
			if response.Code != http.StatusOK {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			var document directread.FactsResponse
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Facts) != 1 || document.Facts[0].Subject.Kind != string(tc.provider.target) {
				t.Fatalf("no derived fact served: %s", response.Body.String())
			}
			if reached := document.Facts[0].ReachedFrom; len(reached) != 1 || reached[0].Kind != tc.root || reached[0].CanonicalID != root {
				t.Fatalf("reached_from %+v", reached)
			}
			if len(document.Coverage) != 1 || document.Coverage[0].Outcome != directread.OutcomeFactServed {
				t.Fatalf("coverage %+v", document.Coverage)
			}
		})
	}
}
