package config

import "testing"

// The anchor binding follow-up switch is off unless a deployment turns it on,
// and a value that is not a boolean is refused.
func TestTheAnchorBindingFollowUpSwitchDefaultsOff(t *testing.T) {
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
			if key == "ACR_CONTEXT_FABRIC_ANCHOR_BINDING_FOLLOW_UP_ENABLED" && tc.set {
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
		if cfg.AnchorBindingFollowUpEnabled != tc.want {
			t.Errorf("value %q (set=%v): enabled = %v, want %v", tc.value, tc.set, cfg.AnchorBindingFollowUpEnabled, tc.want)
		}
	}
	found := false
	attrs := Config{AnchorBindingFollowUpEnabled: true}.SafeAttributes()
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == "context_fabric_anchor_binding_follow_up_enabled" && attrs[i+1] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("the startup attributes do not report the switch")
	}
}
