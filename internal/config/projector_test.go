package config

import "testing"

func TestLoadProjectorDefaults(t *testing.T) {
	cfg, err := loadProjector(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != defaultProjectorListenAddress {
		t.Fatalf("listen address = %q", cfg.ListenAddress)
	}
	if cfg.ProjectionEnabled {
		t.Fatal("projection must be disabled by default")
	}
	if cfg.PollInterval != defaultProjectionPollInterval || cfg.Concurrency != defaultProjectionConcurrency {
		t.Fatalf("unexpected scheduling defaults: %#v", cfg)
	}
	if cfg.RequireBackingStores {
		t.Fatal("development must not require backing stores by default")
	}
}

func TestLoadProjectorProductionRequiresBackingStores(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{"ACR_ENVIRONMENT": "production"}))
	if err == nil {
		t.Fatal("expected an error: production requires ACR_CLICKHOUSE_DSN/ACR_POSTGRES_DSN")
	}
}

func TestLoadProjectorEnabledProductionRequiresOrgAllowlist(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{
		"ACR_ENVIRONMENT": "production", "ACR_CLICKHOUSE_DSN": "https://clickhouse.internal", "ACR_POSTGRES_DSN": "postgres://db/acr",
		"ACR_POSTGRES_CONNECTION_KIND": "direct", "ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "true",
	}))
	if err == nil {
		t.Fatal("expected an error: enabling projection without an organization allowlist")
	}
}

func TestLoadProjectorParsesOrgAllowlistAndScheduling(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED":       "true",
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS":        "org-1, org-2 ,org-3",
		"ACR_CONTEXT_FABRIC_PROJECTION_POLL_INTERVAL": "30s",
		"ACR_CONTEXT_FABRIC_PROJECTION_CONCURRENCY":   "10",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OrgIDs) != 3 || cfg.OrgIDs[0] != "org-1" || cfg.OrgIDs[2] != "org-3" {
		t.Fatalf("org ids = %#v", cfg.OrgIDs)
	}
	if cfg.PollInterval.String() != "30s" || cfg.Concurrency != 10 {
		t.Fatalf("scheduling = %#v", cfg)
	}
}

func TestLoadProjectorRejectsInvalidEnvironment(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{"ACR_ENVIRONMENT": "sandbox"}))
	if err == nil {
		t.Fatal("expected an error for an invalid environment")
	}
}

func TestProjectorConfigSafeAttributesOmitDSNs(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{"ACR_POSTGRES_DSN": "postgres://secret@db/acr"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, attr := range cfg.SafeAttributes() {
		if text, ok := attr.(string); ok && text == "postgres://secret@db/acr" {
			t.Fatal("SafeAttributes leaked a DSN")
		}
	}
}
