package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

// deletedOrgModelConfig is the resolver's view of an organization whose own
// model configuration is gone by the time the engine resolves a runtime.
type deletedOrgModelConfig struct{}

func (deletedOrgModelConfig) ResolveOrgModelConfig(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	return contextfabric.ResolvedOrgModelConfig{}, false, nil
}

// newResolverRouteRig composes the real API over the real engine whose model
// runtime is the production per-organization resolver over a counting
// deployment model. The gate sees the organization's own model configuration;
// the resolver, reading later, sees none.
func newResolverRouteRig(t *testing.T, grants *serviceModelGrants) *clientRouteRig {
	t.Helper()
	rig, _ := newSequencedResolverRig(t, grants, ownModelConfigs{found: true}, deletedOrgModelConfig{})
	return rig
}

// newSequencedResolverRig is newResolverRouteRig with the gate's view of the
// organization configuration, and the resolver's, given. The resolver builds a
// counting organization runtime, returned beside the rig; the rig's own model
// is the deployment model.
func newSequencedResolverRig(t *testing.T, grants *serviceModelGrants, gateConfigs, resolverConfigs contextfabric.OrgModelConfigResolver) (*clientRouteRig, *clientRouteModel) {
	t.Helper()
	fixture := singleSubjectFixture()
	project := suppliedRouteProject()
	deployment := &clientRouteModel{interpreted: fixture.interpreted}
	own := &clientRouteModel{interpreted: fixture.interpreted}
	resolver := modelruntimeresolver.New(deployment, resolverConfigs, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return own, nil
	})
	store := memoryinvestigation.NewStore()
	rig := &clientRouteRig{model: deployment, store: store}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: resolver},
		Graph:       clientRouteGraph{authorizingGraph: authorizingGraph{liveGraphReader: liveGraphReader{project: project}, outcome: contextfabric.StoredSubjectAdmitted}},
		Facts:       liveFactReader{bundle: fixture.facts},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{
			Runtime: resolver, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "resolver-route", Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
		},
		Results: store,
	}, contextfabric.EngineOptions{
		ServiceVersion: "resolver-route",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			rig.nextID++
			return fmt.Sprintf("result_resolver_route%02d", rig.nextID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.logs = &bytes.Buffer{}
	rig.app, rig.token = newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, rig.logs)
	rig.app.config.RequestTimeout = time.Minute
	rig.app.runtime.Entitlements = grants
	rig.app.runtime.OrgModelConfigs = gateConfigs
	return rig, own
}

type configRead struct {
	found bool
	err   error
}

// sequencedConfigs answers each read with the next step and repeats the last.
type sequencedConfigs struct {
	mu    sync.Mutex
	steps []configRead
	reads int
}

func (c *sequencedConfigs) ResolveOrgModelConfig(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	step := c.steps[min(c.reads, len(c.steps)-1)]
	c.reads++
	return contextfabric.ResolvedOrgModelConfig{Generation: 1}, step.found, step.err
}

// The gate's basis binds the whole turn: whatever the resolver reads later,
// one model serves it and the label names that model.
func TestGateBasisBindsTheTurnAcrossConfigReadSequences(t *testing.T) {
	for _, tc := range []struct {
		name       string
		gate       contextfabric.OrgModelConfigResolver
		resolver   contextfabric.OrgModelConfigResolver
		status     int
		basis      string
		wantOwn    [2]int
		wantDeploy [2]int
	}{
		{name: "found then missing mid-turn", gate: ownModelConfigs{found: true},
			resolver: &sequencedConfigs{steps: []configRead{{found: true}, {found: false}}},
			status:   http.StatusServiceUnavailable, basis: "own_model", wantOwn: [2]int{1, 0}},
		{name: "found then read error mid-turn", gate: ownModelConfigs{found: true},
			resolver: &sequencedConfigs{steps: []configRead{{found: true}, {err: errors.New("store down")}}},
			status:   http.StatusServiceUnavailable, basis: "own_model", wantOwn: [2]int{1, 0}},
		{name: "missing at the gate then found mid-turn", gate: ownModelConfigs{},
			resolver: &sequencedConfigs{steps: []configRead{{found: false}, {found: true}}},
			status:   http.StatusOK, basis: "entitlement", wantDeploy: [2]int{1, 1}},
		{name: "missing at the gate then found from the first read", gate: ownModelConfigs{},
			resolver: &sequencedConfigs{steps: []configRead{{found: true}}},
			status:   http.StatusOK, basis: "entitlement", wantDeploy: [2]int{1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, own := newSequencedResolverRig(t, &serviceModelGrants{feature: true}, tc.gate, tc.resolver)
			recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), tc.status)
			}
			if i, s := own.counts(); [2]int{i, s} != tc.wantOwn {
				t.Fatalf("own model calls = %d/%d, want %v", i, s, tc.wantOwn)
			}
			if i, s := rig.model.counts(); [2]int{i, s} != tc.wantDeploy {
				t.Fatalf("deployment model calls = %d/%d, want %v", i, s, tc.wantDeploy)
			}
			if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != tc.basis {
				t.Fatalf("decision log bases = %v, want [%s]", got, tc.basis)
			}
		})
	}
}

// An organization admitted on its own model whose configuration is deleted
// before the engine resolves a runtime never falls through to the deployment
// model: the turn fails as model-unavailable with zero deployment calls.
func TestOwnModelDeletedAfterTheGateNeverFallsBackToTheDeploymentModel(t *testing.T) {
	rig := newResolverRouteRig(t, &serviceModelGrants{})
	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("deployment model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "own_model" {
		t.Fatalf("decision log bases = %v, want [own_model]", got)
	}
}

// With the feature, an organization that also holds its own model
// configuration is admitted on that configuration: when it is deleted before
// the resolver reads it, the turn never falls through to the deployment model.
func TestEntitledOrgWithOwnModelDeletedAfterTheGateNeverFallsBackToTheDeploymentModel(t *testing.T) {
	rig := newResolverRouteRig(t, &serviceModelGrants{feature: true})
	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("deployment model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "own_model" {
		t.Fatalf("decision log bases = %v, want [own_model]", got)
	}
}
