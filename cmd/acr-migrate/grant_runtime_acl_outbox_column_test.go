package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/dbacl"
)

// Real-database cells of the outbox column grant (the lifecycle outbox publisher). Lead-run.

const outboxSeed = `INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
VALUES (gen_random_uuid(), gen_random_uuid(), 'cred_col', 1, 'acr_credential.issued', '{}'::jsonb, now())`

func requireDenied(t *testing.T, err error, what string) {
	t.Helper()
	require.Error(t, err, what)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, what)
	require.Equal(t, "42501", pgErr.Code, what+": "+pgErr.Message)
}

func TestOutboxColumnGrant_TheRuntimeRoleMarksPublishedAtAndNothingElse(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_outbox_column_on", Features: dbacl.Features{dbacl.AuthProjection: true}})
	_, err := database.Owner.ExecContext(ctx, outboxSeed)
	require.NoError(t, err)
	_, err = database.Runtime.ExecContext(ctx, `UPDATE acr.credential_lifecycle_outbox SET published_at = now() WHERE aggregate_id = 'cred_col'`)
	require.NoError(t, err, "the publisher's mark")
	_, err = database.Runtime.ExecContext(ctx, `SELECT revision FROM acr.credential_lifecycle_outbox WHERE published_at IS NULL FOR UPDATE SKIP LOCKED`)
	require.NoError(t, err, "the publisher's claim")
	for _, column := range []string{"outbox_id = gen_random_uuid()", "org_id = gen_random_uuid()", "aggregate_id = 'other'", "revision = 9", "event_type = 'acr_credential.revoked'", "payload = '{\"x\":1}'::jsonb", "created_at = now()"} {
		_, err = database.Runtime.ExecContext(ctx, `UPDATE acr.credential_lifecycle_outbox SET `+column+` WHERE aggregate_id = 'cred_col'`)
		requireDenied(t, err, "UPDATE of "+column)
	}
	var tableUpdate bool
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, 'acr.credential_lifecycle_outbox', 'UPDATE')`, database.Role).Scan(&tableUpdate))
	require.False(t, tableUpdate, "no table-level UPDATE")
}

func TestOutboxColumnGrant_WithTheFeatureOffTheRoleHoldsNoUpdate(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_outbox_column_off"})
	_, err := database.Owner.ExecContext(ctx, outboxSeed)
	require.NoError(t, err)
	_, err = database.Runtime.ExecContext(ctx, `UPDATE acr.credential_lifecycle_outbox SET published_at = now()`)
	requireDenied(t, err, "UPDATE of published_at with the publisher off")
	_, err = database.Runtime.ExecContext(ctx, `SELECT revision FROM acr.credential_lifecycle_outbox FOR UPDATE SKIP LOCKED`)
	requireDenied(t, err, "a row lock with the publisher off")
	_, err = database.Runtime.ExecContext(ctx, `INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
VALUES (gen_random_uuid(), gen_random_uuid(), 'cred_col_ins', 1, 'acr_credential.issued', '{}'::jsonb, now())`)
	require.NoError(t, err, "the mutation path's INSERT stays")
}

func TestOutboxColumnGrant_TurningTheFeatureOffRevokesWhatAnEarlierRunGranted(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_outbox_column_revoke", Features: dbacl.Features{dbacl.AuthProjection: true}})
	_, err := database.Owner.ExecContext(ctx, outboxSeed)
	require.NoError(t, err)
	_, err = database.Runtime.ExecContext(ctx, `UPDATE acr.credential_lifecycle_outbox SET published_at = now()`)
	require.NoError(t, err, "the mark works while the feature is on")
	environmentOff := map[string]string{
		migrationDSNEnvironment: database.DSN,
		runtimeDSNEnvironment:   "postgres://" + database.Role + ":unused@runtime-dsn-is-never-dialed.invalid:5432/acr",
	}
	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(environmentOff), &bytes.Buffer{}))
	_, err = database.Runtime.ExecContext(ctx, `UPDATE acr.credential_lifecycle_outbox SET published_at = NULL`)
	requireDenied(t, err, "UPDATE of published_at after the feature went off")
	_, err = database.Runtime.ExecContext(ctx, `SELECT revision, event_type FROM acr.credential_lifecycle_outbox`)
	require.NoError(t, err, "the table's SELECT stays")
}
