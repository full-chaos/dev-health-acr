package config

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestProjectorWindowPassIntervalDomain(t *testing.T) {
	if defaultProjectorWindowPassInterval != devhealthsource.DefaultWindowPassInterval {
		t.Fatalf("config default %s differs from the source default %s", defaultProjectorWindowPassInterval, devhealthsource.DefaultWindowPassInterval)
	}
	for _, d := range []time.Duration{0, defaultProjectorWindowPassInterval} {
		if _, err := (&devhealthsource.ClickHouseProjectionSource{}).WithWindowPassInterval(d); err != nil {
			t.Fatalf("interval %s refused by the clickhouse source: %v", d, err)
		}
		if _, err := (&devhealthsource.TeamsProjectsSource{}).WithWindowPassInterval(d); err != nil {
			t.Fatalf("interval %s refused by the teams source: %v", d, err)
		}
	}
	if _, err := (&devhealthsource.ClickHouseProjectionSource{}).WithWindowPassInterval(-time.Second); err == nil {
		t.Fatal("a negative interval was accepted by the clickhouse source")
	}
	if _, err := (&devhealthsource.TeamsProjectsSource{}).WithWindowPassInterval(-time.Second); err == nil {
		t.Fatal("a negative interval was accepted by the teams source")
	}
	for _, testCase := range []struct {
		name, value string
		want        time.Duration
		refused     bool
	}{
		{name: "unset", value: "", want: defaultProjectorWindowPassInterval},
		{name: "explicit", value: "2m", want: 2 * time.Minute},
		{name: "zero is the rollback", value: "0s", want: 0},
		{name: "negative refused", value: "-5m", refused: true},
		{name: "unparseable", value: "soon", refused: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := projectionEnvironment(map[string]string{"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "true"})
			if testCase.value != "" {
				env["ACR_CONTEXT_FABRIC_PROJECTOR_WINDOW_PASS_INTERVAL"] = testCase.value
			}
			cfg, err := loadProjector(mapLookup(env), requiredStoresAll)
			if testCase.refused {
				if err == nil {
					t.Fatalf("interval %q accepted, want refused", testCase.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("interval %q: %v", testCase.value, err)
			}
			if cfg.WindowPassInterval != testCase.want {
				t.Fatalf("WindowPassInterval = %s, want %s", cfg.WindowPassInterval, testCase.want)
			}
		})
	}
}

func TestSafeAttributesDiscloseTheWindowPassInterval(t *testing.T) {
	attributes := ProjectorConfig{WindowPassInterval: 7 * time.Minute}.SafeAttributes()
	values := map[string]any{}
	for i := 0; i+1 < len(attributes); i += 2 {
		if key, ok := attributes[i].(string); ok {
			values[key] = attributes[i+1]
		}
	}
	if got, want := values["window_pass_interval"], (7 * time.Minute).String(); got != want {
		t.Errorf("SafeAttributes window_pass_interval = %v, want %q", got, want)
	}
}
