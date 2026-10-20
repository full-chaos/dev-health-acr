package config

import (
	"strings"
	"testing"
)

func TestServiceModelDefaultIsOffUnlessSetOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		open bool
	}{
		{name: "unset", env: map[string]string{}},
		{name: "empty", env: map[string]string{"ACR_MCP_SERVICE_MODEL_DEFAULT": ""}},
		{name: "off", env: map[string]string{"ACR_MCP_SERVICE_MODEL_DEFAULT": "off"}},
		{name: "on", env: map[string]string{"ACR_MCP_SERVICE_MODEL_DEFAULT": "on"}, open: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.env["ACR_LOCAL_COMPOSITION_READY"] = "true"
			cfg, err := load(mapLookup(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ServiceModelOpen != tc.open {
				t.Fatalf("ServiceModelOpen = %t, want %t", cfg.ServiceModelOpen, tc.open)
			}
		})
	}
}

func TestServiceModelDefaultRejectsAnyOtherValue(t *testing.T) {
	for _, value := range []string{"true", "ON", "1", "enabled"} {
		_, err := load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true", "ACR_MCP_SERVICE_MODEL_DEFAULT": value}))
		if err == nil || !strings.Contains(err.Error(), "ACR_MCP_SERVICE_MODEL_DEFAULT") {
			t.Fatalf("load() with %q error = %v, want a refusal naming ACR_MCP_SERVICE_MODEL_DEFAULT", value, err)
		}
	}
}

func TestServiceModelDefaultOnIsRefusedOnlyInProduction(t *testing.T) {
	for _, tc := range []struct {
		environment string
		value       string
		refused     bool
	}{
		{environment: "production", value: "on", refused: true},
		{environment: "production", value: "off"},
		{environment: "staging", value: "on"},
		{environment: "test", value: "on"},
		{environment: "development", value: "on"},
	} {
		t.Run(tc.environment+"/"+tc.value, func(t *testing.T) {
			values := completeRuntimeEnvironment()
			values["ACR_ENVIRONMENT"] = tc.environment
			values["ACR_MCP_SERVICE_MODEL_DEFAULT"] = tc.value
			cfg, err := load(mapLookup(values))
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "ACR_MCP_SERVICE_MODEL_DEFAULT") {
					t.Fatalf("load() error = %v, want a refusal naming ACR_MCP_SERVICE_MODEL_DEFAULT", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ServiceModelOpen != (tc.value == "on") {
				t.Fatalf("ServiceModelOpen = %t for %q", cfg.ServiceModelOpen, tc.value)
			}
		})
	}
}
