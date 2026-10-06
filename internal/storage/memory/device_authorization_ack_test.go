package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type ackFixture struct {
	*deviceAuthorizationFixture
	hash  storage.DeviceCodeHash
	grant storage.DeviceAuthorizationGrant
}

func newApprovedAckFixture(t *testing.T) ackFixture {
	t.Helper()
	fixture := newDeviceAuthorizationFixture(t)
	record := fixture.createPending(t, "ack")
	grant := validDeviceAuthorizationGrant()
	_, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.NoError(t, err)
	return ackFixture{deviceAuthorizationFixture: fixture, hash: record.DeviceCodeHash, grant: grant}
}

func (f ackFixture) redeemAckable(t *testing.T) (string, error) {
	t.Helper()
	credential, err := f.store.RedeemAckable(context.Background(), f.hash, f.prepareCredential(t, f.grant).StorageInput())
	return credential.CredentialID, err
}

// liveCredentials counts unrevoked credentials from the credential store
// itself, independent of the device authorization record under test.
func (f ackFixture) liveCredentials(t *testing.T) []string {
	t.Helper()
	all, err := f.credentials.List(context.Background(), f.grant.OrgID)
	require.NoError(t, err)
	var live []string
	for _, credential := range all {
		if credential.RevokedAt == nil {
			live = append(live, credential.CredentialID)
		}
	}
	return live
}

func TestDeviceCredentialAck_acknowledgedCredentialSurvivesTheSweep(t *testing.T) {
	f := newApprovedAckFixture(t)
	id, err := f.redeemAckable(t)
	require.NoError(t, err)

	ackedAt, err := f.store.AcknowledgeCredential(context.Background(), f.grant.OrgID, id)
	require.NoError(t, err)
	require.Equal(t, f.now.UTC(), ackedAt)
	again, err := f.store.AcknowledgeCredential(context.Background(), f.grant.OrgID, id)
	require.NoError(t, err)
	require.Equal(t, ackedAt, again)

	f.now = f.now.Add(storage.DeviceCredentialAckWindow + time.Hour)
	revoked, err := f.store.RevokeUnacknowledged(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, revoked)
	require.Equal(t, []string{id}, f.liveCredentials(t))
}

func TestDeviceCredentialAck_lostResponseRetryInsideWindowReplacesTheCredential(t *testing.T) {
	f := newApprovedAckFixture(t)
	first, err := f.redeemAckable(t)
	require.NoError(t, err)

	f.now = f.now.Add(storage.DeviceCredentialAckWindow - time.Second)
	second, err := f.redeemAckable(t)
	require.NoError(t, err)

	require.NotEqual(t, first, second)
	require.Equal(t, []string{second}, f.liveCredentials(t), "exactly one live credential, the replacement")
	record, err := f.store.GetByDeviceCodeHash(context.Background(), f.hash)
	require.NoError(t, err)
	require.Equal(t, second, record.RedeemedCredentialID)
	_, err = f.store.AcknowledgeCredential(context.Background(), f.grant.OrgID, first)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationNotFound, "the replaced credential can no longer be acknowledged")
	_, err = f.store.AcknowledgeCredential(context.Background(), f.grant.OrgID, second)
	require.NoError(t, err)
}

func TestDeviceCredentialAck_retryAfterWindowIsRefusedAndSweepRevokes(t *testing.T) {
	f := newApprovedAckFixture(t)
	first, err := f.redeemAckable(t)
	require.NoError(t, err)

	f.now = f.now.Add(storage.DeviceCredentialAckWindow)
	_, err = f.redeemAckable(t)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationConflict)
	var conflict *storage.DeviceAuthorizationError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, storage.DeviceConflictAckWindowElapsed, conflict.Reason)
	require.Equal(t, []string{first}, f.liveCredentials(t), "refusing the retry mints nothing")

	revoked, err := f.store.RevokeUnacknowledged(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, revoked)
	require.Empty(t, f.liveCredentials(t))
	revoked, err = f.store.RevokeUnacknowledged(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, revoked, "a revoked credential is not swept twice")
}

func TestDeviceCredentialAck_sweepLeavesCredentialInsideWindow(t *testing.T) {
	f := newApprovedAckFixture(t)
	id, err := f.redeemAckable(t)
	require.NoError(t, err)

	f.now = f.now.Add(storage.DeviceCredentialAckWindow - time.Second)
	revoked, err := f.store.RevokeUnacknowledged(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, revoked)
	require.Equal(t, []string{id}, f.liveCredentials(t))
}

func TestDeviceCredentialAck_retryAfterAcknowledgementIsRefused(t *testing.T) {
	f := newApprovedAckFixture(t)
	id, err := f.redeemAckable(t)
	require.NoError(t, err)
	_, err = f.store.AcknowledgeCredential(context.Background(), f.grant.OrgID, id)
	require.NoError(t, err)

	_, err = f.redeemAckable(t)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationConflict)
	var conflict *storage.DeviceAuthorizationError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, storage.DeviceConflictAcknowledged, conflict.Reason)
	require.Equal(t, []string{id}, f.liveCredentials(t))
}

func TestDeviceCredentialAck_unknownOrForeignIDIsNotFound(t *testing.T) {
	f := newApprovedAckFixture(t)
	id, err := f.redeemAckable(t)
	require.NoError(t, err)

	_, err = f.store.AcknowledgeCredential(context.Background(), f.grant.OrgID, "cred_does_not_exist")
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationNotFound)
	_, err = f.store.AcknowledgeCredential(context.Background(), "00000000-0000-4000-8000-000000000999", id)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationNotFound)
	f.now = f.now.Add(storage.DeviceCredentialAckWindow)
	revoked, err := f.store.RevokeUnacknowledged(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, revoked, "a rejected acknowledgement leaves the credential unacknowledged")
}

func TestDeviceCredentialAck_plainRedeemIsNeverAckedOrSwept(t *testing.T) {
	f := newApprovedAckFixture(t)
	credential, err := f.store.Redeem(context.Background(), f.hash, f.prepareCredential(t, f.grant).StorageInput())
	require.NoError(t, err)

	_, err = f.store.AcknowledgeCredential(context.Background(), f.grant.OrgID, credential.CredentialID)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationNotFound)
	f.now = f.now.Add(24 * time.Hour)
	revoked, err := f.store.RevokeUnacknowledged(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, revoked)
	_, err = f.redeemAckable(t)
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationConflict)
	require.Equal(t, []string{credential.CredentialID}, f.liveCredentials(t))
}

func TestDeviceCredentialAck_concurrentRetriesLeaveOneLiveCredential(t *testing.T) {
	f := newApprovedAckFixture(t)
	_, err := f.redeemAckable(t)
	require.NoError(t, err)
	f.now = f.now.Add(time.Second)
	inputs := make([]storage.CredentialCreateInput, 8)
	for i := range inputs {
		inputs[i] = f.prepareCredential(t, f.grant).StorageInput()
	}

	var wg sync.WaitGroup
	for _, input := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.store.RedeemAckable(context.Background(), f.hash, input)
		}()
	}
	wg.Wait()

	require.Len(t, f.liveCredentials(t), 1)
}
