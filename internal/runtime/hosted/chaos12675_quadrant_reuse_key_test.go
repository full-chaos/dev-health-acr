package hosted

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-12675: the quadrant switch reaches the reuse key from the same config
// field the engine's selection reads.
func TestBuildContextFabricInvestigator_keysReuseByTheQuadrantSwitch(t *testing.T) {
	configureGraph(t)
	versions := map[bool]string{}
	for _, on := range []bool{false, true} {
		request, postgres, clickhouse := investigatorBuildRequest(t)
		request.config.AnswerReuseMaxAge = time.Hour
		request.config.ContextFabricLandscapeQuadrantEnabled = on
		investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)
		if err != nil {
			t.Fatalf("buildContextFabricInvestigator() = %v", err)
		}
		engine, ok := investigator.(*contextfabric.Engine)
		if !ok {
			t.Fatalf("investigator is a %T, want *contextfabric.Engine", investigator)
		}
		authorities := mustField(t, reflect.ValueOf(engine).Elem(), "reuseVersionAuthorities")
		got := mustField(t, authorities, "QuestionFamilyVersion").String()
		if want := contextfabric.ReuseQuestionFamilyVersion(contextfabric.RenderShapeOptions{LandscapePosition: on}); got != want {
			t.Errorf("quadrant on=%v: QuestionFamilyVersion = %q, want %q", on, got, want)
		}
		versions[on] = got
	}
	if versions[false] == versions[true] {
		t.Fatalf("the quadrant switch does not reach the reuse key: %q either way", versions[false])
	}
}
