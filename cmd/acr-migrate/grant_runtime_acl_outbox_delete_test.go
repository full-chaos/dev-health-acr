package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/dbacl"
)

// Real-database cells of the outbox DELETE grant (the reaper). Lead-run.

const outboxDeleteSeed = `INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at, published_at)
VALUES (gen_random_uuid(), gen_random_uuid(), 'cred_del', 1, 'acr_credential.issued', '{}'::jsonb, now(), now())`

func TestOutboxDeleteGrant_TheRuntimeRoleDeletesWithThePublisherAndStillCannotUpdateOtherColumns(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_outbox_delete_on", Features: dbacl.Features{dbacl.AuthProjection: true}})
	_, err := database.Owner.ExecContext(ctx, outboxDeleteSeed)
	require.NoError(t, err)
	_, err = database.Runtime.ExecContext(ctx, `UPDATE acr.credential_lifecycle_outbox SET payload = '{"x":1}'::jsonb WHERE aggregate_id = 'cred_del'`)
	requireDenied(t, err, "UPDATE of payload stays refused with the DELETE grant")
	result, err := database.Runtime.ExecContext(ctx, `DELETE FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_del'`)
	require.NoError(t, err, "the reaper's DELETE with the publisher on")
	n, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the DELETE took the row")
}

func TestOutboxDeleteGrant_WithTheFeatureOffTheRoleCannotDelete(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_outbox_delete_off"})
	_, err := database.Owner.ExecContext(ctx, outboxDeleteSeed)
	require.NoError(t, err)
	_, err = database.Runtime.ExecContext(ctx, `DELETE FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_del'`)
	requireDenied(t, err, "DELETE with the publisher off")
}

func TestOutboxDeleteGrant_TurningTheFeatureOffRevokesWhatAnEarlierGrantJobRunGranted(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_outbox_delete_revoke", Features: dbacl.Features{dbacl.AuthProjection: true}})
	_, err := database.Owner.ExecContext(ctx, outboxDeleteSeed)
	require.NoError(t, err)
	_, err = database.Runtime.ExecContext(ctx, `DELETE FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_del'`)
	require.NoError(t, err, "the DELETE works while the feature is on")
	_, err = database.Owner.ExecContext(ctx, outboxDeleteSeed)
	require.NoError(t, err)
	environmentOff := map[string]string{
		migrationDSNEnvironment: database.DSN,
		runtimeDSNEnvironment:   "postgres://" + database.Role + ":unused@runtime-dsn-is-never-dialed.invalid:5432/acr",
	}
	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(environmentOff), &bytes.Buffer{}))
	_, err = database.Runtime.ExecContext(ctx, `DELETE FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_del'`)
	requireDenied(t, err, "DELETE after the feature went off")
	_, err = database.Runtime.ExecContext(ctx, `SELECT revision FROM acr.credential_lifecycle_outbox`)
	require.NoError(t, err, "the table's SELECT stays after the DELETE revoke")
	_, err = database.Runtime.ExecContext(ctx, `INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
VALUES (gen_random_uuid(), gen_random_uuid(), 'cred_del_ins', 1, 'acr_credential.issued', '{}'::jsonb, now())`)
	require.NoError(t, err, "the mutation path's INSERT stays after the DELETE revoke")
}

func TestComposeBlock_OnToOffRevokesTheOutboxDeleteGrant(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_compose_delete", SkipGrant: true})
	runBlock := func(vars map[string]bool) {
		for _, statement := range composeStatements(t, database.Role, vars) {
			_, err := database.Owner.ExecContext(ctx, statement)
			require.NoError(t, err, statement)
		}
	}
	held := func(privilege string) bool {
		var has bool
		require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, 'acr.credential_lifecycle_outbox', $2)`, database.Role, privilege).Scan(&has))
		return has
	}
	runBlock(map[string]bool{"auth_projection": true})
	require.True(t, held("DELETE"), "DELETE is held after the compose run with the publisher on")
	runBlock(map[string]bool{"auth_projection": false})
	require.False(t, held("DELETE"), "the compose run with the publisher off clears the DELETE grant")
	require.True(t, held("SELECT") && held("INSERT"), "the mutation path's SELECT and INSERT stay after the publisher goes off")
}
