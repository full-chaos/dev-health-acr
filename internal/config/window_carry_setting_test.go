package config

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestProjectorWindowCarryDomain(t *testing.T) {
	if !(&devhealthsource.ClickHouseProjectionSource{}).WithWindowCarry(true).WindowCarry() || (&devhealthsource.TeamsProjectsSource{}).WithWindowCarry(false).WindowCarry() {
		t.Fatal("WithWindowCarry does not set the value it was given")
	}
	for _, testCase := range []struct {
		name, value string
		want        bool
		refused     bool
	}{
		{name: "unset is on", value: "", want: true},
		{name: "explicit true", value: "true", want: true},
		{name: "false is the rollback", value: "false", want: false},
		{name: "zero is false", value: "0", want: false},
		{name: "unparseable", value: "maybe", refused: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := projectionEnvironment(map[string]string{"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY": "true"})
			if testCase.value != "" {
				env["ACR_CONTEXT_FABRIC_PROJECTOR_WINDOW_CARRY"] = testCase.value
			}
			cfg, err := loadProjector(mapLookup(env), requiredStoresAll)
			if testCase.refused {
				if err == nil {
					t.Fatalf("value %q accepted, want refused", testCase.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("value %q: %v", testCase.value, err)
			}
			if cfg.WindowCarry != testCase.want {
				t.Fatalf("WindowCarry = %v, want %v", cfg.WindowCarry, testCase.want)
			}
		})
	}
}

func TestSafeAttributesDiscloseTheWindowCarry(t *testing.T) {
	for _, carry := range []bool{false, true} {
		attributes := ProjectorConfig{WindowCarry: carry}.SafeAttributes()
		values := map[string]any{}
		for i := 0; i+1 < len(attributes); i += 2 {
			if key, ok := attributes[i].(string); ok {
				values[key] = attributes[i+1]
			}
		}
		if got := values["window_carry"]; got != carry {
			t.Errorf("SafeAttributes window_carry = %v, want %v", got, carry)
		}
	}
}
