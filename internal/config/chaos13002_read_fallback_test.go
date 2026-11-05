package config

import "testing"

// CHAOS-13002: the read fallback switch is off unless a deployment turns it
// on, a value that is not a boolean is refused, and the startup attributes
// report it. Off keeps every answer on its fixed family's plan.
func TestTheReadFallbackSwitchDefaultsOff(t *testing.T) {
	if ContextFabricReadFallbackEnabledEnvVar != "ACR_CONTEXT_FABRIC_READ_FALLBACK_ENABLED" {
		t.Fatalf("env var %q", ContextFabricReadFallbackEnabledEnvVar)
	}
	for _, tc := range []struct {
		value   string
		set     bool
		want    bool
		wantErr bool
	}{
		{set: false, want: false},
		{value: "true", set: true, want: true},
		{value: "false", set: true, want: false},
		{value: "sometimes", set: true, wantErr: true},
	} {
		lookup := func(key string) (string, bool) {
			if key == "ACR_CONTEXT_FABRIC_READ_FALLBACK_ENABLED" && tc.set {
				return tc.value, true
			}
			return "", false
		}
		var cfg Config
		err := loadHostedRuntimeValues(lookup, &cfg, false, false, false)
		if tc.wantErr {
			if err == nil {
				t.Errorf("value %q: no error", tc.value)
			}
			continue
		}
		if err != nil {
			t.Fatalf("value %q: %v", tc.value, err)
		}
		if cfg.ContextFabricReadFallbackEnabled != tc.want {
			t.Errorf("value %q (set=%v): enabled = %v, want %v", tc.value, tc.set, cfg.ContextFabricReadFallbackEnabled, tc.want)
		}
	}
	for _, enabled := range []bool{true, false} {
		attrs := Config{ContextFabricReadFallbackEnabled: enabled}.SafeAttributes()
		found := false
		for i := 0; i+1 < len(attrs); i += 2 {
			if attrs[i] == "context_fabric_read_fallback_enabled" && attrs[i+1] == enabled {
				found = true
			}
		}
		if !found {
			t.Errorf("SafeAttributes does not report enabled=%v", enabled)
		}
	}
}
