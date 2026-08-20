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
	// added 0014, CHAOS-3862 added 0015, CHAOS-3859 added 0016, CHAOS-3889
	// added 0017, CHAOS-3884 added 0018 (renumbered from 0017 during
	// the rebase onto origin/main -- see runner_integration_test.go's own
	// comment for why), CHAOS-3898 S2a added 0019 and 0020, CHAOS-3898
	// S2 (§2.3 graph_epoch reuse-key dimension) added 0021, and CHAOS-3900
	// W1 (window_inference_version reuse-key dimension) added 0022,
	// CHAOS-3927 P4 (structure_supersession_claims, the offer-supersession
	// atomicity table) added 0023, CHAOS-3927 P4's own capture-schema
	// evolution (structure_selections, the StructureSelectionEvent sink
	// table) added 0024, and CHAOS-3927 P4's codex-review backfill
	// (structure_supersession_backfill, claiming pre-0023 confirmed-
	// structure rows) added 0025, and CHAOS-3860 P6's precondition fix
	// (StructureSelectionEvent's ConsensusEvidence field, the design
	// brief's own §4 P6 dependency that P4 shipped without) added 0026,
	// CHAOS-3860 P6's codex-review panel-size CHECK constraint added 0027,
	// and CHAOS-3977 P5 (structure_priors, the versioned prior store +
	// active-version pointer + pointer history + per-entry revocations --
	// renumbered from 0026 to 0028 during the rebase onto origin/main:
	// both P6 and P5 independently claimed 0026 as separate features
	// landed in parallel, P6 merged first) added 0028, and CHAOS-3977 P5's
	// own one-time reuse-column cleanup (structure_bearing_reuse_cleanup,
	// clearing pre-existing structure-bearing rows' reuse columns,
	// renumbered from 0027 to 0029 for the same reason) added 0029, so the
	// embedded set is now 29 files. A future migration must bump this
	// literal too -- see expectedMigrationVersions in migrations/postgres/
	// runner_integration_test.go for the same convention, held in one
	// place there.
	require.Equal(t, "applied 29 migrations\n", first.String())
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
