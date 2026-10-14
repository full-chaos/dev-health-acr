package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/dbacl"
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
// The grant list here deliberately covers acr.oauth_device_grants -- the
// one table CHAOS-6277 found missing its Kubernetes grant -- plus, since
// CHAOS-6191, DELETE alone on acr.oauth_clients and
// acr.oauth_authorization_requests for the bounded OAuth purge loop, plus, since
// CHAOS-7229, DELETE alone on acr.device_authorizations for the same loop.
// oauth_clients and oauth_authorization_requests (migration 0040) were both
// independently confirmed already correctly granted SELECT, INSERT, UPDATE
// on trial and prod when CHAOS-6277 was filed, so only the new privilege is
// added here; every other migration between 0038 and 0042 either predates
// this range or only alters existing tables (0041), so no other table needs
// the same fix here (see CHAOS-6277's RISK-NOTES).
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
func grantRuntimeACL(ctx context.Context, db *sql.DB, runtimeDSN string, features dbacl.Features, output io.Writer) error {
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
		// embedded `"` is doubled by QuoteIdent.
		if strings.ContainsRune(role, 0) {
			return fmt.Errorf("runtime role name %q contains a NUL byte", role)
		}
		// Bare, repeatable GRANTs (never REVOKE first): idempotent, and never
		// interrupt a role already serving traffic.
		for _, statement := range dbacl.GrantStatements(dbacl.QuoteIdent(role), features) {
			if _, err := db.ExecContext(ctx, statement.SQL); err != nil {
				return fmt.Errorf("grant privileges on acr.%s to %s: %w", statement.Table, role, err)
			}
		}
	}
	role := strings.Join(roles, ", ")
	for _, statement := range dbacl.GrantStatements("", features) {
		if len(statement.Privileges) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(output, "granted %s on acr.%s to %s\n", strings.Join(statement.Privileges, ", "), statement.Table, role); err != nil {
			return err
		}
	}
	return nil
}

// runtimeFeatures reads the optional runtime features that decide which rows of
// the declaration apply, from the same settings the runtime itself reads.
func runtimeFeatures(lookup lookupEnv) dbacl.Features {
	features := dbacl.Features{}
	if value, _ := lookup("ACR_ENABLE_EPISODE_WRITEBACK"); strings.TrimSpace(value) == "true" {
		features[dbacl.EpisodeWriteback] = true
	}
	audience, _ := lookup("ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE")
	trustDomain, _ := lookup("ACR_WORKLOAD_TRUST_DOMAIN")
	if strings.TrimSpace(audience) != "" && strings.TrimSpace(trustDomain) != "" {
		features[dbacl.WorkloadTokenExchange] = true
	}
	return features
}

// purgeDSNEnvironment names the purge role's DSN. grant-runtime-acl reads it,
// like the runtime DSN, only for the user name it embeds, and never dials it.
const purgeDSNEnvironment = "ACR_POSTGRES_PURGE_DSN"

// dsnRoles returns the database roles a DSN's connections run as: its login
// user and, when the DSN carries a role parameter (applied as SET ROLE on
// connect), that role too. Privileges are granted to all of them.
func dsnRoles(dsn string) ([]string, error) {
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid DSN")
	}
	roles := []string{parsed.User}
	if effective := parsed.RuntimeParams["role"]; effective != "" && effective != parsed.User {
		roles = append(roles, effective)
	}
	for _, role := range roles {
		if role == "" {
			return nil, errors.New("DSN has no username")
		}
		if strings.ContainsRune(role, 0) {
			return nil, errors.New("DSN role name contains a NUL byte")
		}
	}
	return roles, nil
}

// purgeGrantTargets returns the roles that receive the purge privileges: every
// role the purge DSN runs as. It refuses when any of them is a role the runtime
// DSN runs as, so the purge's DELETE grants never reach the role the API and the
// projector connect as, however either DSN names its roles.
func purgeGrantTargets(purgeDSN, runtimeDSN string) ([]string, error) {
	purge, err := dsnRoles(purgeDSN)
	if err != nil {
		return nil, fmt.Errorf("purge DSN: %w", err)
	}
	runtime, err := dsnRoles(runtimeDSN)
	if err != nil {
		return nil, fmt.Errorf("runtime DSN: %w", err)
	}
	for _, role := range purge {
		for _, other := range runtime {
			if role == other {
				return nil, fmt.Errorf("the purge role %q must not be a role the runtime runs as: the purge's privileges are not granted to the runtime role", role)
			}
		}
	}
	return purge, nil
}

// grantPurgeACL gives the org purge's own role exactly the privileges the purge
// statements need (internal/dbacl PurgeGrants), as bare idempotent GRANTs.
func grantPurgeACL(ctx context.Context, db *sql.DB, purgeDSN, runtimeDSN string, output io.Writer) error {
	roles, err := purgeGrantTargets(purgeDSN, runtimeDSN)
	if err != nil {
		return err
	}
	for _, role := range roles {
		for _, statement := range dbacl.PurgeGrantStatements(dbacl.QuoteIdent(role)) {
			if _, err := db.ExecContext(ctx, statement.SQL); err != nil {
				return fmt.Errorf("grant privileges on acr.%s to the purge role: %w", statement.Table, err)
			}
		}
	}
	_, err = fmt.Fprintf(output, "granted the org purge privileges (%d tables) to %s\n", len(dbacl.PurgeGrants), strings.Join(roles, ", "))
	return err
}
