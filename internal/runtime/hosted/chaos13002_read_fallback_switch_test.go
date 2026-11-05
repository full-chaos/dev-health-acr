package hosted

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type planningDeploymentRuntime struct{ plainDeploymentRuntime }

func (*planningDeploymentRuntime) PlanReads(context.Context, storage.Principal, contextfabric.ReadPlanningInput) (contextfabric.ReadPlan, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.ReadPlan{}, contextfabric.ModelExecutionReceipt{}, nil
}

// CHAOS-13002: the read fallback switch reaches the engine from the
// deployment's config, and a deployment model that plans reads is wired as
// the read planner whatever the switch says.
func TestBuildContextFabricInvestigator_wiresTheReadFallbackSwitch(t *testing.T) {
	configureGraph(t)
	for _, on := range []bool{false, true} {
		request, postgres, clickhouse := investigatorBuildRequest(t)
		request.config.ContextFabricReadFallbackEnabled = on
		request.options.ModelRuntimeOverride = &planningDeploymentRuntime{}
		investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)
		if err != nil {
			t.Fatalf("buildContextFabricInvestigator() = %v", err)
		}
		engine, ok := investigator.(*contextfabric.Engine)
		if !ok {
			t.Fatalf("investigator is a %T, want *contextfabric.Engine", investigator)
		}
		if got := mustField(t, reflect.ValueOf(engine).Elem(), "readFallbackEnabled").Bool(); got != on {
			t.Fatalf("config on=%v: the engine's readFallbackEnabled = %v", on, got)
		}
		if planner := mustField(t, reflect.ValueOf(engine).Elem(), "readPlanner"); planner.IsNil() {
			t.Fatalf("config on=%v: no read planner is wired", on)
		}
	}
}

// The read planner always resolves through a resolver, and a deployment
// runtime that cannot plan, or none, leaves it off.
func TestReadPlanningRuntimeForAlwaysResolvesThroughAResolver(t *testing.T) {
	var typedNil *planningDeploymentRuntime
	wired := modelruntimeresolver.New(nil, nil, nil)
	if got := readPlanningRuntimeFor(wired, nil); got != contextfabric.ReadPlanningModelRuntime(wired) {
		t.Fatalf("with a resolver wired, got %T, want that resolver", got)
	}
	deployment := &planningDeploymentRuntime{}
	got := readPlanningRuntimeFor(nil, deployment)
	if _, ok := got.(*modelruntimeresolver.Resolver); !ok || got == contextfabric.ReadPlanningModelRuntime(deployment) {
		t.Fatalf("without a resolver, got %T, want a resolver over the deployment runtime", got)
	}
	for name, runtime := range map[string]contextfabric.ModelRuntime{
		"nil":                 nil,
		"typed nil":           typedNil,
		"no planning support": &plainDeploymentRuntime{},
	} {
		if got := readPlanningRuntimeFor(nil, runtime); got != nil {
			t.Fatalf("%s deployment runtime: got %T, want nil", name, got)
		}
	}
}
