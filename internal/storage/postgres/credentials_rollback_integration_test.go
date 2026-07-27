package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCredentialStore_RollbackCommitsSuccessorRevocationAndAuditTogether(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	ctx, db, lifecycle, source, successor := rollbackFixture(t, func() time.Time { return now })

	// When
	revoked, err := lifecycle.RollbackCredentialRotation(ctx, rollbackInput(source, successor, now.Add(time.Minute)))

	// Then
	require.NoError(t, err)
	require.Equal(t, successor.Credential.CredentialID, revoked.CredentialID)
	assertRollbackCommitted(t, ctx, db, successor.Credential.CredentialID, true, 1)
	storedSource, getErr := lifecycle.GetByID(ctx, credentialTestOrgID, source.Credential.CredentialID)
	require.NoError(t, getErr)
	require.Nil(t, storedSource.RevokedAt)
}

func TestCredentialStore_RollbackRejectsReceiptAtDeadlineWithoutPartialMutation(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	ctx, db, lifecycle, source, successor := rollbackFixture(t, func() time.Time { return now })

	// When
	_, err := lifecycle.RollbackCredentialRotation(ctx, rollbackInput(source, successor, now))

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
	assertRollbackCommitted(t, ctx, db, successor.Credential.CredentialID, false, 0)
}

func TestCredentialStore_RollbackRejectsMismatchedAuditRelationshipWithoutPartialMutation(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	ctx, db, lifecycle, source, successor := rollbackFixture(t, func() time.Time { return now })
	_, err := db.ExecContext(ctx, `
UPDATE acr.audit_events
SET metadata = jsonb_build_object('replacement_credential_id', 'cred_wrong')
WHERE org_id = $1 AND action = 'credential_rotated' AND resource_id = $2`, credentialTestOrgID, source.Credential.CredentialID)
	require.NoError(t, err)

	// When
	_, err = lifecycle.RollbackCredentialRotation(ctx, rollbackInput(source, successor, now.Add(time.Minute)))

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
	assertRollbackCommitted(t, ctx, db, successor.Credential.CredentialID, false, 0)
}

func TestCredentialStore_RollbackComparesAndSetsSuccessorWithoutDuplicateAudit(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	ctx, db, lifecycle, source, successor := rollbackFixture(t, func() time.Time { return now })
	input := rollbackInput(source, successor, now.Add(time.Minute))
	_, err := lifecycle.RollbackCredentialRotation(ctx, input)
	require.NoError(t, err)

	// When
	_, err = lifecycle.RollbackCredentialRotation(ctx, input)

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
	assertRollbackCommitted(t, ctx, db, successor.Credential.CredentialID, true, 1)
}

func TestCredentialStore_RollbackRechecksSourceActivityWithoutRevokingSuccessor(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	ctx, db, lifecycle, source, successor := rollbackFixture(t, func() time.Time { return now })
	_, err := lifecycle.RevokeCredential(ctx, storage.CredentialRevocationInput{
		OrgID: credentialTestOrgID, CredentialID: source.Credential.CredentialID, ActorID: credentialTestActorID,
	})
	require.NoError(t, err)

	// When
	_, err = lifecycle.RollbackCredentialRotation(ctx, rollbackInput(source, successor, now.Add(time.Minute)))

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
	assertRollbackCommitted(t, ctx, db, successor.Credential.CredentialID, false, 0)
}

func TestCredentialStore_RollbackRollsBackWhenSuccessorUpdateFails(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	ctx, db, lifecycle, source, successor := rollbackFixture(t, func() time.Time { return now })
	_, err := db.ExecContext(ctx, `UPDATE acr.client_credentials SET name = $1 WHERE credential_id = $2`, forcedLifecycleFailureName, successor.Credential.CredentialID)
	require.NoError(t, err)
	installLifecycleFailureTrigger(t, ctx, db)

	// When
	_, err = lifecycle.RollbackCredentialRotation(ctx, rollbackInput(source, successor, now.Add(time.Minute)))

	// Then
	require.ErrorIs(t, err, storage.ErrUnavailable)
	assertRollbackCommitted(t, ctx, db, successor.Credential.CredentialID, false, 0)
}

func TestCredentialStore_RollbackRollsBackWhenDeferredAuditConstraintFailsAtCommit(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	ctx, db, lifecycle, source, successor := rollbackFixture(t, func() time.Time { return now })
	installRollbackCommitFailureTrigger(t, ctx, db)

	// When
	_, err := lifecycle.RollbackCredentialRotation(ctx, rollbackInput(source, successor, now.Add(time.Minute)))

	// Then
	require.ErrorIs(t, err, storage.ErrUnavailable)
	assertRollbackCommitted(t, ctx, db, successor.Credential.CredentialID, false, 0)
}

func TestCredentialStore_RollbackCanceledWhileWaitingForSuccessorLockHasNoPartialMutation(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	ctx, db, lifecycle, source, successor := rollbackFixture(t, func() time.Time { return now })
	lockTx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockTx.Rollback() })
	var lockedID string
	require.NoError(t, lockTx.QueryRowContext(ctx, `
SELECT credential_id FROM acr.client_credentials WHERE org_id = $1 AND credential_id = $2 FOR UPDATE`, credentialTestOrgID, successor.Credential.CredentialID).Scan(&lockedID))
	require.Equal(t, successor.Credential.CredentialID, lockedID)
	canceledCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		_, rollbackErr := lifecycle.RollbackCredentialRotation(canceledCtx, rollbackInput(source, successor, now.Add(time.Minute)))
		result <- rollbackErr
	}()
	waitForCredentialLock(t, ctx, db)

	// When
	cancel()
	err = <-result

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, lockTx.Rollback())
	assertRollbackCommitted(t, ctx, db, successor.Credential.CredentialID, false, 0)
}

func rollbackFixture(t *testing.T, now func() time.Time) (context.Context, *sql.DB, *storage.CredentialLifecycle, auth.IssuedCredential, auth.IssuedCredential) {
	t.Helper()
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	_, lifecycle, err := newCredentialStore(db, audit, now)
	require.NoError(t, err)
	service, err := auth.NewService(lifecycle, auth.ServiceOptions{Now: now})
	require.NoError(t, err)
	source, err := service.Create(ctx, credentialCreateRequest("rollback-source"))
	require.NoError(t, err)
	successor, err := service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: source.Credential.CredentialID, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	return ctx, db, lifecycle, source, successor
}

func rollbackInput(source, successor auth.IssuedCredential, until time.Time) storage.CredentialRotationRollbackInput {
	return storage.CredentialRotationRollbackInput{
		OrgID: credentialTestOrgID, SourceCredentialID: source.Credential.CredentialID,
		SuccessorCredentialID: successor.Credential.CredentialID, ActorID: credentialTestActorID, RollbackUntil: until,
	}
}

func assertRollbackCommitted(t *testing.T, ctx context.Context, db *sql.DB, successorID string, revoked bool, audits int) {
	t.Helper()
	var revokedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `SELECT revoked_at FROM acr.client_credentials WHERE credential_id = $1`, successorID).Scan(&revokedAt))
	require.Equal(t, revoked, revokedAt.Valid)
	var auditCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.audit_events WHERE action = 'credential_revoked' AND resource_id = $1`, successorID).Scan(&auditCount))
	require.Equal(t, audits, auditCount)
}

func installRollbackCommitFailureTrigger(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
CREATE FUNCTION acr.fail_rollback_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.action = 'credential_revoked' THEN
    RAISE EXCEPTION 'forced deferred rollback failure';
  END IF;
  RETURN NEW;
END;
$$;
CREATE CONSTRAINT TRIGGER fail_rollback_commit
AFTER INSERT ON acr.audit_events DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION acr.fail_rollback_commit();`)
	require.NoError(t, err)
}
