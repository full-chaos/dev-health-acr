package config

import (
	"log/slog"
	"testing"
)

func mapLookup(values map[string]string) lookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != ":8080" || cfg.Environment != "development" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("unexpected log level: %v", cfg.LogLevel)
	}
	if cfg.RequireBackingStores {
		t.Fatal("development must not require backing stores by default")
	}
}

func TestProductionRequiresBackingStores(t *testing.T) {
	_, err := load(mapLookup(map[string]string{"ACR_ENVIRONMENT": "production"}))
	if err == nil {
		t.Fatal("expected production configuration error")
	}
}

func TestProductionAcceptsConfiguredBackingStores(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{
		"ACR_ENVIRONMENT":    "production",
		"ACR_CLICKHOUSE_DSN": "clickhouse://redacted",
		"ACR_POSTGRES_DSN":   "postgres://redacted",
		"ACR_LOG_LEVEL":      "warn",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireBackingStores || cfg.LogLevel != slog.LevelWarn {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestSafeAttributesDoNotContainDSNs(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{
		"ACR_CLICKHOUSE_DSN": "clickhouse://user:secret@example",
		"ACR_POSTGRES_DSN":   "postgres://user:secret@example",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range cfg.SafeAttributes() {
		if text, ok := value.(string); ok && (text == cfg.ClickHouseDSN || text == cfg.PostgresDSN) {
			t.Fatal("safe attributes leaked a DSN")
		}
	}
}
