package config

import "testing"

// CHAOS-12997: ACR_MCP_SERVICE_MODEL_DEFAULT is not read any more. Any value,
// including the ones that used to be refused (on in production, a stray word),
// loads like an unset one.
func TestTheRemovedServiceModelDefaultSettingNeverFailsALoad(t *testing.T) {
	for _, tc := range []struct{ environment, value string }{
		{"production", "on"}, {"production", "off"}, {"development", "on"}, {"development", "enabled"},
	} {
		values := completeRuntimeEnvironment()
		values["ACR_ENVIRONMENT"] = tc.environment
		values["ACR_MCP_SERVICE_MODEL_DEFAULT"] = tc.value
		if _, err := load(mapLookup(values)); err != nil {
			t.Fatalf("load() with %s/%s = %v, want the setting ignored", tc.environment, tc.value, err)
		}
	}
}
