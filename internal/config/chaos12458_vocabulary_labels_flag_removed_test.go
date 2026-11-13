package config

import "testing"

// CHAOS-12458: the vocabulary_labels switch is removed. A deployment that still
// sets the old variable must start without error, and the startup attributes
// must no longer report it.
func TestTheRemovedVocabularyLabelsSwitchIsIgnored(t *testing.T) {
	for _, value := range []string{"true", "false", "sometimes"} {
		lookup := func(key string) (string, bool) {
			if key == "ACR_CONTEXT_FABRIC_VOCABULARY_LABELS_ENABLED" {
				return value, true
			}
			return "", false
		}
		var cfg Config
		if err := loadHostedRuntimeValues(lookup, &cfg, false, false, false); err != nil {
			t.Fatalf("value %q: %v", value, err)
		}
	}
	attrs := Config{}.SafeAttributes()
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == "context_fabric_vocabulary_labels_enabled" {
			t.Fatalf("SafeAttributes still reports the removed switch")
		}
	}
}
