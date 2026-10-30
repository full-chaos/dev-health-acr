package config

import "testing"

// CHAOS-11911: the investigation event stream switch is off unless a
// deployment turns it on, a value that is not a boolean is refused, and the
// startup attributes report it.
func TestTheEventStreamSwitchDefaultsOff(t *testing.T) {
	if ContextFabricEventStreamEnabledEnvVar != "ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED" {
		t.Fatalf("env var %q, want ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED", ContextFabricEventStreamEnabledEnvVar)
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
			if key == "ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED" && tc.set {
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
		if cfg.ContextFabricEventStreamEnabled != tc.want {
			t.Errorf("value %q (set=%v): enabled = %v, want %v", tc.value, tc.set, cfg.ContextFabricEventStreamEnabled, tc.want)
		}
	}
	for _, enabled := range []bool{true, false} {
		attrs := Config{ContextFabricEventStreamEnabled: enabled}.SafeAttributes()
		found := false
		for i := 0; i+1 < len(attrs); i += 2 {
			if attrs[i] == "context_fabric_event_stream_enabled" && attrs[i+1] == enabled {
				found = true
			}
		}
		if !found {
			t.Fatalf("the startup attributes do not report the switch as %v", enabled)
		}
	}
}
