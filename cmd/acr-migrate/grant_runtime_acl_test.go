package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// TestGrantRuntimeACL_deviceGrantsRoundTrip is CHAOS-6277's red/green proof:
// on Kubernetes, migration 0042 created acr.oauth_device_grants with zero
// grants for the runtime role (the compose-only fix this ticket closes).
// Before the fix, a runtime-role connection gets a real Postgres
// permission-denied error on INSERT (RED, reproducing the incident this
// ticket was filed from -- trial/prod both showed empty `\dp
// acr.oauth_device_grants` access privileges). After running
// grant-runtime-acl, the identical runtime-role connection can INSERT and
// SELECT a real device grant row (GREEN).
func TestGrantRuntimeACL_deviceGrantsRoundTrip(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)

	// Given a fully migrated schema (owned by the container's superuser,
	// standing in for the migration role acr-db-init.sh's "roles" stage
	// ALTERs table ownership to)...
	var migrateOutput bytes.Buffer
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &migrateOutput))

	// ...and a restricted runtime role with NO grants at all, exactly the
	// state CHAOS-6277 found on trial/prod (acr-db-init.sh's "roles" stage
	// creates the login role; runtime-acl is the missing step on
	// Kubernetes).
	adminDB, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	rawDB := stdlib.OpenDB(*adminDB)
	t.Cleanup(func() { require.NoError(t, rawDB.Close()) })
	_, err = rawDB.ExecContext(ctx, `CREATE ROLE acr_mcp_runtime_test LOGIN PASSWORD 'runtime-test-password'`)
	require.NoError(t, err)
	_, err = rawDB.ExecContext(ctx, `GRANT CONNECT ON DATABASE acr TO acr_mcp_runtime_test`)
	require.NoError(t, err)
	_, err = rawDB.ExecContext(ctx, `GRANT USAGE ON SCHEMA acr TO acr_mcp_runtime_test`)
	require.NoError(t, err)

	runtimeConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	runtimeConfig.User = "acr_mcp_runtime_test"
	runtimeConfig.Password = "runtime-test-password"
	runtimeDB := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { require.NoError(t, runtimeDB.Close()) })

	// acr.oauth_device_grants.device_code_hash FK-references
	// acr.device_authorizations, so a real row needs a parent -- inserted
	// as the admin/migration connection, matching how the real
	// /device_authorization handler writes both rows in the same
	// transaction. This does not affect the RED assertion below: Postgres
	// checks table-level privilege before any constraint, so a
	// permission-denied INSERT never reaches the FK check either.
	const deviceCodeHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const userCodeHashA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err = rawDB.ExecContext(ctx, `INSERT INTO acr.device_authorizations
		(device_code_hash, user_code_hash, state, created_at, expires_at, poll_interval_seconds, issuance_provenance)
		VALUES ($1, $2, 'pending', now(), now() + interval '10 minutes', 5, 'device_authorization')`,
		deviceCodeHashA, userCodeHashA)
	require.NoError(t, err)

	// RED: the runtime role has no privileges on acr.oauth_device_grants at
	// all -- INSERT fails permission-denied, reproducing the incident.
	insertDeviceGrant := `INSERT INTO acr.oauth_device_grants
		(device_code_hash, client_id, client_kind, resource, scope, created_at, expires_at)
		VALUES ($1, 'acrc_00000000000000000000000000000000', 'dynamic', 'https://example.test/resource', '', now(), now() + interval '10 minutes')`
	_, err = runtimeDB.ExecContext(ctx, insertDeviceGrant, deviceCodeHashA)
	require.Error(t, err, "the runtime role must NOT be able to write acr.oauth_device_grants before grant-runtime-acl runs")
	require.Contains(t, err.Error(), "permission denied", "the pre-fix failure must be a real Postgres permission-denied error, not some other class")

	// runtimeDSN only needs to parse -- grant-runtime-acl never dials it,
	// it just reads the embedded username (see grantRuntimeACL's doc
	// comment). An unreachable host proves this.
	runtimeDSN := "postgres://acr_mcp_runtime_test:unused@runtime-dsn-is-never-dialed.invalid:5432/acr"

	// When grant-runtime-acl runs, using only the migration DSN (already
	// required) plus that unreachable-but-parseable runtime DSN.
	var grantOutput bytes.Buffer
	grantErr := run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   runtimeDSN,
	}), &grantOutput)

	// Then it succeeds and reports the role it granted.
	require.NoError(t, grantErr)
	require.Contains(t, grantOutput.String(), "acr_mcp_runtime_test")

	// GREEN: the SAME runtime-role connection can now INSERT and SELECT a
	// real device grant row.
	_, err = runtimeDB.ExecContext(ctx, insertDeviceGrant, deviceCodeHashA)
	require.NoError(t, err, "the runtime role must be able to INSERT into acr.oauth_device_grants after grant-runtime-acl runs")

	var resource string
	err = runtimeDB.QueryRowContext(ctx, `SELECT resource FROM acr.oauth_device_grants WHERE device_code_hash = $1`, deviceCodeHashA).Scan(&resource)
	require.NoError(t, err, "the runtime role must be able to SELECT its own just-written row")
	require.Equal(t, "https://example.test/resource", resource)

	// And the runtime role still has none of the OTHER privileges this
	// table's owner never granted it (UPDATE, DELETE) -- grant-runtime-acl
	// must not have over-granted.
	_, err = runtimeDB.ExecContext(ctx, `UPDATE acr.oauth_device_grants SET scope = 'widened' WHERE device_code_hash = $1`, deviceCodeHashA)
	require.Error(t, err, "the runtime role must still be denied UPDATE on acr.oauth_device_grants -- grant-runtime-acl must not over-grant beyond SELECT, INSERT")
	require.Contains(t, err.Error(), "permission denied")

	// Idempotency: re-running grant-runtime-acl against an environment
	// where the grant already exists (trial/prod today, after the
	// hand-applied fix, or simply a second Helm upgrade) must not error --
	// this is the "idempotent against prod/trial where it already exists"
	// requirement.
	var secondGrantOutput bytes.Buffer
	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   runtimeDSN,
	}), &secondGrantOutput), "grant-runtime-acl must be idempotent when the grant already exists")

	// Still exactly SELECT, INSERT afterward -- re-running never widens the
	// ACL.
	_, err = runtimeDB.ExecContext(ctx, `UPDATE acr.oauth_device_grants SET scope = 'widened' WHERE device_code_hash = $1`, deviceCodeHashA)
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission denied")
}

// TestGrantRuntimeACL_acceptsRoleNamesWithHyphens is the codex round
// cf-6277-r1 P1 regression test: an earlier version of grantRuntimeACL
// additionally rejected any role name outside a narrow lowercase/underscore
// charset, so a genuinely valid PostgreSQL role like "acr-runtime"
// (hyphenated -- this repository's own convention never uses one, but
// nothing about PostgreSQL or the Helm chart's credential contract forbids
// it) was refused even though it is perfectly safe once double-quoted. A
// real role with a hyphen, a space, and mixed case all round-trip through
// grantRuntimeACL and actually receive the grant.
func TestGrantRuntimeACL_acceptsRoleNamesWithHyphens(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	for _, roleName := range []string{"acr-runtime", "acr runtime", "Acr_Runtime"} {
		t.Run(roleName, func(t *testing.T) {
			adminDB, err := pgx.ParseConfig(migrationDSN)
			require.NoError(t, err)
			rawDB := stdlib.OpenDB(*adminDB)
			t.Cleanup(func() { require.NoError(t, rawDB.Close()) })
			quoted := `"` + strings.ReplaceAll(roleName, `"`, `""`) + `"`
			_, err = rawDB.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'x'`, quoted))
			require.NoError(t, err)
			_, err = rawDB.ExecContext(ctx, fmt.Sprintf(`GRANT CONNECT ON DATABASE acr TO %s`, quoted))
			require.NoError(t, err)
			_, err = rawDB.ExecContext(ctx, fmt.Sprintf(`GRANT USAGE ON SCHEMA acr TO %s`, quoted))
			require.NoError(t, err)

			// url.QueryEscape encodes a space as "+", which is
			// form-encoding, not userinfo percent-encoding -- pgx (like any
			// RFC 3986 URL parser) does NOT decode "+" back to a space in
			// the userinfo component, so that mis-escaping alone made the
			// "acr runtime" case fail here on a TEST bug, not a production
			// one. url.URL{User: url.UserPassword(...)}.String() escapes
			// userinfo correctly for every case, including the space.
			dsnURL := url.URL{Scheme: "postgres", User: url.UserPassword(roleName, "x"), Host: "runtime-dsn-is-never-dialed.invalid:5432", Path: "/acr"}
			runtimeDSN := dsnURL.String()
			var output bytes.Buffer
			err = run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
				migrationDSNEnvironment: migrationDSN,
				runtimeDSNEnvironment:   runtimeDSN,
			}), &output)
			require.NoErrorf(t, err, "grant-runtime-acl rejected a valid PostgreSQL role name %q", roleName)
			require.Contains(t, output.String(), roleName)

			runtimeConfig, err := pgx.ParseConfig(migrationDSN)
			require.NoError(t, err)
			runtimeConfig.User = roleName
			runtimeConfig.Password = "x"
			runtimeDB := stdlib.OpenDB(*runtimeConfig)
			t.Cleanup(func() { require.NoError(t, runtimeDB.Close()) })
			_, err = runtimeDB.ExecContext(ctx, `SELECT 1 FROM acr.oauth_device_grants LIMIT 0`)
			require.NoError(t, err, "the hyphenated/spaced/mixed-case role must have actually received SELECT")
		})
	}
}

// TestGrantRuntimeACL_rejectsMalformedRuntimeDSN proves grantRuntimeACL
// fails closed rather than silently doing nothing or interpolating a bad
// value when the runtime DSN cannot be parsed, or carries no username.
func TestGrantRuntimeACL_rejectsMalformedRuntimeDSN(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	tests := []struct {
		name       string
		runtimeDSN string
	}{
		{name: "unparseable", runtimeDSN: "not a dsn at all"},
		{name: "missing username", runtimeDSN: "postgres://runtime-dsn-is-never-dialed.invalid:5432/acr"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
				migrationDSNEnvironment: migrationDSN,
				runtimeDSNEnvironment:   test.runtimeDSN,
			}), &bytes.Buffer{})
			require.Error(t, err)
		})
	}
}

// TestGrantRuntimeACL_requiresRuntimeDSNEnvironment proves the new verb
// fails closed (rather than silently skipping the grant) when
// ACR_POSTGRES_DSN is not configured -- mirroring
// TestRun_requiresMigrationDSNEnvironment's existing coverage for the
// migration DSN.
func TestGrantRuntimeACL_requiresRuntimeDSNEnvironment(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)

	err := run(ctx, []string{"grant-runtime-acl"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{})

	require.Error(t, err)
	require.Contains(t, err.Error(), runtimeDSNEnvironment)
}

func TestGrantRuntimeACL_rejectsUnexpectedArguments(t *testing.T) {
	err := run(context.Background(), []string{"grant-runtime-acl", "--extra"}, environment(nil), &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid arguments")
}
