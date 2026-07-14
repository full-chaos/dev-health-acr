package config

import (
	"strings"
	"testing"
	"time"
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
		{name: "entitlement URL", missing: "ACR_DEV_HEALTH_ENTITLEMENT_URL"},
		{name: "entitlement token", missing: "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE"},
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

func TestLoad_requires_verified_PostgreSQL_TLS_for_hosted_network_DSNs(t *testing.T) {
	tests := []struct {
		name    string
		primary string
		pooler  string
	}{
		{name: "implicit prefer", primary: "postgres://db.example.test/acr"},
		{name: "disabled", primary: "postgres://db.example.test/acr?sslmode=disable"},
		{name: "encrypted but unverified", primary: "postgres://db.example.test/acr?sslmode=require"},
		{name: "pooler disabled", primary: "postgres://db.example.test/acr?sslmode=verify-full", pooler: "postgres://pooler.example.test/pgbouncer?sslmode=disable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			values := completeRuntimeEnvironment()
			delete(values, "ACR_ALLOW_INSECURE_POSTGRES")
			values["ACR_ENVIRONMENT"] = "production"
			values["ACR_POSTGRES_DSN"] = test.primary
			values["ACR_POSTGRES_POOLER_ADMIN_DSN"] = test.pooler

			// When
			_, err := load(mapLookup(values))

			// Then
			if err == nil || !strings.Contains(err.Error(), "verified TLS") {
				t.Fatalf("load() error = %v, want verified TLS rejection", err)
			}
		})
	}
}

func TestLoad_allows_explicit_insecure_PostgreSQL_only_in_test_environment(t *testing.T) {
	// Given
	values := completeRuntimeEnvironment()
	values["ACR_ENVIRONMENT"] = "test"
	values["ACR_ALLOW_INSECURE_POSTGRES"] = "true"

	// When
	cfg, err := load(mapLookup(values))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowInsecurePostgres {
		t.Fatal("explicit test-only insecure PostgreSQL override was not retained")
	}
	values["ACR_ENVIRONMENT"] = "development"
	if _, err := load(mapLookup(values)); err == nil {
		t.Fatal("insecure PostgreSQL override was accepted outside the test environment")
	}
}

func TestLoad_rejectsInsecurePostgresGloballyEvenWithoutRequiredBackingStores(t *testing.T) {
	// Given
	values := map[string]string{"ACR_ALLOW_INSECURE_POSTGRES": "true"}

	// When
	_, err := load(mapLookup(values))

	// Then
	if err == nil || !strings.Contains(err.Error(), "ACR_ALLOW_INSECURE_POSTGRES") {
		t.Fatalf("load() error = %v, want restricted-to-test rejection even without required backing stores", err)
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

func completeRuntimeEnvironment() map[string]string {
	return map[string]string{
		"ACR_ENVIRONMENT":                       "test",
		"ACR_ALLOW_INSECURE_POSTGRES":           "true",
		"ACR_REQUIRE_BACKING_STORES":            "true",
		"ACR_CLICKHOUSE_DSN":                    "clickhouse://configured",
		"ACR_POSTGRES_DSN":                      "postgres://configured",
		"ACR_EVIDENCE_ID_ACTIVE_KID":            "current",
		"ACR_EVIDENCE_ID_KEYS":                  "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        "https://ops.example.test",
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "/run/secrets/ops-token",
		"ACR_POSTGRES_CONNECTION_KIND":          "direct",
	}
}
