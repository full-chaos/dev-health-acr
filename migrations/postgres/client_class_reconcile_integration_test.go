package postgres

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
)

const reconcileOrgID = "70d529e0-0000-4000-8000-0000000012a5"

func insertReconcileCredential(t *testing.T, ctx context.Context, db *sql.DB, id, class string) {
	t.Helper()
	var classValue any
	if class != "" {
		classValue = class
	}
	_, err := db.ExecContext(ctx, `INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, created_at, client_class)
		VALUES ($1, $2, 'reconcile', 'fcacr_recn', 'hash-'||$1::text, '["context:read"]', now(), $3)`, id, reconcileOrgID, classValue)
	require.NoError(t, err)
}

func reconcileClass(t *testing.T, ctx context.Context, db *sql.DB, id string) sql.NullString {
	t.Helper()
	var class sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, "SELECT client_class FROM acr.client_credentials WHERE credential_id = $1", id).Scan(&class))
	return class
}

func reconcileTriggerState(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var state string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT tgenabled::text FROM pg_trigger
		WHERE tgrelid = 'acr.client_credentials'::regclass AND tgname = 'trg_client_credentials_provenance_immutable'`).Scan(&state))
	return state
}

// A row the previous release writes as operator after 0063 ran is relabelled
// by the next Apply, which applies no migration.
func TestRunner_applyRelabelsOperatorCredentialsWrittenAfter0063(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	count, err := runner.Apply(ctx, db)
	require.NoError(t, err)
	require.Equal(t, len(runner.migrations), count)

	insertReconcileCredential(t, ctx, db, "cred-operator", "operator")
	insertReconcileCredential(t, ctx, db, "cred-workload", "workload")
	insertReconcileCredential(t, ctx, db, "cred-bearer", "org_bearer")
	insertReconcileCredential(t, ctx, db, "cred-legacy", "")
	_, err = db.ExecContext(ctx, `INSERT INTO acr.audit_events (audit_event_id, org_id, actor_type, actor_id, action, resource_type, resource_id, status, metadata, created_at)
		VALUES ('00000000-0000-4000-8000-0000000012a6', $1, 'operator', 'cli', 'credential_created', 'client_credential', 'cred-operator', 'success', '{"client_class":"operator"}', now())`, reconcileOrgID)
	require.NoError(t, err)

	count, err = runner.Apply(ctx, db)
	require.NoError(t, err)
	require.Zero(t, count, "the step is not a migration and records no history row")

	require.Equal(t, sql.NullString{String: "org_bearer", Valid: true}, reconcileClass(t, ctx, db, "cred-operator"))
	require.Equal(t, sql.NullString{String: "workload", Valid: true}, reconcileClass(t, ctx, db, "cred-workload"))
	require.Equal(t, sql.NullString{String: "org_bearer", Valid: true}, reconcileClass(t, ctx, db, "cred-bearer"))
	require.False(t, reconcileClass(t, ctx, db, "cred-legacy").Valid)
	var auditClass string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT metadata->>'client_class' FROM acr.audit_events WHERE audit_event_id = '00000000-0000-4000-8000-0000000012a6'").Scan(&auditClass))
	require.Equal(t, "operator", auditClass)

	require.Equal(t, "O", reconcileTriggerState(t, ctx, db), "the immutability trigger is enabled after the step")
	_, err = db.ExecContext(ctx, "UPDATE acr.client_credentials SET client_class = 'workload' WHERE credential_id = 'cred-operator'")
	require.Error(t, err)
	require.Contains(t, err.Error(), "credential provenance is immutable")

	var history int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.schema_migrations").Scan(&history))
	require.Equal(t, len(runner.migrations), history)
}

// With no operator row the step takes no table lock: a writer holding ROW
// EXCLUSIVE on the table does not block Apply, while DISABLE TRIGGER (SHARE
// ROW EXCLUSIVE) would.
func TestRunner_applyWithNoOperatorRowTakesNoTableLock(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	runner, err := Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)
	insertReconcileCredential(t, ctx, db, "cred-bearer", "org_bearer")

	writer, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer writer.Rollback()
	_, err = writer.ExecContext(ctx, "UPDATE acr.client_credentials SET name = 'held' WHERE credential_id = 'cred-bearer'")
	require.NoError(t, err)

	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	count, err := runner.Apply(bounded, db)
	require.NoError(t, err, "Apply waited on a table lock with no operator row")
	require.Zero(t, count)
	require.NoError(t, writer.Commit())
	require.Equal(t, "O", reconcileTriggerState(t, ctx, db))
}

// Before 0063 the CHECK refuses org_bearer: the step is skipped and the row stays.
func TestRunner_applySkipsTheReconcileStepBefore0063(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	pre := fstest.MapFS{}
	entries, err := fs.ReadDir(Files, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name >= "0063_" {
			continue
		}
		body, readErr := fs.ReadFile(Files, name)
		require.NoError(t, readErr)
		pre[name] = &fstest.MapFile{Data: body}
	}
	require.Len(t, pre, 62, "every migration before 0063")
	released, err := NewRunner(pre)
	require.NoError(t, err)
	_, err = released.Apply(ctx, db)
	require.NoError(t, err)
	insertReconcileCredential(t, ctx, db, "cred-operator", "operator")

	count, err := released.Apply(ctx, db)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Equal(t, sql.NullString{String: "operator", Valid: true}, reconcileClass(t, ctx, db, "cred-operator"))
	require.Equal(t, "O", reconcileTriggerState(t, ctx, db))
}
