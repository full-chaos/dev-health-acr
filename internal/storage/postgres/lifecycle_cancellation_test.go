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

func TestCredentialStore_RevokeRollsBackWhenForUpdateLockIsCanceled(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	issued, err := service.Create(ctx, credentialCreateRequest("blocked-revoke"))
	require.NoError(t, err)
	lockTx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockTx.Rollback() })
	var lockedID string
	require.NoError(t, lockTx.QueryRowContext(ctx, `
SELECT credential_id FROM acr.client_credentials
WHERE org_id = $1 AND credential_id = $2 FOR UPDATE`, credentialTestOrgID, issued.Credential.CredentialID).Scan(&lockedID))
	require.Equal(t, issued.Credential.CredentialID, lockedID)
	canceledCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		_, revokeErr := service.Revoke(canceledCtx, credentialTestOrgID, issued.Credential.CredentialID, credentialTestActorID)
		result <- revokeErr
	}()
	waitForCredentialLock(t, ctx, db)

	// When
	cancel()
	err = <-result

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, storage.ErrUnavailable)
	require.NoError(t, lockTx.Rollback())
	var revokedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT revoked_at FROM acr.client_credentials WHERE org_id = $1 AND credential_id = $2`, credentialTestOrgID, issued.Credential.CredentialID).Scan(&revokedAt))
	require.False(t, revokedAt.Valid)
	var auditCount int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT count(*) FROM acr.audit_events WHERE action = 'credential_revoked' AND resource_id = $1`, issued.Credential.CredentialID).Scan(&auditCount))
	require.Zero(t, auditCount)
}

func waitForCredentialLock(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_stat_activity
  WHERE query LIKE '%FOR UPDATE%' AND wait_event_type = 'Lock'
)`).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			return
		}
		select {
		case <-timeout.C:
			t.Fatal("revoke did not block on credential FOR UPDATE lock")
		case <-ticker.C:
		}
	}
}
