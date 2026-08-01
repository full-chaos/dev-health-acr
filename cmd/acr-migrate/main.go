package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/config"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

const migrationDSNEnvironment = "ACR_POSTGRES_MIGRATION_DSN"
const poolerAdminDSNEnvironment = "ACR_POSTGRES_MIGRATION_POOLER_ADMIN_DSN"
const migrationConnectionKindEnvironment = "ACR_POSTGRES_CONNECTION_KIND"

type lookupEnv func(string) (string, bool)

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
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn, PoolerAdminDSN: poolerAdminDSN})
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
