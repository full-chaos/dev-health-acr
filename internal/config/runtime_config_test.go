package config

import (
	"strings"
	"testing"
	"time"

	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

func TestLoad_requires_complete_runtime_when_backing_stores_are_explicit(t *testing.T) {
	tests := []struct {
		name    string
		missing string
	}{
		{name: "postgres", missing: "ACR_POSTGRES_DSN"},
		{name: "clickhouse", missing: "ACR_CLICKHOUSE_DSN"},
		{name: "evidence key id", missing: "ACR_EVIDENCE_ID_ACTIVE_KID"},
		{name: "evidence keys", missing: "ACR_EVIDENCE_ID_KEYS"},
		{name: "device verification URL", missing: "ACR_DEVICE_VERIFICATION_URL"},
		{name: "postgres connection kind", missing: "ACR_POSTGRES_CONNECTION_KIND"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			values := completeRuntimeEnvironment()
			delete(values, test.missing)

			// When
			_, err := load(mapLookup(values))

			// Then
			if err == nil || !strings.Contains(err.Error(), test.missing) {
				t.Fatalf("load() error = %v, want missing %s", err, test.missing)
			}
		})
	}
}

func TestLoad_selects_local_entitlement_without_remote_configuration_in_development_and_test(t *testing.T) {
	for _, environment := range []string{"development", "test"} {
		t.Run(environment, func(t *testing.T) {
			values := completeRuntimeEnvironment()
			values["ACR_ENVIRONMENT"] = environment
			delete(values, "ACR_DEV_HEALTH_ENTITLEMENT_URL")
			delete(values, "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE")
			if environment == "development" {
				values["ACR_POSTGRES_DSN"] = "postgres://configured?sslmode=verify-full"
			}

			cfg, err := load(mapLookup(values))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.EntitlementMode(); got != EntitlementModeLocal {
				t.Fatalf("entitlement mode = %q, want %q", got, EntitlementModeLocal)
			}
		})
	}
}

func TestLoad_selects_remote_entitlement_when_URL_and_token_are_complete(t *testing.T) {
	values := completeRuntimeEnvironment()

	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.EntitlementMode(); got != EntitlementModeRemote {
		t.Fatalf("entitlement mode = %q, want %q", got, EntitlementModeRemote)
	}
}

func TestLoad_rejects_partial_remote_entitlement_configuration(t *testing.T) {
	tests := []struct {
		name   string
		remove string
	}{
		{name: "missing URL", remove: "ACR_DEV_HEALTH_ENTITLEMENT_URL"},
		{name: "missing token", remove: "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := completeRuntimeEnvironment()
			delete(values, test.remove)

			_, err := load(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), "must be configured together") {
				t.Fatalf("load() error = %v, want partial entitlement rejection", err)
			}
		})
	}
}

func TestLoad_rejects_local_entitlement_in_staging_and_production(t *testing.T) {
	for _, environment := range []string{"staging", "production"} {
		t.Run(environment, func(t *testing.T) {
			values := completeRuntimeEnvironment()
			values["ACR_ENVIRONMENT"] = environment
			values["ACR_POSTGRES_DSN"] = "postgres://configured?sslmode=verify-full"
			delete(values, "ACR_DEV_HEALTH_ENTITLEMENT_URL")
			delete(values, "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE")

			_, err := load(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), "remote entitlement configuration is required") {
				t.Fatalf("load() error = %v, want remote-only %s rejection", err, environment)
			}
		})
	}
}

func TestLoad_rejects_remote_only_entitlement_options_in_local_mode(t *testing.T) {
	for _, field := range []string{"ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE", "ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL"} {
		t.Run(field, func(t *testing.T) {
			values := completeRuntimeEnvironment()
			delete(values, "ACR_DEV_HEALTH_ENTITLEMENT_URL")
			delete(values, "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE")
			values[field] = "configured"

			_, err := load(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("load() error = %v, want orphaned %s rejection", err, field)
			}
		})
	}
}

func TestLoad_maps_postgres_pool_and_episode_writeback(t *testing.T) {
	// Given
	values := completeRuntimeEnvironment()
	values["ACR_CLICKHOUSE_CA_BUNDLE"] = "/run/secrets/clickhouse-ca.pem"
	values["ACR_POSTGRES_POOLER_ADMIN_DSN"] = "postgres://pooler-admin"
	values["ACR_POSTGRES_CONNECTION_KIND"] = "pgbouncer"
	values["ACR_POSTGRES_MAX_OPEN_CONNS"] = "17"
	values["ACR_POSTGRES_MAX_IDLE_CONNS"] = "6"
	values["ACR_POSTGRES_CONN_MAX_LIFETIME"] = "45m"
	values["ACR_POSTGRES_CONN_MAX_IDLE_TIME"] = "7m"
	values["ACR_POSTGRES_PING_TIMEOUT"] = "4s"
	values["ACR_ENABLE_EPISODE_WRITEBACK"] = "true"

	// When
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if cfg.PostgresPoolerAdminDSN != "postgres://pooler-admin" || cfg.PostgresMaxOpenConns != 17 || cfg.PostgresMaxIdleConns != 6 || !cfg.PostgresMaxIdleConnsConfigured {
		t.Fatalf("unexpected PostgreSQL pool configuration: %#v", cfg)
	}
	if cfg.ClickHouseCACertPath != "/run/secrets/clickhouse-ca.pem" {
		t.Fatalf("ClickHouse CA bundle = %q", cfg.ClickHouseCACertPath)
	}
	if cfg.PostgresConnMaxLifetime != 45*time.Minute || cfg.PostgresConnMaxIdleTime != 7*time.Minute || cfg.PostgresPingTimeout != 4*time.Second {
		t.Fatalf("unexpected PostgreSQL pool durations: %#v", cfg)
	}
	if !cfg.EnableEpisodeWriteback {
		t.Fatal("episode writeback was not enabled explicitly")
	}
}

func TestLoad_preservesExplicitZeroPostgreSQLIdleConnections(t *testing.T) {
	values := completeRuntimeEnvironment()
	values["ACR_POSTGRES_MAX_IDLE_CONNS"] = "0"
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PostgresMaxIdleConnsConfigured || cfg.PostgresMaxIdleConns != 0 {
		t.Fatalf("idle pool config = configured:%t value:%d", cfg.PostgresMaxIdleConnsConfigured, cfg.PostgresMaxIdleConns)
	}
}

func TestLoad_episode_writeback_defaults_disabled(t *testing.T) {
	// When
	cfg, err := load(mapLookup(nil))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableEpisodeWriteback {
		t.Fatal("episode writeback must default to disabled")
	}
}

func TestLoad_accepts_plaintext_PostgreSQL_DSNs_in_production(t *testing.T) {
	values := completeRuntimeEnvironment()
	values["ACR_ENVIRONMENT"] = "production"
	values["ACR_POSTGRES_DSN"] = "postgres://db.internal/acr?sslmode=disable"
	values["ACR_POSTGRES_POOLER_ADMIN_DSN"] = "postgres://pooler.internal/pgbouncer?sslmode=disable"
	values["ACR_POSTGRES_CONNECTION_KIND"] = "pgbouncer"

	cfg, err := load(mapLookup(values))

	if err != nil {
		t.Fatal(err)
	}
	if cfg.PostgresDSN != values["ACR_POSTGRES_DSN"] || cfg.PostgresPoolerAdminDSN != values["ACR_POSTGRES_POOLER_ADMIN_DSN"] {
		t.Fatalf("plaintext PostgreSQL DSNs were not retained: %#v", cfg)
	}
}

func TestLoad_requiresPostgresConnectionKindDirectOrPgBouncer(t *testing.T) {
	// Given
	values := completeRuntimeEnvironment()
	values["ACR_POSTGRES_CONNECTION_KIND"] = "auto"

	// When
	_, err := load(mapLookup(values))

	// Then
	if err == nil || !strings.Contains(err.Error(), "ACR_POSTGRES_CONNECTION_KIND") {
		t.Fatalf("load() error = %v, want connection kind rejection", err)
	}
}

func TestLoad_rejectsPostgresConnectionKindContradictions(t *testing.T) {
	tests := []struct {
		name           string
		kind           string
		poolerAdminDSN string
	}{
		{name: "direct with pooler admin DSN", kind: "direct", poolerAdminDSN: "postgres://pooler-admin"},
		{name: "pgbouncer without pooler admin DSN", kind: "pgbouncer", poolerAdminDSN: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			values := completeRuntimeEnvironment()
			values["ACR_POSTGRES_CONNECTION_KIND"] = test.kind
			if test.poolerAdminDSN == "" {
				delete(values, "ACR_POSTGRES_POOLER_ADMIN_DSN")
			} else {
				values["ACR_POSTGRES_POOLER_ADMIN_DSN"] = test.poolerAdminDSN
			}

			// When
			_, err := load(mapLookup(values))

			// Then
			if err == nil || !strings.Contains(err.Error(), "ACR_POSTGRES_CONNECTION_KIND") {
				t.Fatalf("load() error = %v, want connection kind contradiction rejection", err)
			}
		})
	}
}

func TestLoad_acceptsPgBouncerConnectionKindWithAdminDSN(t *testing.T) {
	// Given
	values := completeRuntimeEnvironment()
	values["ACR_POSTGRES_CONNECTION_KIND"] = "pgbouncer"
	values["ACR_POSTGRES_POOLER_ADMIN_DSN"] = "postgres://pooler-admin"

	// When
	cfg, err := load(mapLookup(values))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PostgresConnectionKind != "pgbouncer" || cfg.PostgresPoolerAdminDSN != "postgres://pooler-admin" {
		t.Fatalf("unexpected PostgreSQL connection kind configuration: %#v", cfg)
	}
}

// TestLoad_defaultsClickHouseMaxBytesToRead is CHAOS-3848's part-1 closure
// test: pre-fix, Config had no ClickHouseMaxBytesToRead field at all, so an
// unset environment left the value that reached
// github.com/full-chaos/dev-health-go/clickhouse.Options at its Go zero (0), which
// applyOptions's own defaultPositiveUint64 fallback happened to paper over
// -- but nothing pinned that the CONFIGURED default was the raised 64 MiB
// ceiling rather than the stale 16 MiB one. This fails red against the old
// 16 MiB constant and green against runtimeclickhouse.DefaultMaxBytesToRead.
func TestLoad_defaultsClickHouseMaxBytesToRead(t *testing.T) {
	// Given
	values := completeRuntimeEnvironment()

	// When
	cfg, err := load(mapLookup(values))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClickHouseMaxBytesToRead != runtimeclickhouse.DefaultMaxBytesToRead {
		t.Fatalf("ClickHouseMaxBytesToRead = %d, want default %d", cfg.ClickHouseMaxBytesToRead, runtimeclickhouse.DefaultMaxBytesToRead)
	}
}

func TestLoad_appliesConfiguredClickHouseMaxBytesToRead(t *testing.T) {
	// Given
	values := completeRuntimeEnvironment()
	values["ACR_CLICKHOUSE_MAX_BYTES_TO_READ"] = "33554432" // 32 MiB

	// When
	cfg, err := load(mapLookup(values))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClickHouseMaxBytesToRead != 32<<20 {
		t.Fatalf("ClickHouseMaxBytesToRead = %d, want %d", cfg.ClickHouseMaxBytesToRead, uint64(32<<20))
	}
}

func TestLoad_rejectsInvalidClickHouseMaxBytesToRead(t *testing.T) {
	for _, value := range []string{"0", "-1", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			// Given
			values := completeRuntimeEnvironment()
			values["ACR_CLICKHOUSE_MAX_BYTES_TO_READ"] = value

			// When
			_, err := load(mapLookup(values))

			// Then
			if err == nil || !strings.Contains(err.Error(), "ACR_CLICKHOUSE_MAX_BYTES_TO_READ") {
				t.Fatalf("load() error = %v, want ACR_CLICKHOUSE_MAX_BYTES_TO_READ rejection", err)
			}
		})
	}
}

func completeRuntimeEnvironment() map[string]string {
	return map[string]string{
		"ACR_ENVIRONMENT":                       "test",
		"ACR_REQUIRE_BACKING_STORES":            "true",
		"ACR_CLICKHOUSE_DSN":                    "clickhouse://configured",
		"ACR_POSTGRES_DSN":                      "postgres://configured",
		"ACR_EVIDENCE_ID_ACTIVE_KID":            "current",
		"ACR_EVIDENCE_ID_KEYS":                  "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        "https://ops.example.test",
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "/run/secrets/ops-token",
		"ACR_DEVICE_VERIFICATION_URL":           "https://verify.example.test/device",
		"ACR_POSTGRES_CONNECTION_KIND":          "direct",
	}
}

func TestLoad_rejectsNonAbsoluteDeviceVerificationURL(t *testing.T) {
	for _, value := range []string{"/device", "verify.example.test/device", "https:///device", "//verify.example.test/device", "https://%zz"} {
		t.Run(value, func(t *testing.T) {
			// Given
			values := completeRuntimeEnvironment()
			values["ACR_DEVICE_VERIFICATION_URL"] = value

			// When
			_, err := load(mapLookup(values))

			// Then
			if err == nil || !strings.Contains(err.Error(), "ACR_DEVICE_VERIFICATION_URL") {
				t.Fatalf("load() error = %v, want device verification URL rejection", err)
			}
		})
	}
}
