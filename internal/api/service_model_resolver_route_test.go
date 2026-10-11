package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
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
	fixture := singleSubjectFixture()
	project := suppliedRouteProject()
	deployment := &clientRouteModel{interpreted: fixture.interpreted}
	resolver := modelruntimeresolver.New(deployment, deletedOrgModelConfig{}, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		t.Error("no organization runtime may be built for a deleted configuration")
		return nil, fmt.Errorf("unexpected build")
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
	rig.app.runtime.OrgModelConfigs = ownModelConfigs{found: true}
	return rig
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

// The mirror: with the feature, the same organization is served on the
// deployment model through the same resolver.
func TestDeploymentModelServesThroughTheResolverWithTheFeature(t *testing.T) {
	rig := newResolverRouteRig(t, &serviceModelGrants{feature: true})
	decodeEnvelope(t, rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil))
	if interprets, synthesizes := rig.model.counts(); interprets != 1 || synthesizes != 1 {
		t.Fatalf("deployment model calls = interpret %d synthesize %d, want 1 and 1", interprets, synthesizes)
	}
}
