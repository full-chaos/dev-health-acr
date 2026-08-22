package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

const migrationDSNEnvironment = "ACR_POSTGRES_MIGRATION_DSN"
const poolerAdminDSNEnvironment = "ACR_POSTGRES_MIGRATION_POOLER_ADMIN_DSN"
const migrationConnectionKindEnvironment = "ACR_POSTGRES_CONNECTION_KIND"

// migrationConnectRetriesEnvironment/migrationConnectRetryBackoffEnvironment
// (CHAOS-4116, the 2026-08-22 A/B incident -- see the scoped-kill skill):
// runtimepostgres.Open already bounds each attempt to Config.PingTimeout's
// 5s default, so a hung handshake fails fast rather than blocking forever;
// what was missing is trying AGAIN. Defaults (3 attempts, 2s backoff)
// leave every existing invocation's behavior on a FIRST-TRY success
// unchanged -- only a failing first attempt now costs a few extra
// seconds instead of the whole run.
const migrationConnectRetriesEnvironment = "ACR_POSTGRES_MIGRATION_CONNECT_RETRIES"
const migrationConnectRetryBackoffEnvironment = "ACR_POSTGRES_MIGRATION_CONNECT_RETRY_BACKOFF_MS"
const defaultMigrationConnectRetries = 3
const defaultMigrationConnectRetryBackoff = 2 * time.Second

type lookupEnv func(string) (string, bool)

// migrateOpen is the injection point runtimepostgres.Open is called
// through -- overridden in tests with a fake opener so the retry loop
// itself (attempt counting, backoff, error propagation) is provable
// without a real, deliberately-failing postgres connection to race
// against.
var migrateOpen = runtimepostgres.Open

// positiveIntEnv parses a positive-integer env var, falling back to
// def when absent, blank, non-numeric, or non-positive -- a malformed
// override degrades to the safe default rather than a startup crash for
// a knob this tool has never required before.
func positiveIntEnv(lookup lookupEnv, key string, def int) int {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// openMigrationDB retries migrateOpen up to attempts times (CHAOS-4116):
// a transient dial/ping failure -- the 2026-08-22 A/B incident's own
// signature, a Docker Desktop host port-forward handshake hang -- no
// longer costs the whole migration run. Every attempt's outcome is
// written to output (the SAME writer "applied N migrations" already uses)
// so a retry's occurrence is part of the run's own log, not silent.
func openMigrationDB(ctx context.Context, cfg runtimepostgres.Config, attempts int, backoff time.Duration, sleep func(time.Duration), output io.Writer) (*sql.DB, error) {
	return openMigrationDBWithOpener(ctx, cfg, attempts, backoff, sleep, output, migrateOpen)
}

// openMigrationDBWithOpener is openMigrationDB's real implementation, with
// the opener call injectable -- tests exercise the retry loop itself
// (attempt counting, backoff placement, error propagation, logging)
// against a fake opener, never a real connection racing a real failure.
func openMigrationDBWithOpener(ctx context.Context, cfg runtimepostgres.Config, attempts int, backoff time.Duration, sleep func(time.Duration), output io.Writer, opener func(context.Context, runtimepostgres.Config) (*sql.DB, error)) (*sql.DB, error) {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		db, err := opener(ctx, cfg)
		if err == nil {
			if attempt > 1 {
				fmt.Fprintf(output, "connected to PostgreSQL on attempt %d/%d\n", attempt, attempts)
			}
			return db, nil
		}
		lastErr = err
		fmt.Fprintf(output, "connect attempt %d/%d failed: %v\n", attempt, attempts, err)
		if attempt < attempts {
			sleep(backoff)
		}
	}
	return nil, lastErr
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.LookupEnv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookup lookupEnv, output io.Writer) error {
	if len(args) != 1 || (args[0] != "up" && args[0] != "status") {
		return errors.New("invalid arguments: use acr-migrate up or acr-migrate status")
	}
	dsn, err := config.SecretValue(lookup, migrationDSNEnvironment)
	if err != nil {
		return err
	}
	if dsn == "" {
		return fmt.Errorf("%s or %s_FILE is required", migrationDSNEnvironment, migrationDSNEnvironment)
	}
	poolerAdminDSN, err := config.SecretValue(lookup, poolerAdminDSNEnvironment)
	if err != nil {
		return err
	}
	if err := validateDeclaredMigrationConnectionKind(lookup, poolerAdminDSN); err != nil {
		return err
	}
	attempts := positiveIntEnv(lookup, migrationConnectRetriesEnvironment, defaultMigrationConnectRetries)
	backoffMS := positiveIntEnv(lookup, migrationConnectRetryBackoffEnvironment, int(defaultMigrationConnectRetryBackoff/time.Millisecond))
	db, err := openMigrationDB(ctx, runtimepostgres.Config{DSN: dsn, PoolerAdminDSN: poolerAdminDSN}, attempts, time.Duration(backoffMS)*time.Millisecond, time.Sleep, output)
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer db.Close()
	runner, err := migrations.Embedded()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	switch args[0] {
	case "up":
		count, err := runner.Apply(ctx, db)
		if err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		if count == 0 {
			_, err := fmt.Fprintln(output, "no migrations applied")
			return err
		}
		_, err = fmt.Fprintf(output, "applied %d migrations\n", count)
		return err
	case "status":
		status, err := runner.Status(ctx, db)
		if err != nil {
			return fmt.Errorf("read migration status: %w", err)
		}
		if len(status) == 0 {
			_, err := fmt.Fprintln(output, "no migrations applied")
			return err
		}
		for _, migration := range status {
			if _, err := fmt.Fprintf(output, "%04d %s\n", migration.Version, migration.Name); err != nil {
				return fmt.Errorf("write migration status: %w", err)
			}
		}
		return nil
	default:
		return errors.New("invalid arguments")
	}
}

// validateDeclaredMigrationConnectionKind rejects a declared
// ACR_POSTGRES_CONNECTION_KIND that contradicts the presence of a PgBouncer
// administration DSN, applying the same rule the hosted server enforces. The
// declaration is optional for this administrative CLI; when absent, the
// PgBouncer administration DSN alone continues to control the session-mode
// probe.
func validateDeclaredMigrationConnectionKind(lookup lookupEnv, poolerAdminDSN string) error {
	raw, declared := lookup(migrationConnectionKindEnvironment)
	if !declared || strings.TrimSpace(raw) == "" {
		return nil
	}
	kind, err := runtimepostgres.ParseConnectionKind(raw)
	if err != nil {
		return err
	}
	return runtimepostgres.ValidateConnectionKind(kind, poolerAdminDSN)
}
