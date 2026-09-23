package main

import (
	"context"
	"database/sql"
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
// REVOKE-then-GRANT, not a bare GRANT, makes this idempotent: safe on a
// fresh install, and equally safe re-run against trial/prod, where the
// grant already exists from the hand-applied fix -- it converges to the
// identical ACL either way instead of erroring on a privilege that is
// already present.
func grantRuntimeACL(ctx context.Context, db *sql.DB, runtimeDSN string, output io.Writer) error {
	parsed, err := pgx.ParseConfig(runtimeDSN)
	if err != nil {
		return fmt.Errorf("parse runtime DSN: %w", err)
	}
	role := parsed.User
	if role == "" {
		return fmt.Errorf("runtime DSN has no username")
	}
	// Postgres quoted identifiers (`"..."`) admit almost any character --
	// including this repository's own convention (acr_runtime,
	// acr_mcp_runtime) but also hyphens, spaces, and mixed case, which a
	// real PostgreSQL role name is free to use (codex round cf-6277-r1's
	// P1, executed repro: a role literally named "acr-runtime" was
	// rejected by an earlier, narrower charset check here even though it
	// is a perfectly valid role Postgres itself created and would have
	// GRANTed correctly). The ONLY unsafe input for a double-quoted
	// identifier is a NUL byte (Postgres/the wire protocol cannot
	// represent one inside a C string, and Go's database/sql driver would
	// reject it before this ever reaches the server) -- doubling every
	// embedded `"` below is what makes any other string safe to
	// interpolate, the same mechanism psql's own `%I`/`format(...,
	// :'ident')` quoting (acr-db-init.sh's runtime-acl mode) relies on.
	if strings.ContainsRune(role, 0) {
		return fmt.Errorf("runtime role name %q contains a NUL byte", role)
	}
	quotedRole := `"` + strings.ReplaceAll(role, `"`, `""`) + `"`

	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		`REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.oauth_device_grants FROM %s`,
		quotedRole,
	)); err != nil {
		return fmt.Errorf("revoke acr.oauth_device_grants privileges from %s: %w", role, err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		`GRANT SELECT, INSERT ON TABLE acr.oauth_device_grants TO %s`,
		quotedRole,
	)); err != nil {
		return fmt.Errorf("grant acr.oauth_device_grants privileges to %s: %w", role, err)
	}
	_, err = fmt.Fprintf(output, "granted SELECT, INSERT on acr.oauth_device_grants to %s\n", role)
	return err
}
