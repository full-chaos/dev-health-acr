package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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

	// CHAOS-6191/CHAOS-7229, RED: before grant-runtime-acl the runtime role can DELETE from
	// none of the tables the OAuth purge loop sweeps (checked as privileges, not by
	// deleting rows, so no fixture rows are needed).
	oauthPurgeTables := []string{"acr.oauth_clients", "acr.oauth_authorization_requests", "acr.device_authorizations"}
	hasPrivilege := func(table, privilege string) bool {
		var granted bool
		require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT has_table_privilege('acr_mcp_runtime_test', $1, $2)`, table, privilege).Scan(&granted))
		return granted
	}
	for _, table := range oauthPurgeTables {
		require.False(t, hasPrivilege(table, "DELETE"), "%s: the runtime role must not hold DELETE before grant-runtime-acl runs", table)
	}

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
	require.Contains(t, grantOutput.String(), "granted DELETE on acr.oauth_clients, acr.oauth_authorization_requests to acr_mcp_runtime_test")
	require.Contains(t, grantOutput.String(), "granted DELETE on acr.device_authorizations to acr_mcp_runtime_test")

	// CHAOS-6191, GREEN: DELETE, and nothing else, on the two purge tables.
	for _, table := range oauthPurgeTables {
		require.True(t, hasPrivilege(table, "DELETE"), "%s: grant-runtime-acl must give the runtime role DELETE for the OAuth purge loop", table)
		for _, privilege := range []string{"INSERT", "UPDATE", "TRUNCATE"} {
			require.False(t, hasPrivilege(table, privilege), "%s: grant-runtime-acl must grant DELETE only, not %s", table, privilege)
		}
	}

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
	for _, table := range oauthPurgeTables {
		require.True(t, hasPrivilege(table, "DELETE"), "%s: DELETE must survive a re-run", table)
	}

	// Still exactly SELECT, INSERT afterward -- re-running never widens the
	// ACL.
	_, err = runtimeDB.ExecContext(ctx, `UPDATE acr.oauth_device_grants SET scope = 'widened' WHERE device_code_hash = $1`, deviceCodeHashA)
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission denied")
}

// TestGrantRuntimeACL_reapplyingNeverInterruptsAConcurrentReader is codex
// round cf-6277-r3's P1 regression test, executed repro: an earlier
// version of grantRuntimeACL did REVOKE then GRANT as two separate,
// separately-committed statements -- during a rolling Helm upgrade,
// already-running application pods holding the SAME already-granted role
// could see a real, if narrow, window of permission-denied reads between
// the REVOKE committing and the GRANT committing. The round's own
// reproduction observed 201 of 4953 concurrent reads fail across 50
// reapplications. Fixed by removing the REVOKE entirely (a bare,
// idempotent GRANT is the correct fix, not making REVOKE+GRANT atomic --
// this function never has a legitimate reason to narrow the role's
// privileges). This test reapplies the grant repeatedly while a separate
// connection, using the SAME already-granted runtime role, reads
// continuously in a tight loop -- proving zero permission-denied reads
// occur at any point.
func TestGrantRuntimeACL_reapplyingNeverInterruptsAConcurrentReader(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	adminDB, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	rawDB := stdlib.OpenDB(*adminDB)
	t.Cleanup(func() { require.NoError(t, rawDB.Close()) })
	_, err = rawDB.ExecContext(ctx, `CREATE ROLE reapply_reader_role LOGIN PASSWORD 'x'`)
	require.NoError(t, err)
	_, err = rawDB.ExecContext(ctx, `GRANT CONNECT ON DATABASE acr TO reapply_reader_role`)
	require.NoError(t, err)
	_, err = rawDB.ExecContext(ctx, `GRANT USAGE ON SCHEMA acr TO reapply_reader_role`)
	require.NoError(t, err)

	runtimeDSN := "postgres://reapply_reader_role:x@runtime-dsn-is-never-dialed.invalid:5432/acr"
	// Grant once up front, matching a real already-granted trial/prod
	// environment -- the interruption this test guards against is on the
	// REAPPLY path, not the first grant.
	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   runtimeDSN,
	}), &bytes.Buffer{}))

	runtimeConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	runtimeConfig.User = "reapply_reader_role"
	runtimeConfig.Password = "x"
	readerDB := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { require.NoError(t, readerDB.Close()) })

	stop := make(chan struct{})
	var permissionDenied atomic.Int64
	var totalReads atomic.Int64
	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, readErr := readerDB.ExecContext(ctx, `SELECT 1 FROM acr.oauth_device_grants LIMIT 0`)
			totalReads.Add(1)
			if readErr != nil {
				if strings.Contains(readErr.Error(), "permission denied") {
					permissionDenied.Add(1)
				} else {
					// A non-permission error (e.g. connection reset)
					// would also be a real defect, but this test's own
					// claim is narrowly about permission-denied reads.
					require.NoError(t, readErr)
				}
			}
		}
	}()

	const reapplications = 25
	for i := 0; i < reapplications; i++ {
		var output bytes.Buffer
		require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
			migrationDSNEnvironment: migrationDSN,
			runtimeDSNEnvironment:   runtimeDSN,
		}), &output))
	}
	close(stop)
	readerWG.Wait()

	require.Positive(t, totalReads.Load(), "the concurrent reader goroutine must have actually run reads for this test to mean anything")
	require.Zero(t, permissionDenied.Load(), "reapplying grant-runtime-acl must never interrupt an already-granted role's access (observed %d/%d permission-denied reads across %d reapplications)", permissionDenied.Load(), totalReads.Load(), reapplications)
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
// fails closed rather than silently doing nothing when the runtime DSN
// cannot be parsed at all.
//
// codex round cf-6277-r3's P3, executed repro: a prior version of this
// test also carried a "missing username" case asserting the plain
// `role == ""` check in grant_runtime_acl.go fires -- but pgx.ParseConfig
// implements the same fallback chain libpq/psql does: PGUSER, then the
// OS user (os/user.Current()), so a real DSN with no explicit userinfo
// NEVER actually parses to an empty User in practice (the round proved
// this by setting PGUSER, which this test's own attempt to force it via
// `t.Setenv("PGUSER", "")` also could not prevent -- pgx treats an EMPTY
// PGUSER the same as an unset one and still falls through to the OS
// user). The `role == ""` branch is therefore unreachable from any real
// runtime DSN and is kept purely as defense in depth, not because a test
// can drive it -- removing the false "proves rejection" claim is the fix,
// not adding more environment manipulation that cannot work either.
// TestGrantRuntimeACL_missingUsernameFallsBackToPGUSER (below) tests the
// REAL behavior this DSN shape actually has.
func TestGrantRuntimeACL_rejectsMalformedRuntimeDSN(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	err := run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   "not a dsn at all",
	}), &bytes.Buffer{})
	require.EqualError(t, err, "invalid runtime DSN")
}

// TestGrantRuntimeACL_missingUsernameFallsBackToPGUSER is codex round
// cf-6277-r3's P3 regression test, testing the REAL, demonstrated
// behavior of a runtime DSN with no explicit userinfo: pgx.ParseConfig
// resolves the username from the PGUSER environment variable (libpq's own
// fallback chain), so grant-runtime-acl operates on THAT role, not an
// empty one. Documenting and pinning this (rather than leaving it as an
// implicit, untested side effect) is the point -- an operator relying on
// "no username in the DSN" to mean "rejected" would be wrong, and this
// test is what would catch a future pgx upgrade changing that fallback.
func TestGrantRuntimeACL_missingUsernameFallsBackToPGUSER(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	adminDB, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	rawDB := stdlib.OpenDB(*adminDB)
	t.Cleanup(func() { require.NoError(t, rawDB.Close()) })
	_, err = rawDB.ExecContext(ctx, `CREATE ROLE pguser_fallback_role LOGIN PASSWORD 'x'`)
	require.NoError(t, err)
	_, err = rawDB.ExecContext(ctx, `GRANT CONNECT ON DATABASE acr TO pguser_fallback_role`)
	require.NoError(t, err)
	_, err = rawDB.ExecContext(ctx, `GRANT USAGE ON SCHEMA acr TO pguser_fallback_role`)
	require.NoError(t, err)

	t.Setenv("PGUSER", "pguser_fallback_role")
	var output bytes.Buffer
	err = run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   "postgres://runtime-dsn-is-never-dialed.invalid:5432/acr",
	}), &output)
	require.NoError(t, err)
	require.Contains(t, output.String(), "pguser_fallback_role", "grant-runtime-acl must operate on the PGUSER-resolved role, not silently do nothing")
}

// TestGrantRuntimeACL_parseErrorNeverLeaksTheRuntimeDSN is codex round
// cf-6277-r3's second P1 regression test: a malformed runtime DSN
// (unparseable by pgx for a reason unrelated to the username, e.g. a bad
// connect_timeout) must never echo the DSN -- including any embedded
// password -- into the returned error, which a Helm hook Job would print
// to Kubernetes' own Job logs.
func TestGrantRuntimeACL_parseErrorNeverLeaksTheRuntimeDSN(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	const secretMarker = "review-secret-marker-9f3a1c"
	runtimeDSN := "postgres://acr_mcp_runtime:" + secretMarker + "@runtime-dsn-is-never-dialed.invalid:5432/acr?connect_timeout=not-a-number"

	err := run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   runtimeDSN,
	}), &bytes.Buffer{})

	require.Error(t, err)
	require.NotContains(t, err.Error(), secretMarker, "the parse error must never echo the runtime DSN's password")
	require.NotContains(t, err.Error(), "connect_timeout", "the parse error must never echo the raw DSN text at all")
	require.Equal(t, "invalid runtime DSN", err.Error())
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

// TestGrantRuntimeACL_grantsTheEffectiveRoleOfARoleParameterDSN: a runtime
// DSN carrying `role=<name>` logs in as one role but executes queries as
// another (SET ROLE on connect). The grant must reach the effective role, or
// the hook reports success while runtime queries stay permission-denied.
func TestGrantRuntimeACL_grantsTheEffectiveRoleOfARoleParameterDSN(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	adminConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	adminDB := stdlib.OpenDB(*adminConfig)
	t.Cleanup(func() { require.NoError(t, adminDB.Close()) })
	for _, stmt := range []string{
		`CREATE ROLE runtime_effective NOLOGIN`,
		`CREATE ROLE runtime_login LOGIN PASSWORD 'x' IN ROLE runtime_effective`,
		`GRANT CONNECT ON DATABASE acr TO runtime_login`,
		`GRANT USAGE ON SCHEMA acr TO runtime_effective`,
	} {
		_, err = adminDB.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	runtimeDSN := url.URL{Scheme: "postgres", User: url.UserPassword("runtime_login", "x"), Host: "runtime-dsn-is-never-dialed.invalid:5432", Path: "/acr", RawQuery: "role=runtime_effective"}
	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   runtimeDSN.String(),
	}), &bytes.Buffer{}))

	runtimeConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	runtimeConfig.User = "runtime_login"
	runtimeConfig.Password = "x"
	runtimeConfig.RuntimeParams["role"] = "runtime_effective"
	runtimeDB := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { require.NoError(t, runtimeDB.Close()) })
	_, err = runtimeDB.ExecContext(ctx, `SELECT 1 FROM acr.oauth_device_grants LIMIT 0`)
	require.NoError(t, err, "the effective (role=) runtime role must have actually received SELECT")
}
