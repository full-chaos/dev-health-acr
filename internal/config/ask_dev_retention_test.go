package config

import "testing"

// CHAOS-11952: Ask Dev retention is one switch, default off. CHAOS-12244: the
// job runs as the runtime role, so the switch alone turns it on; a retention
// DSN left in the environment is ignored, not required.
func TestLoad_askDevRetention(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true"}))
	if err != nil || cfg.AskDevRetentionEnabled {
		t.Fatalf("default: enabled=%t err=%v, want off", cfg.AskDevRetentionEnabled, err)
	}
	cfg, err = load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true", "ACR_ASK_DEV_RETENTION_ENABLED": "true"}))
	if err != nil || !cfg.AskDevRetentionEnabled {
		t.Fatalf("on with no other setting: enabled=%t err=%v, want on", cfg.AskDevRetentionEnabled, err)
	}
	if _, err = load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true", "ACR_ASK_DEV_RETENTION_ENABLED": "soon"})); err == nil {
		t.Fatal("ACR_ASK_DEV_RETENTION_ENABLED=soon was accepted")
	}
}
