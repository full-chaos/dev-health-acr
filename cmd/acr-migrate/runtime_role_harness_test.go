package main

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/dbacl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// runtimeRoleDatabase is the one harness for tests that run as the restricted
// runtime role: a fresh Postgres, migrated the way Kubernetes migrates it, a
// login role with no table privileges, then grant-runtime-acl. The database has
// no default privileges (asserted), so a table the declaration misses shows up
// as permission denied.
type runtimeRoleDatabase struct {
	Owner   *sql.DB // the migration role: owns every table and may grant
	Runtime *sql.DB // connected as the runtime role
	Role    string
	DSN     string // the owner DSN
}

type runtimeRoleOptions struct {
	Role     string
	Features dbacl.Features
	// BeforeGrant runs as the owner after the role exists and before
	// grant-runtime-acl, to stand in for grants an operator applied out of band.
	BeforeGrant func(t *testing.T, owner *sql.DB)
	// SkipGrant leaves the role without grant-runtime-acl.
	SkipGrant bool
}

func newRuntimeRoleDatabase(t *testing.T, ctx context.Context, options runtimeRoleOptions) *runtimeRoleDatabase {
	t.Helper()
	if options.Role == "" {
		options.Role = "acr_runtime_harness"
	}
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	ownerConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	owner := stdlib.OpenDB(*ownerConfig)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	for _, statement := range []string{
		`CREATE ROLE ` + dbacl.QuoteIdent(options.Role) + ` LOGIN PASSWORD 'runtime-test-password'`,
		`GRANT CONNECT ON DATABASE acr TO ` + dbacl.QuoteIdent(options.Role),
		`GRANT USAGE ON SCHEMA acr TO ` + dbacl.QuoteIdent(options.Role),
	} {
		_, err = owner.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	var defaultPrivileges int
	require.NoError(t, owner.QueryRowContext(ctx, `SELECT count(*) FROM pg_default_acl`).Scan(&defaultPrivileges))
	require.Zero(t, defaultPrivileges, "no default privileges: a missing grant must show up as permission denied")

	if options.BeforeGrant != nil {
		options.BeforeGrant(t, owner)
	}
	if !options.SkipGrant {
		env := map[string]string{
			migrationDSNEnvironment: migrationDSN,
			// Parsed for its user name only; never dialed.
			runtimeDSNEnvironment: "postgres://" + options.Role + ":unused@runtime-dsn-is-never-dialed.invalid:5432/acr",
		}
		if options.Features[dbacl.EpisodeWriteback] {
			env["ACR_ENABLE_EPISODE_WRITEBACK"] = "true"
		}
		if options.Features[dbacl.WorkloadTokenExchange] {
			env["ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE"] = "https://acr.test"
			env["ACR_WORKLOAD_TRUST_DOMAIN"] = "cluster.test"
		}
		require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(env), &bytes.Buffer{}))
	}

	runtimeConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	runtimeConfig.User, runtimeConfig.Password = options.Role, "runtime-test-password"
	runtime := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	return &runtimeRoleDatabase{Owner: owner, Runtime: runtime, Role: options.Role, DSN: migrationDSN}
}

// NewRole creates one more login role on the same database, applies the given
// statements as the owner, and returns a connection as that role. It stands for
// a role other than the runtime role, such as a purge role.
func (d *runtimeRoleDatabase) NewRole(t *testing.T, ctx context.Context, role string, statements ...string) *sql.DB {
	t.Helper()
	for _, statement := range append([]string{
		`CREATE ROLE ` + dbacl.QuoteIdent(role) + ` LOGIN PASSWORD 'role-test-password'`,
		`GRANT CONNECT ON DATABASE acr TO ` + dbacl.QuoteIdent(role),
		`GRANT USAGE ON SCHEMA acr TO ` + dbacl.QuoteIdent(role),
	}, statements...) {
		_, err := d.Owner.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	config, err := pgx.ParseConfig(d.DSN)
	require.NoError(t, err)
	config.User, config.Password = role, "role-test-password"
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
