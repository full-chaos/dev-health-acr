package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestRun_usesMigrationDSNOnlyFromEnvironment(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	var stdout bytes.Buffer
	lookup := testMigrationEnvironment(dsn)

	// When
	err := run(ctx, []string{"up"}, lookup, &stdout)

	// Then
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "applied")
}

func TestRun_usesMigrationDSNFromSecretFile(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	path := filepath.Join(t.TempDir(), "migration-dsn")
	require.NoError(t, os.WriteFile(path, []byte("  "+dsn+"\n"), 0o600))
	lookup := environment(map[string]string{
		migrationDSNEnvironment + "_FILE": path,
	})

	// When
	err := run(ctx, []string{"up"}, lookup, &bytes.Buffer{})

	// Then
	require.NoError(t, err)
}

func TestRun_reportsStatusFromEnvironmentConfiguredDSN(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	lookup := testMigrationEnvironment(dsn)
	require.NoError(t, run(ctx, []string{"up"}, lookup, &bytes.Buffer{}))
	var stdout bytes.Buffer

	// When
	err := run(ctx, []string{"status"}, lookup, &stdout)

	// Then
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "0001")
	require.Contains(t, stdout.String(), "0002")
	require.Contains(t, stdout.String(), "0003")
	require.Contains(t, stdout.String(), "0004")
}

func TestRun_reportsAppliedCountAndNoOpDistinctly(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	lookup := testMigrationEnvironment(dsn)
	var first, second bytes.Buffer

	// When
	firstErr := run(ctx, []string{"up"}, lookup, &first)
	secondErr := run(ctx, []string{"up"}, lookup, &second)

	// Then
	require.NoError(t, firstErr)
	// CHAOS-3786 added migration 0012, CHAOS-3781 added 0013, CHAOS-3833
	// added 0014, CHAOS-3862 added 0015, CHAOS-3859 added 0016, and
	// CHAOS-3889 added 0017, so the embedded set is now 17 files. A future
	// migration must bump this literal too -- see
	// expectedMigrationVersions in migrations/postgres/
	// runner_integration_test.go for the same convention, held in one
	// place there.
	require.Equal(t, "applied 17 migrations\n", first.String())
	require.NoError(t, secondErr)
	require.Equal(t, "no migrations applied\n", second.String())
}

func TestRun_reportsExplicitEmptyStatus_whenDatabaseIsFresh(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	lookup := testMigrationEnvironment(dsn)
	var stdout bytes.Buffer

	// When
	err := run(ctx, []string{"status"}, lookup, &stdout)

	// Then
	require.NoError(t, err)
	require.Equal(t, "no migrations applied\n", stdout.String())
}

func TestRun_rejectsDSNCommandLineFlag(t *testing.T) {
	// Given
	var stdout bytes.Buffer
	lookup := environment(map[string]string{migrationDSNEnvironment: "postgres://acr:secret@db/acr"})

	// When
	err := run(context.Background(), []string{"up", "--dsn", "postgres://acr:leak@db/acr"}, lookup, &stdout)

	// Then
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid arguments")
	require.NotContains(t, err.Error(), "leak")
}

func TestRun_requiresMigrationDSNEnvironment(t *testing.T) {
	// Given
	var stdout bytes.Buffer

	// When
	err := run(context.Background(), []string{"status"}, environment(nil), &stdout)

	// Then
	require.Error(t, err)
	require.Contains(t, err.Error(), migrationDSNEnvironment)
}

func TestRun_acceptsPlaintextMigrationDSNOutsideTestEnvironment(t *testing.T) {
	// Given
	lookup := environment(map[string]string{migrationDSNEnvironment: "postgres://user:sentinel-secret@db.example/acr?sslmode=disable"})

	// When
	err := run(context.Background(), []string{"status"}, lookup, &bytes.Buffer{})

	// Then
	require.ErrorContains(t, err, "PostgreSQL is unavailable")
	require.NotContains(t, err.Error(), "sentinel-secret")
	require.NotContains(t, err.Error(), "verified TLS")
}

func TestRun_rejectsDeclaredConnectionKindContradictions(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "direct with pooler admin DSN", values: map[string]string{
			migrationDSNEnvironment:            "postgres://localhost/acr?sslmode=verify-full",
			poolerAdminDSNEnvironment:          "postgres://pooler-admin",
			migrationConnectionKindEnvironment: "direct",
		}},
		{name: "pgbouncer without pooler admin DSN", values: map[string]string{
			migrationDSNEnvironment:            "postgres://localhost/acr?sslmode=verify-full",
			migrationConnectionKindEnvironment: "pgbouncer",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			lookup := environment(test.values)

			// When
			err := run(context.Background(), []string{"status"}, lookup, &bytes.Buffer{})

			// Then
			require.ErrorContains(t, err, "ACR_POSTGRES_CONNECTION_KIND")
		})
	}
}

func environment(values map[string]string) lookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func testMigrationEnvironment(dsn string) lookupEnv {
	return environment(map[string]string{
		migrationDSNEnvironment: dsn,
	})
}

func newTestPostgresDSN(t *testing.T, ctx context.Context) string {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"),
		tcpostgres.WithUsername("acr"),
		tcpostgres.WithPassword("acr"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return strings.TrimSpace(dsn)
}
