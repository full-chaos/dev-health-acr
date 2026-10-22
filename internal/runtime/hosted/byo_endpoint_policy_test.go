package hosted

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

func TestBYOAllowNonPublicBaseURLFromEnvDomain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		present  bool
		value    string
		want     bool
		wantWarn bool
	}{
		{name: "absent is off, silently"},
		{name: "blank is off, silently", present: true, value: "  "},
		{name: "false is off, silently", present: true, value: "false"},
		{name: "0 is off, silently", present: true, value: "0"},
		{name: "true is on, with one warning", present: true, value: "true", want: true, wantWarn: true},
		{name: "padded TRUE is on", present: true, value: " TRUE ", want: true, wantWarn: true},
		{name: "1 is on", present: true, value: "1", want: true, wantWarn: true},
		{name: "yes is malformed: off, with a warning", present: true, value: "yes", wantWarn: true},
		{name: "garbage is malformed: off, with a warning", present: true, value: "allow-all", wantWarn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			if tc.present {
				values[EnvBYOAllowNonPublicBaseURL] = tc.value
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			if got := byoAllowNonPublicBaseURLFromEnv(envLookup(values), logger); got != tc.want {
				t.Fatalf("allowance = %v, want %v", got, tc.want)
			}
			warned := strings.Count(logs.String(), EnvBYOAllowNonPublicBaseURL)
			if tc.wantWarn && warned != 1 || !tc.wantWarn && warned != 0 {
				t.Fatalf("log names the variable %d time(s), want warn=%v: %q", warned, tc.wantWarn, logs.String())
			}
		})
	}
}

// The allowance reaches the per-organization defaults on both composition
// shapes: a deployment with a platform provider and one without.
func TestContextFabricModelDefaultsCarryTheBYOAllowance(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"platform provider configured": {modelprovider.EnvAPIKey: "sk-test", EnvBYOAllowNonPublicBaseURL: "true"},
		"no platform provider":         {EnvBYOAllowNonPublicBaseURL: "true"},
	} {
		t.Run(name, func(t *testing.T) {
			defaults, err := contextFabricModelDefaults(envLookup(values), discardLogger())
			if err != nil {
				t.Fatal(err)
			}
			if !defaults.AllowNonPublicBYOEndpoint {
				t.Fatal("the allowance did not reach the per-organization defaults")
			}
			delete(values, EnvBYOAllowNonPublicBaseURL)
			defaults, err = contextFabricModelDefaults(envLookup(values), discardLogger())
			if err != nil || defaults.AllowNonPublicBYOEndpoint {
				t.Fatalf("without the variable: err = %v allowance = %v, want off", err, defaults.AllowNonPublicBYOEndpoint)
			}
		})
	}
}

// The deployment-default (platform) model runtime is never held to the rule:
// the allowance is not part of what it reads, and it never sets the rule.
func TestThePlatformModelConfigIsNotHeldToThePublicEndpointRule(t *testing.T) {
	config, err := contextFabricModelConfigFromEnv(envLookup(map[string]string{modelprovider.EnvAPIKey: "sk-test"}), nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if config.PublicEndpointsOnly {
		t.Fatal("the platform model config dials public addresses only")
	}
}
