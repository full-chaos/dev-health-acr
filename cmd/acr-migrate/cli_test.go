package main

import (
	"bytes"
	"context"
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
	lookup := environment(map[string]string{migrationDSNEnvironment: dsn})

	// When
	err := run(ctx, []string{"up"}, lookup, &stdout)

	// Then
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "applied")
}

func TestRun_reportsStatusFromEnvironmentConfiguredDSN(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	lookup := environment(map[string]string{migrationDSNEnvironment: dsn})
	require.NoError(t, run(ctx, []string{"up"}, lookup, &bytes.Buffer{}))
	var stdout bytes.Buffer

	// When
	err := run(ctx, []string{"status"}, lookup, &stdout)

	// Then
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "0001")
	require.Contains(t, stdout.String(), "0002")
}

func TestRun_reportsAppliedCountAndNoOpDistinctly(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	lookup := environment(map[string]string{migrationDSNEnvironment: dsn})
	var first, second bytes.Buffer

	// When
	firstErr := run(ctx, []string{"up"}, lookup, &first)
	secondErr := run(ctx, []string{"up"}, lookup, &second)

	// Then
	require.NoError(t, firstErr)
	require.Equal(t, "applied 2 migrations\n", first.String())
	require.NoError(t, secondErr)
	require.Equal(t, "no migrations applied\n", second.String())
}

func TestRun_reportsExplicitEmptyStatus_whenDatabaseIsFresh(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	lookup := environment(map[string]string{migrationDSNEnvironment: dsn})
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

func environment(values map[string]string) lookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
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
