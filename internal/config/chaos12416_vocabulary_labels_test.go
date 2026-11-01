package config

import "testing"

// CHAOS-12416: the vocabulary_labels switch is off unless a deployment turns it
// on, a value that is not a boolean is refused, and the startup attributes
// report it. Off is the state that protects a consumer on an old pin.
func TestTheVocabularyLabelsSwitchDefaultsOff(t *testing.T) {
	if ContextFabricVocabularyLabelsEnabledEnvVar != "ACR_CONTEXT_FABRIC_VOCABULARY_LABELS_ENABLED" {
		t.Fatalf("env var %q", ContextFabricVocabularyLabelsEnabledEnvVar)
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
			if key == "ACR_CONTEXT_FABRIC_VOCABULARY_LABELS_ENABLED" && tc.set {
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
		if cfg.ContextFabricVocabularyLabelsEnabled != tc.want {
			t.Errorf("value %q (set=%v): enabled = %v, want %v", tc.value, tc.set, cfg.ContextFabricVocabularyLabelsEnabled, tc.want)
		}
	}
	for _, enabled := range []bool{true, false} {
		attrs := Config{ContextFabricVocabularyLabelsEnabled: enabled}.SafeAttributes()
		found := false
		for i := 0; i+1 < len(attrs); i += 2 {
			if attrs[i] == "context_fabric_vocabulary_labels_enabled" && attrs[i+1] == enabled {
				found = true
			}
		}
		if !found {
			t.Errorf("SafeAttributes does not report enabled=%v", enabled)
		}
	}
}
