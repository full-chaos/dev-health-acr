package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

const migrationDSNEnvironment = "ACR_POSTGRES_MIGRATION_DSN"
const poolerAdminDSNEnvironment = "ACR_POSTGRES_MIGRATION_POOLER_ADMIN_DSN"

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
	dsn, ok := lookup(migrationDSNEnvironment)
	if !ok || strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("%s is required", migrationDSNEnvironment)
	}
	poolerAdminDSN, _ := lookup(poolerAdminDSNEnvironment)
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
