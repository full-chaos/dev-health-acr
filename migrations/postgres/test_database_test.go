package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
)

func newTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	chfixture.RequireContainers(t)
	// CHAOS-4855: pinned by digest (was a bare tag) so
	// TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves this to the ghcr.io
	// mirror by digest, same as every other postgres:18-alpine pull in
	// this module.
	container, err := startterm.PostgresRun(ctx, "postgres:18-alpine@sha256:10a5c69a83035ab5239f5620d708520e8b28f5aa1f2b20143e6024532edadfb5",
		tcpostgres.WithDatabase("acr"),
		tcpostgres.WithUsername("acr"),
		tcpostgres.WithPassword("acr"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db := stdlib.OpenDB(*mustParseConfig(t, dsn))
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func mustParseConfig(t *testing.T, dsn string) *pgx.ConnConfig {
	t.Helper()
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(fmt.Errorf("parse test PostgreSQL DSN: %w", err))
	}
	return config
}
