package devhealthfacts

// Per-capability proof for the mixed-root scope-resolution omission: a
// request that names a project, a team and roots of other kinds must reach
// every capability with exactly the roots it answers directly, and must not
// invent an expansion decision for a root kind no policy covers. The shared
// resolver's mechanism has its own tests; this file runs each wide
// capability's REAL declaration through the registry.

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type recordingCapabilityProvider struct {
	contextfabric.FactProvider
	queries []contextfabric.FactQuery
}

func (p *recordingCapabilityProvider) ReadFacts(_ context.Context, _ storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	p.queries = append(p.queries, query)
	return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
}

func TestMixedRootScopeSweep_EveryWideCapabilityServesBothRootsAndInventsNoDecision(t *testing.T) {
	t.Parallel()

	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:linear:ALPHA", Label: "Alpha"}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:PLATFORM", Label: "Platform"}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo:github:api", Label: "api"}
	workItem := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:ALPHA-1", Label: "ALPHA-1"}

	// wantServed is written from each capability's documented width, not read
	// back from Capability(): investment, flow, health and metrics answer for
	// a repository too; readiness, workload and landscape do not.
	cases := []struct {
		name       string
		provider   contextfabric.FactProvider
		wantServed []contextfabric.SubjectKind
	}{
		{"metrics", &MetricsProvider{}, []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectRepository, contextfabric.SubjectTeam}},
		{"health", &HealthProvider{}, []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectRepository, contextfabric.SubjectTeam}},
		{"investment", &InvestmentProvider{}, []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectRepository, contextfabric.SubjectTeam}},
		{"flow", &FlowProvider{}, []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectRepository, contextfabric.SubjectTeam}},
		{"readiness", &ReadinessProvider{}, []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam}},
		{"workload", &WorkloadProvider{}, []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam}},
		{"landscape", &LandscapeProvider{}, []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := &recordingCapabilityProvider{FactProvider: tc.provider}
			registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{recorder}, contextfabric.FactRegistryOptions{})
			if err != nil {
				t.Fatalf("NewFactCapabilityRegistry: %v", err)
			}
			kind := tc.provider.Capability().Kind
			bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.CanonicalFactRequest{
				Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
				Subjects:     []contextfabric.SubjectRef{project, team, repository, workItem},
				Requirements: []contextfabric.FactRequirement{{Kind: kind}},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
			if len(recorder.queries) != 1 {
				t.Fatalf("%s queried %d time(s), want 1", kind, len(recorder.queries))
			}
			var served []contextfabric.SubjectKind
			for _, subject := range recorder.queries[0].Subjects {
				served = append(served, subject.Kind)
			}
			want := append([]contextfabric.SubjectKind(nil), tc.wantServed...)
			sort.Slice(served, func(i, j int) bool { return served[i] < served[j] })
			sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
			if !reflect.DeepEqual(served, want) {
				t.Fatalf("%s served subject kinds %v, want %v -- the project and the team root must both reach the provider in one mixed request", kind, served, want)
			}
			if len(bundle.Scope.Events) != 0 {
				t.Fatalf("%s emitted scope events %+v, want none -- every served root is answered directly and the dropped kinds have no policy", kind, bundle.Scope.Events)
			}
		})
	}
}
