package config

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// CHAOS-7263: the trailing overlap window is configurable, defaults to 15m,
// and must be > 0 (a zero window would silently disable late-arrival recovery).
func TestProjectorOverlapDomain(t *testing.T) {
	if _, err := (&devhealthsource.ClickHouseProjectionSource{}).WithOverlap(defaultProjectorOverlap); err != nil {
		t.Fatalf("the default overlap %s is refused by the source: %v", defaultProjectorOverlap, err)
	}
	for _, testCase := range []struct {
		name, value string
		want        time.Duration
		refused     bool
	}{
		{name: "unset", value: "", want: defaultProjectorOverlap},
		{name: "explicit", value: "30m", want: 30 * time.Minute},
		{name: "zero refused", value: "0s", refused: true},
		{name: "negative refused", value: "-5m", refused: true},
		{name: "unparseable", value: "soon", refused: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := projectionEnvironment(map[string]string{"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "true"})
			if testCase.value != "" {
				env["ACR_CONTEXT_FABRIC_PROJECTOR_OVERLAP"] = testCase.value
			}
			cfg, err := loadProjector(mapLookup(env), requiredStoresAll)
			if testCase.refused {
				if err == nil {
					t.Fatalf("overlap %q accepted, want refused", testCase.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("overlap %q: %v", testCase.value, err)
			}
			if cfg.Overlap != testCase.want {
				t.Fatalf("Overlap = %s, want %s", cfg.Overlap, testCase.want)
			}
		})
	}
}
