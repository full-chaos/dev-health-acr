package hosted

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-12626: the landscape quadrant switch reaches the engine's selection from
// the deployment's config.
func TestBuildContextFabricInvestigator_wiresTheLandscapeQuadrantSwitch(t *testing.T) {
	configureGraph(t)
	for _, on := range []bool{false, true} {
		request, postgres, clickhouse := investigatorBuildRequest(t)
		request.config.ContextFabricLandscapeQuadrantEnabled = on
		investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)
		if err != nil {
			t.Fatalf("buildContextFabricInvestigator() = %v", err)
		}
		engine, ok := investigator.(*contextfabric.Engine)
		if !ok {
			t.Fatalf("investigator is a %T, want *contextfabric.Engine", investigator)
		}
		options := mustField(t, reflect.ValueOf(engine).Elem(), "renderShapeOptions")
		if got := mustField(t, options, "LandscapePosition").Bool(); got != on {
			t.Fatalf("config on=%v: the engine's LandscapePosition = %v", on, got)
		}
	}
}
