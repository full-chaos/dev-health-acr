package postgres

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

type postgresAckFixture struct {
	ctx     context.Context
	db      *sql.DB
	store   *DeviceAuthorizationStore
	service *auth.Service
	now     time.Time
	hash    storage.DeviceCodeHash
	grant   storage.DeviceAuthorizationGrant
}

func newPostgresAckFixture(t *testing.T) *postgresAckFixture {
	t.Helper()
	f := &postgresAckFixture{ctx: context.Background(), now: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
	f.db = newCredentialStoreDatabase(t, f.ctx)
	audit, err := NewAuditStore(f.db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(f.db, audit)
	require.NoError(t, err)
	clock := func() time.Time { return f.now }
	f.service, err = auth.NewService(credentials, auth.ServiceOptions{Now: clock})
	require.NoError(t, err)
	f.store, err = NewDeviceAuthorizationStoreWithOptions(f.db, audit, DeviceAuthorizationStoreOptions{Now: clock})
	require.NoError(t, err)
	created, err := f.store.Create(f.ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("ack-device-code-value-32-bytes-long"),
		UserCodeHash:   storage.HashUserCode("ACKUSER1"),
	})
	require.NoError(t, err)
	f.grant = postgresDeviceAuthorizationGrant()
	_, err = f.store.Approve(f.ctx, created.UserCodeHash, f.grant)
	require.NoError(t, err)
	f.hash = created.DeviceCodeHash
	return f
}

func (f *postgresAckFixture) input(t *testing.T) storage.CredentialCreateInput {
	t.Helper()
	prepared, err := f.service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: f.grant.OrgID, Name: "device login", RepositoryScopes: f.grant.RepositoryScopes,
		Scopes: f.grant.Scopes, CreatedBy: f.grant.ApprovingSubject,
		ExpiresAt: postgresPointerTime(f.now.Add(30 * 24 * time.Hour)),
	})
	require.NoError(t, err)
	return prepared.StorageInput()
}

func (f *postgresAckFixture) redeemAckable(t *testing.T) (string, error) {
	t.Helper()
	credential, err := f.store.RedeemAckable(f.ctx, f.hash, f.input(t))
	return credential.CredentialID, err
}

func (f *postgresAckFixture) liveCredentialIDs(t *testing.T) []string {
	t.Helper()
	rows, err := f.db.QueryContext(f.ctx, "SELECT credential_id FROM acr.client_credentials WHERE org_id = $1 AND revoked_at IS NULL ORDER BY credential_id", f.grant.OrgID)
	require.NoError(t, err)
	defer rows.Close()
	var live []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		live = append(live, id)
	}
	require.NoError(t, rows.Err())
	return live
}

func (f *postgresAckFixture) revokedAuditCount(t *testing.T) int {
	t.Helper()
	var count int
	require.NoError(t, f.db.QueryRowContext(f.ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1 AND action = 'credential_revoked'", f.grant.OrgID).Scan(&count))
	return count
}

func TestDeviceCredentialAckStore_lostResponseRetryInsideWindowLeavesOneLiveCredential(t *testing.T) {
	f := newPostgresAckFixture(t)
	first, err := f.redeemAckable(t)
	require.NoError(t, err)

	f.now = f.now.Add(storage.DeviceCredentialAckWindow - time.Second)
	second, err := f.redeemAckable(t)
	require.NoError(t, err)

	require.NotEqual(t, first, second)
	require.Equal(t, []string{second}, f.liveCredentialIDs(t))
	require.Equal(t, 1, f.revokedAuditCount(t), "the replaced credential's revocation is audited")
	var recorded string
	var ackRequired bool
	require.NoError(t, f.db.QueryRowContext(f.ctx, "SELECT redeemed_credential_id, ack_required FROM acr.device_authorizations WHERE device_code_hash = $1", f.hash.String()).Scan(&recorded, &ackRequired))
	require.Equal(t, second, recorded)
	require.True(t, ackRequired)
}

func TestDeviceCredentialAckStore_retryAfterWindowIsRefusedAndTheSweepRevokes(t *testing.T) {
	f := newPostgresAckFixture(t)
	first, err := f.redeemAckable(t)
	require.NoError(t, err)

	f.now = f.now.Add(storage.DeviceCredentialAckWindow)
	_, err = f.redeemAckable(t)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationConflict)
	var conflict *storage.DeviceAuthorizationError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, storage.DeviceConflictAckWindowElapsed, conflict.Reason)
	require.Equal(t, []string{first}, f.liveCredentialIDs(t))

	revoked, err := f.store.RevokeUnacknowledged(f.ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, revoked)
	require.Empty(t, f.liveCredentialIDs(t))
	require.Equal(t, 1, f.revokedAuditCount(t))
	revoked, err = f.store.RevokeUnacknowledged(f.ctx, 10)
	require.NoError(t, err)
	require.Zero(t, revoked)
	require.Equal(t, 1, f.revokedAuditCount(t), "a second sweep audits nothing")
}

func TestDeviceCredentialAckStore_acknowledgementIsFinalAndScopedToTheOrganization(t *testing.T) {
	f := newPostgresAckFixture(t)
	id, err := f.redeemAckable(t)
	require.NoError(t, err)

	_, err = f.store.AcknowledgeCredential(f.ctx, "00000000-0000-4000-8000-000000000999", id)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationNotFound)
	_, err = f.store.AcknowledgeCredential(f.ctx, f.grant.OrgID, "cred_does_not_exist")
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationNotFound)
	ackedAt, err := f.store.AcknowledgeCredential(f.ctx, f.grant.OrgID, id)
	require.NoError(t, err)
	again, err := f.store.AcknowledgeCredential(f.ctx, f.grant.OrgID, id)
	require.NoError(t, err)
	require.True(t, ackedAt.Equal(again))

	f.now = f.now.Add(storage.DeviceCredentialAckWindow + time.Hour)
	revoked, err := f.store.RevokeUnacknowledged(f.ctx, 10)
	require.NoError(t, err)
	require.Zero(t, revoked)
	_, err = f.redeemAckable(t)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationConflict)
	require.Equal(t, []string{id}, f.liveCredentialIDs(t))
}

func TestDeviceCredentialAckStore_plainRedeemIsNeverSweptOrAcknowledged(t *testing.T) {
	f := newPostgresAckFixture(t)
	credential, err := f.store.Redeem(f.ctx, f.hash, f.input(t))
	require.NoError(t, err)

	_, err = f.store.AcknowledgeCredential(f.ctx, f.grant.OrgID, credential.CredentialID)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationNotFound)
	f.now = f.now.Add(24 * time.Hour)
	revoked, err := f.store.RevokeUnacknowledged(f.ctx, 10)
	require.NoError(t, err)
	require.Zero(t, revoked)
	require.Equal(t, []string{credential.CredentialID}, f.liveCredentialIDs(t))
}

func TestDeviceCredentialAckStore_concurrentRetriesLeaveOneLiveCredential(t *testing.T) {
	f := newPostgresAckFixture(t)
	_, err := f.redeemAckable(t)
	require.NoError(t, err)
	f.now = f.now.Add(time.Second)
	inputs := make([]storage.CredentialCreateInput, 6)
	for i := range inputs {
		inputs[i] = f.input(t)
	}

	var wg sync.WaitGroup
	for _, input := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.store.RedeemAckable(f.ctx, f.hash, input)
		}()
	}
	wg.Wait()

	live := f.liveCredentialIDs(t)
	require.Len(t, live, 1)
	var recorded string
	require.NoError(t, f.db.QueryRowContext(f.ctx, "SELECT redeemed_credential_id FROM acr.device_authorizations WHERE device_code_hash = $1", f.hash.String()).Scan(&recorded))
	require.Equal(t, live[0], recorded, "the live credential is the one the record names")
}

func TestDeviceCredentialAckStore_sweepSplitsABatchBetweenConcurrentPods(t *testing.T) {
	f := newPostgresAckFixture(t)
	id, err := f.redeemAckable(t)
	require.NoError(t, err)
	f.now = f.now.Add(storage.DeviceCredentialAckWindow)

	var wg sync.WaitGroup
	counts := make(chan int, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, sweepErr := f.store.RevokeUnacknowledged(f.ctx, 10)
			if sweepErr == nil {
				counts <- n
			}
		}()
	}
	wg.Wait()
	close(counts)

	total := 0
	for n := range counts {
		total += n
	}
	require.Equal(t, 1, total, "exactly one pod revokes the credential")
	require.NotContains(t, f.liveCredentialIDs(t), id)
	require.Equal(t, 1, f.revokedAuditCount(t))
}
