package main

import (
	"context"
	"strings"
	"testing"
)

// TestOpenPriorsDB_zeroEnvRefusesNamingPostgresOnly is r2 P1 finding 1's
// own pin at the cmd/acr-projector entry point actually used by every
// priors subcommand (curate, flip, rollback, revoke): openPriorsDB must
// refuse a zero-env start naming ONLY ACR_POSTGRES_DSN, never
// ACR_CLICKHOUSE_DSN (which priors never opens -- see openPriorsDB's own
// doc comment). Reproduced live before the fix:
//
//	$ env -i PATH=/usr/bin:/bin acr-projector priors flip --org org-review --version 1 --by operator
//	configuration: ACR_CLICKHOUSE_DSN is required when backing stores are required
func TestOpenPriorsDB_zeroEnvRefusesNamingPostgresOnly(t *testing.T) {
	t.Setenv("ACR_ENVIRONMENT", "development")
	for _, key := range []string{
		"ACR_LOCAL_COMPOSITION_READY", "ACR_REQUIRE_BACKING_STORES",
		"ACR_POSTGRES_DSN", "ACR_POSTGRES_CONNECTION_KIND", "ACR_CLICKHOUSE_DSN",
	} {
		t.Setenv(key, "")
	}
	_, err := openPriorsDB(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ACR_POSTGRES_DSN") {
		t.Fatalf("openPriorsDB() error = %v, want an ACR_POSTGRES_DSN refusal", err)
	}
	if strings.Contains(err.Error(), "ACR_CLICKHOUSE_DSN") {
		t.Fatalf("openPriorsDB() error = %v, must never name ACR_CLICKHOUSE_DSN -- priors never opens it", err)
	}
}

// TestOpenPriorsDB_postgresOnlyConfigurationReachesThePostgresOpenPath is
// r2 P1 finding 1's second pin: a fully-valid Postgres-only environment
// (no ClickHouse DSN configured at all) must pass CONFIGURATION and reach
// openPriorsDB's own postgres-open path -- proven by the error changing
// from a "configuration:"-prefixed refusal to an "open postgres:" failure
// against a deliberately unreachable DSN (this test asserts the error
// CLASS changed, not connectivity, which needs no real database).
func TestOpenPriorsDB_postgresOnlyConfigurationReachesThePostgresOpenPath(t *testing.T) {
	t.Setenv("ACR_POSTGRES_DSN", "postgres://nouser:nopass@127.0.0.1:1/nodb?sslmode=disable")
	t.Setenv("ACR_POSTGRES_CONNECTION_KIND", "direct")
	t.Setenv("ACR_CLICKHOUSE_DSN", "")
	_, err := openPriorsDB(context.Background())
	if err == nil {
		t.Fatal("openPriorsDB() unexpectedly succeeded against an unreachable Postgres DSN")
	}
	if strings.HasPrefix(err.Error(), "configuration:") {
		t.Fatalf("openPriorsDB() error = %v, is still a configuration refusal -- the Postgres-only environment should have passed validation", err)
	}
	if !strings.Contains(err.Error(), "open postgres") {
		t.Fatalf("openPriorsDB() error = %v, want an \"open postgres\" failure (past configuration)", err)
	}
}
