package contextfabric

import (
	"context"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type rootsExpander struct {
	byPolicy map[FactScopePolicy]FactScopeExpansionResult
}

func (e rootsExpander) ExpandFactScope(_ context.Context, request FactScopeExpansionRequest) (FactScopeExpansionResult, error) {
	return e.byPolicy[request.Policy], nil
}

func derivationRoots(scope *FactReadScope, target SubjectRef) []string {
	var roots []string
	for _, derivation := range scope.Derivations {
		if canonicalFactSubjectKey(derivation.Target) == canonicalFactSubjectKey(target) {
			roots = append(roots, derivation.Root.CanonicalID)
		}
	}
	sort.Strings(roots)
	return roots
}

func rootsRegistry(t *testing.T, expander FactScopeExpander, policies map[FactKind]map[SubjectKind]factScopePolicyRule) *FactCapabilityRegistry {
	t.Helper()
	metrics := &factProviderStub{capability: planCapability(FactMetrics, "metrics", SubjectRepository), result: FactProviderResult{State: SourceAvailable}}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{ScopeExpander: expander, ScopePolicies: policies})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

var teamMetricsPolicy = factScopePolicyRule{
	Policy: FactScopePolicyTeamPrimaryAttributionRepository, TargetKind: SubjectRepository,
	Basis: FactScopeBasisActivityProxy, Enabled: true, Limit: 10,
}

// A target two same-kind origins reach is derived from both; an origin the
// request did not name is never recorded; the event says every root is known.
func TestScopeResolverRecordsEveryRequestedRootOfASharedTarget(t *testing.T) {
	secondTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:SRE", Label: "SRE"}
	foreignTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:FOREIGN", Label: "Foreign"}
	shared := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:shared"}
	own := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:own"}
	registry := rootsRegistry(t, rootsExpander{byPolicy: map[FactScopePolicy]FactScopeExpansionResult{
		FactScopePolicyTeamPrimaryAttributionRepository: {
			Targets:    []SubjectRef{shared, own},
			TargetRoot: map[string]SubjectRef{canonicalFactSubjectKey(shared): scopeTeam},
			TargetRoots: map[string][]SubjectRef{
				canonicalFactSubjectKey(shared): {secondTeam, foreignTeam, scopeTeam, secondTeam},
				canonicalFactSubjectKey(own):    {secondTeam},
			},
		},
	}}, map[FactKind]map[SubjectKind]factScopePolicyRule{FactMetrics: {SubjectTeam: teamMetricsPolicy}})
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeTeam, secondTeam}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatal(err)
	}
	if got := derivationRoots(bundle.Scope, shared); len(got) != 2 || got[0] != scopeTeam.CanonicalID || got[1] != secondTeam.CanonicalID {
		t.Fatalf("shared target roots %v, want both requested teams", got)
	}
	if got := derivationRoots(bundle.Scope, own); len(got) != 1 || got[0] != secondTeam.CanonicalID {
		t.Fatalf("own target roots %v", got)
	}
	if len(bundle.Scope.DerivedSubjects[FactMetrics]) != 2 {
		t.Fatalf("derived subjects %+v, want each target once", bundle.Scope.DerivedSubjects[FactMetrics])
	}
	event := bundle.Scope.Events[0]
	if !event.RootsComplete || event.SharedTargetCount != 1 {
		t.Fatalf("event roots_complete=%v shared=%d", event.RootsComplete, event.SharedTargetCount)
	}
}

// Without TargetRoots, two origins can name only one root per target: the
// event says the roots are not complete.
func TestScopeResolverMarksRootsIncompleteWithoutTargetRoots(t *testing.T) {
	secondTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:SRE", Label: "SRE"}
	shared := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:shared"}
	registry := rootsRegistry(t, rootsExpander{byPolicy: map[FactScopePolicy]FactScopeExpansionResult{
		FactScopePolicyTeamPrimaryAttributionRepository: {Targets: []SubjectRef{shared}, TargetRoot: map[string]SubjectRef{canonicalFactSubjectKey(shared): secondTeam}},
	}}, map[FactKind]map[SubjectKind]factScopePolicyRule{FactMetrics: {SubjectTeam: teamMetricsPolicy}})
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeTeam, secondTeam}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatal(err)
	}
	if got := derivationRoots(bundle.Scope, shared); len(got) != 1 || got[0] != secondTeam.CanonicalID {
		t.Fatalf("roots %v", got)
	}
	if bundle.Scope.Events[0].RootsComplete {
		t.Fatal("roots_complete with two origins and no TargetRoots")
	}
}

// A target a project origin and a team origin both reach is derived from
// both kinds' roots, and served once.
func TestScopeResolverRecordsRootsOfASecondOriginKind(t *testing.T) {
	shared := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:shared"}
	projectPolicy := factScopePolicyRule{
		Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
		Basis: FactScopeBasisActivityProxy, Enabled: true, Limit: 10,
	}
	registry := rootsRegistry(t, rootsExpander{byPolicy: map[FactScopePolicy]FactScopeExpansionResult{
		FactScopePolicyProjectWorkItemRepository:        {Targets: []SubjectRef{shared}, TargetRoots: map[string][]SubjectRef{canonicalFactSubjectKey(shared): {scopeProject}}},
		FactScopePolicyTeamPrimaryAttributionRepository: {Targets: []SubjectRef{shared}, TargetRoots: map[string][]SubjectRef{canonicalFactSubjectKey(shared): {scopeTeam}}},
	}}, map[FactKind]map[SubjectKind]factScopePolicyRule{FactMetrics: {SubjectProject: projectPolicy, SubjectTeam: teamMetricsPolicy}})
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeProject, scopeTeam}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatal(err)
	}
	if got := derivationRoots(bundle.Scope, shared); len(got) != 2 || got[0] != scopeProject.CanonicalID || got[1] != scopeTeam.CanonicalID {
		t.Fatalf("roots %v, want the project and the team", got)
	}
	if len(bundle.Scope.DerivedSubjects[FactMetrics]) != 1 {
		t.Fatalf("derived subjects %+v, want the target once", bundle.Scope.DerivedSubjects[FactMetrics])
	}
}

// A target a second origin kind returns past the cap, when an earlier origin
// kind already admitted it, still records that origin's root.
func TestScopeResolverRecordsTheRootOfAnAdmittedTargetPastTheCap(t *testing.T) {
	shared := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:shared"}
	other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:other"}
	projectPolicy := factScopePolicyRule{
		Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
		Basis: FactScopeBasisActivityProxy, Enabled: true, Limit: 1,
	}
	teamPolicy := teamMetricsPolicy
	teamPolicy.Limit = 1
	registry := rootsRegistry(t, rootsExpander{byPolicy: map[FactScopePolicy]FactScopeExpansionResult{
		FactScopePolicyProjectWorkItemRepository: {Targets: []SubjectRef{shared}, TargetRoots: map[string][]SubjectRef{canonicalFactSubjectKey(shared): {scopeProject}}},
		FactScopePolicyTeamPrimaryAttributionRepository: {
			Targets:     []SubjectRef{other, shared},
			TargetRoots: map[string][]SubjectRef{canonicalFactSubjectKey(other): {scopeTeam}, canonicalFactSubjectKey(shared): {scopeTeam}},
		},
	}}, map[FactKind]map[SubjectKind]factScopePolicyRule{FactMetrics: {SubjectProject: projectPolicy, SubjectTeam: teamPolicy}})
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeProject, scopeTeam}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Scope.DerivedSubjects[FactMetrics]) != 1 {
		t.Fatalf("derived subjects %+v, want the cap of one subject", bundle.Scope.DerivedSubjects[FactMetrics])
	}
	if got := derivationRoots(bundle.Scope, shared); len(got) != 2 || got[0] != scopeProject.CanonicalID || got[1] != scopeTeam.CanonicalID {
		t.Fatalf("shared target roots %v, want the project and the team", got)
	}
}

// A TargetRoot the request did not name is never recorded: the first
// requested origin stands in, and the event says the roots are not complete.
func TestScopeResolverNeverRecordsAnUnrequestedTargetRoot(t *testing.T) {
	secondTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:SRE", Label: "SRE"}
	foreignTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:FOREIGN", Label: "Foreign"}
	shared := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:shared"}
	registry := rootsRegistry(t, rootsExpander{byPolicy: map[FactScopePolicy]FactScopeExpansionResult{
		FactScopePolicyTeamPrimaryAttributionRepository: {Targets: []SubjectRef{shared}, TargetRoot: map[string]SubjectRef{canonicalFactSubjectKey(shared): foreignTeam}},
	}}, map[FactKind]map[SubjectKind]factScopePolicyRule{FactMetrics: {SubjectTeam: teamMetricsPolicy}})
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeTeam, secondTeam}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatal(err)
	}
	for _, derivation := range bundle.Scope.Derivations {
		if derivation.Root.CanonicalID == foreignTeam.CanonicalID {
			t.Fatalf("derivation rooted at an unrequested origin: %+v", derivation)
		}
	}
	if bundle.Scope.Events[0].RootsComplete {
		t.Fatal("roots_complete with an unrequested TargetRoot")
	}
}
