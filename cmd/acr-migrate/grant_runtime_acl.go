package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
)

// grantRuntimeACL is CHAOS-6277's durable fix for a gap specific to
// Kubernetes: migration 0042 (acr.oauth_device_grants) documents, in its
// own header comment, that this repository never grants runtime-role table
// privileges from a migration -- compose bootstraps and re-asserts those
// grants out of band with deploy/compose/acr-db-init.sh's runtime-acl mode,
// using the Postgres ADMIN credential. Kubernetes deliberately never holds
// that credential (deploy/README.md's "documented gaps" section, ADR-0004's
// existing-Secret-only contract), so acr.oauth_device_grants shipped with a
// grant on compose only; trial and prod both needed it hand-applied live
// (CHAOS-6233's incident record).
//
// This closes the gap without an admin DSN: the migration role db already
// connects as (the schema/table owner, since acr-db-init.sh's "roles" stage
// runs ALTER TABLE ... OWNER TO the migration role) can GRANT privileges on
// any table it owns to any other role -- no elevated privilege required.
// runtimeDSN is parsed ONLY for its embedded username, the runtime role's
// own name; grantRuntimeACL never dials it. The Helm chart already requires
// this DSN's Secret reference for acr-api/acr-mcp (acr.validateCredentials),
// so this reuses that same reference rather than adding a new one.
//
// The grant list here deliberately covers ONLY acr.oauth_device_grants --
// the one table CHAOS-6277 found missing its Kubernetes grant.
// oauth_clients and oauth_authorization_requests (migration 0040) were both
// independently confirmed already correctly granted on trial and prod when
// this ticket was filed; every other migration between 0038 and 0042 either
// predates this range or only alters existing tables (0041), so no other
// table needs the same fix here (see this PR's RISK-NOTES).
//
// A bare, repeatable GRANT (not REVOKE-then-GRANT) makes this idempotent
// AND safe to run against a role that is already serving live traffic:
// codex round cf-6277-r3's P1, executed repro, found that a separate
// REVOKE-then-GRANT (two autocommitted statements) opens a real window,
// during a rolling Helm upgrade, where already-running application pods
// hold the same already-granted role and see permission-denied reads
// between the REVOKE committing and the GRANT committing -- 201 of 4953
// concurrent reads failed in the reviewer's 50-reapplication reproduction.
// Re-granting an already-held privilege is a no-op in Postgres (no error,
// no interruption), so a bare GRANT is EQUALLY idempotent against
// trial/prod (where the grant already exists from the hand-applied fix)
// without ever narrowing the role's privileges first. This function has
// no legitimate reason to ever REDUCE this role's privileges on this
// table -- it exists only to ensure SELECT, INSERT are present -- so
// there is no privilege-reduction case to make atomic either; removing it
// is the fix, not a workaround.
func grantRuntimeACL(ctx context.Context, db *sql.DB, runtimeDSN string, output io.Writer) error {
	parsed, err := pgx.ParseConfig(runtimeDSN)
	if err != nil {
		// codex round cf-6277-r3's P1, executed repro: pgx's own parse
		// error echoes the offending DSN verbatim, INCLUDING the
		// password, into this error -- which a Helm hook Job would then
		// print to Kubernetes' own Job logs on any malformed runtime DSN
		// (a wrong connect_timeout value was enough to trigger it).
		// internal/runtime/postgres.Open's own DSN parse-error handling
		// already established the pattern this follows: a fixed, generic
		// message, NEVER err.Error() or a %w wrap of the raw pgx error.
		return errors.New("invalid runtime DSN")
	}
	roles := []string{parsed.User}
	// A runtime DSN may carry `role=<name>` (a runtime parameter, applied as
	// SET ROLE on connect): queries then execute as that role, not as the
	// login user, so privileges must be granted to it as well.
	if effective := parsed.RuntimeParams["role"]; effective != "" && effective != parsed.User {
		roles = append(roles, effective)
	}
	for _, role := range roles {
		if role == "" {
			return fmt.Errorf("runtime DSN has no username")
		}
		// Only a NUL byte is unsafe in a double-quoted identifier; every
		// embedded `"` is doubled below.
		if strings.ContainsRune(role, 0) {
			return fmt.Errorf("runtime role name %q contains a NUL byte", role)
		}
		quotedRole := `"` + strings.ReplaceAll(role, `"`, `""`) + `"`
		// A bare repeatable GRANT (never REVOKE first) is idempotent and
		// never interrupts a role already serving traffic.
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			`GRANT SELECT, INSERT ON TABLE acr.oauth_device_grants TO %s`,
			quotedRole,
		)); err != nil {
			return fmt.Errorf("grant acr.oauth_device_grants privileges to %s: %w", role, err)
		}
	}
	role := strings.Join(roles, ", ")
	_, err = fmt.Fprintf(output, "granted SELECT, INSERT on acr.oauth_device_grants to %s\n", role)
	return err
}
