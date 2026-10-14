package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// The write fence on a database built the way Kubernetes builds it: migrate,
// then grant-runtime-acl, with the writes made by a restricted role. The fence
// reads the marker with its owner's privileges, so it holds for a writer role
// that has no grant on the marker table at all: grant-runtime-acl gives the
// runtime role SELECT and DELETE there for the projector, and the fence must
// not depend on that grant.
func TestWriteFenceWorksForARuntimeRoleWithNoGrantOnTheMarkerTable(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	var output bytes.Buffer
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &output))

	ownerConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	owner := stdlib.OpenDB(*ownerConfig)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	for _, statement := range []string{
		`CREATE ROLE acr_fence_runtime LOGIN PASSWORD 'runtime-test-password'`,
		`GRANT CONNECT ON DATABASE acr TO acr_fence_runtime`,
		`GRANT USAGE ON SCHEMA acr TO acr_fence_runtime`,
		// Stands for the table grants a deployment already holds; the fence adds none.
		`GRANT SELECT, INSERT ON TABLE acr.context_fabric_projection_rebuild_markers TO acr_fence_runtime`,
	} {
		_, err := owner.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   "postgres://acr_fence_runtime:unused@runtime-dsn-is-never-dialed.invalid:5432/acr",
	}), &output))

	var grantedForTheProjector bool
	require.NoError(t, owner.QueryRowContext(ctx, `SELECT has_table_privilege('acr_fence_runtime', 'acr.context_fabric_org_purge_markers', 'SELECT')`).Scan(&grantedForTheProjector))
	require.True(t, grantedForTheProjector, "grant-runtime-acl gives the runtime role SELECT on the marker for the projector's check")
	_, err = owner.ExecContext(ctx, `REVOKE ALL ON TABLE acr.context_fabric_org_purge_markers FROM acr_fence_runtime`)
	require.NoError(t, err)

	runtimeConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	runtimeConfig.User, runtimeConfig.Password = "acr_fence_runtime", "runtime-test-password"
	runtime := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	state := func(err error) string {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.Code
		}
		return ""
	}
	const (
		purged        = "aaaaaaaa-0000-4000-8000-000000000001"
		live          = "bbbbbbbb-0000-4000-8000-000000000002"
		insertRebuild = `INSERT INTO acr.context_fabric_projection_rebuild_markers (org_id, started_at) VALUES ($1, now())`
		denied        = "42501"
	)
	_, err = owner.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, now())`, purged)
	require.NoError(t, err)

	var mayReadMarkers, mayExecute, ownerIsMigrationRole bool
	require.NoError(t, owner.QueryRowContext(ctx, `
SELECT has_table_privilege('acr_fence_runtime', 'acr.context_fabric_org_purge_markers', 'SELECT'),
       has_function_privilege('acr_fence_runtime', 'acr.context_fabric_refuse_purged_org()', 'EXECUTE'),
       (SELECT pg_get_userbyid(proowner) = current_user FROM pg_proc WHERE oid = 'acr.context_fabric_refuse_purged_org()'::regprocedure)`).Scan(&mayReadMarkers, &mayExecute, &ownerIsMigrationRole))
	require.False(t, mayReadMarkers, "the writer role under test holds nothing on the marker table")
	require.False(t, mayExecute, "the runtime role may not call the fence function")
	require.True(t, ownerIsMigrationRole, "the fence function is owned by the role that ran the migrations")

	_, err = runtime.ExecContext(ctx, insertRebuild, live)
	require.NoError(t, err, "a write for a live organization, by the runtime role")
	_, err = runtime.ExecContext(ctx, insertRebuild, purged)
	require.Equal(t, "ACR01", state(err), "a write for the purged organization, by the runtime role: %v", err)

	_, err = runtime.ExecContext(ctx, `SELECT acr.context_fabric_refuse_purged_org()`)
	require.Equal(t, denied, state(err), "the runtime role cannot call the fence function to learn whether an organization is marked: %v", err)
	_, err = runtime.ExecContext(ctx, `SELECT 1 FROM acr.context_fabric_org_purge_markers`)
	require.Equal(t, denied, state(err), "the runtime role cannot read the marker table: %v", err)

	// With the function run under the writer's privileges, the missing grant
	// fails every fenced write, for every organization.
	_, err = owner.ExecContext(ctx, `ALTER FUNCTION acr.context_fabric_refuse_purged_org() SECURITY INVOKER`)
	require.NoError(t, err)
	_, err = runtime.ExecContext(ctx, insertRebuild, "cccccccc-0000-4000-8000-000000000003")
	require.Equal(t, denied, state(err), "as security invoker the fence needs the marker grant: %v", err)
}

// The purge runs as its own role and the writer as the runtime role. Advisory
// locks belong to the database, not to a role, so a runtime-role write that
// starts while a purge-role transaction holds the fence waits for it, then
// sees the marker that transaction wrote.
func TestWriteFenceExcludesARuntimeRoleWriterFromAPurgeRunAsAnotherRole(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	var output bytes.Buffer
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &output))
	ownerConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	owner := stdlib.OpenDB(*ownerConfig)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	for _, statement := range []string{
		`CREATE ROLE acr_fence_writer LOGIN PASSWORD 'writer-test-password'`,
		`CREATE ROLE acr_fence_purger LOGIN PASSWORD 'purger-test-password'`,
		`GRANT CONNECT ON DATABASE acr TO acr_fence_writer, acr_fence_purger`,
		`GRANT USAGE ON SCHEMA acr TO acr_fence_writer, acr_fence_purger`,
		`GRANT SELECT, INSERT ON TABLE acr.context_fabric_projection_rebuild_markers TO acr_fence_writer`,
		`GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_org_purge_markers TO acr_fence_purger`,
	} {
		_, err := owner.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	as := func(role, password string) *sql.DB {
		config, err := pgx.ParseConfig(migrationDSN)
		require.NoError(t, err)
		config.User, config.Password = role, password
		db := stdlib.OpenDB(*config)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		return db
	}
	writer, purger := as("acr_fence_writer", "writer-test-password"), as("acr_fence_purger", "purger-test-password")
	const org = "dddddddd-0000-4000-8000-000000000004"

	purge, err := purger.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = purge.Rollback() })
	_, err = purge.ExecContext(ctx, `SELECT pg_advisory_xact_lock(20261014, hashtext($1))`, org)
	require.NoError(t, err)

	written := make(chan error, 1)
	go func() {
		_, err := writer.ExecContext(ctx, `INSERT INTO acr.context_fabric_projection_rebuild_markers (org_id, started_at) VALUES ($1, now())`, "{"+org+"}")
		written <- err
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting int
		require.NoError(t, owner.QueryRowContext(ctx, `
SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
WHERE l.locktype = 'advisory' AND l.classid::bigint = 20261014 AND l.mode = 'ShareLock' AND NOT l.granted AND a.usename = 'acr_fence_writer'`).Scan(&waiting))
		if waiting == 1 {
			break
		}
		require.True(t, time.Now().Before(deadline), "the runtime-role writer never waited on the purge role's fence")
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-written:
		t.Fatalf("the writer finished while the purge role held the fence: %v", err)
	default:
	}

	_, err = purge.ExecContext(ctx, `INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at) VALUES ($1, clock_timestamp())`, org)
	require.NoError(t, err)
	require.NoError(t, purge.Commit())
	err = <-written
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "%v", err)
	require.Equal(t, "ACR01", pgErr.Code)
	var rows int
	require.NoError(t, owner.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_projection_rebuild_markers`).Scan(&rows))
	require.Zero(t, rows)
}
