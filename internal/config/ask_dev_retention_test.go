package config

import (
	"strings"
	"testing"
)

// CHAOS-11952: Ask Dev retention is one switch, default off; on, it needs
// the retention role's own DSN, and the DSN is read only then.
func TestLoad_askDevRetention(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true", "ACR_POSTGRES_RETENTION_DSN": "postgres://acr_retention:x@h/acr"}))
	if err != nil || cfg.AskDevRetentionEnabled || cfg.AskDevRetentionPostgresDSN != "" {
		t.Fatalf("default: enabled=%t dsn set=%t err=%v, want off and the DSN unread", cfg.AskDevRetentionEnabled, cfg.AskDevRetentionPostgresDSN != "", err)
	}
	_, err = load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true", "ACR_ASK_DEV_RETENTION_ENABLED": "true"}))
	if err == nil || !strings.Contains(err.Error(), "ACR_POSTGRES_RETENTION_DSN") {
		t.Fatalf("on without the DSN: err = %v, want a refusal naming ACR_POSTGRES_RETENTION_DSN", err)
	}
	cfg, err = load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true", "ACR_ASK_DEV_RETENTION_ENABLED": "true", "ACR_POSTGRES_RETENTION_DSN": "postgres://acr_retention:x@h/acr"}))
	if err != nil || !cfg.AskDevRetentionEnabled || cfg.AskDevRetentionPostgresDSN != "postgres://acr_retention:x@h/acr" {
		t.Fatalf("on with the DSN: enabled=%t err=%v", cfg.AskDevRetentionEnabled, err)
	}
	if _, err = load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true", "ACR_ASK_DEV_RETENTION_ENABLED": "soon"})); err == nil {
		t.Fatal("ACR_ASK_DEV_RETENTION_ENABLED=soon was accepted")
	}
}
