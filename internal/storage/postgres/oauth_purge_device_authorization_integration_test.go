package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-7229: expired device authorizations are purged, cascading to the
// request and device grant rows behind them. These tests run the purge against
// real PostgreSQL and pin what it keeps as hard as what it deletes.

// deviceAt creates a pending device authorization created at createdAt (the
// fixture's own devices are all created at t0) and returns its code hash and
// user code hash.
func (f *oauthPurgeFixture) deviceAt(createdAt time.Time) (storage.DeviceCodeHash, storage.UserCodeHash) {
	f.t.Helper()
	n := f.next()
	audit, err := NewAuditStore(f.db)
	require.NoError(f.t, err)
	devices, err := NewDeviceAuthorizationStoreWithOptions(f.db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return createdAt }})
	require.NoError(f.t, err)
	device, err := devices.Create(f.ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode(fmt.Sprintf("purge-device-at-%d", n)),
		UserCodeHash:   storage.HashUserCode(fmt.Sprintf("DEVAT%03d", n)),
	})
	require.NoError(f.t, err)
	return device.DeviceCodeHash, device.UserCodeHash
}

func (f *oauthPurgeFixture) deviceExists(hash storage.DeviceCodeHash) bool {
	f.t.Helper()
	var count int
	require.NoError(f.t, f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM acr.device_authorizations WHERE device_code_hash = $1`, hash.String()).Scan(&count))
	return count == 1
}

// setDeviceState moves a pending row to a terminal state the way the store
// would have (approve and deny go through the store; 'expired' is what a poll
// writes lazily).
func (f *oauthPurgeFixture) approve(user storage.UserCodeHash) {
	f.t.Helper()
	_, err := f.devices.Approve(f.ctx, user, postgresDeviceAuthorizationGrant())
	require.NoError(f.t, err)
}

func (f *oauthPurgeFixture) deny(user storage.UserCodeHash) {
	f.t.Helper()
	_, err := f.devices.Deny(f.ctx, user)
	require.NoError(f.t, err)
}

func (f *oauthPurgeFixture) markExpired(hash storage.DeviceCodeHash) {
	f.t.Helper()
	_, err := f.db.ExecContext(f.ctx, `UPDATE acr.device_authorizations SET state = 'expired' WHERE device_code_hash = $1`, hash.String())
	require.NoError(f.t, err)
}

// The eligibility matrix: a device authorization is purged once it is past
// expires_at + grace, whatever its state, unless a live credential still
// points at it or a row its deletion would cascade to is inside its own
// retention. Every "kept" row here is one a flow or the client purge can still
// use.
func TestOAuthStore_PurgeExpired_deviceAuthorizations(t *testing.T) {
	// Given: a purge 40 days after t0 (grace 30d: rows created before t0+10d are
	// past it); a credential issued at t0 for 45 days is live
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(40 * 24 * time.Hour)
	credentialEnd := f.t0.Add(45 * 24 * time.Hour)
	day := 24 * time.Hour
	client := f.client(0xa9, purgeAt.Add(-time.Hour)) // young: never idle, so the client purge does not interfere

	// Purged: past the grace, in every state a row can be left in.
	pending := f.device() // abandoned /authorize row: never approved, never polled, stays 'pending'
	approvedDevice, approvedUser := f.deviceAt(f.t0)
	f.approve(approvedUser)
	deniedDevice, deniedUser := f.deviceAt(f.t0)
	f.deny(deniedUser)
	expiredDevice, _ := f.deviceAt(f.t0)
	f.markExpired(expiredDevice)
	revokedDevice, revokedCredential := f.redeemedDevice(&credentialEnd)
	f.revoke(revokedCredential)
	endedDevice, endedCredential := f.redeemedDevice(&credentialEnd)
	f.expireCredential(endedCredential, f.t0.Add(day))
	justPast, _ := f.deviceAt(purgeAt.Add(-30 * day).Add(-time.Minute).Add(-storage.DeviceAuthorizationTTL))

	// Kept: a live flow, a row inside the grace, a live credential, and rows a cascade would cut short.
	liveFlow, _ := f.deviceAt(purgeAt.Add(-time.Minute))
	insideGrace, _ := f.deviceAt(purgeAt.Add(-29 * day))
	liveCredentialDevice, _ := f.redeemedDevice(&credentialEnd)
	noExpiryDevice, _ := f.redeemedDevice(nil)
	requestInsideGrace, _ := f.deviceAt(f.t0)
	f.request(client, requestInsideGrace, purgeAt.Add(-day))
	grantInsideWindow, _ := f.deviceAt(f.t0)
	f.deviceGrant(client, grantInsideWindow, purgeAt.Add(-day))

	// Purged with their children: an old request and an old grant go with the row.
	withOldChildren, _ := f.deviceAt(f.t0)
	oldRequest := f.request(client, withOldChildren, f.t0)
	f.deviceGrant(client, withOldChildren, f.t0)

	// When
	result := f.purge(purgeAt, 500)

	// Then
	purged := map[string]storage.DeviceCodeHash{
		"pending-abandoned": pending, "approved-never-redeemed": approvedDevice, "denied": deniedDevice, "expired": expiredDevice,
		"redeemed-revoked-credential": revokedDevice, "redeemed-ended-credential": endedDevice,
		"just-past-the-grace": justPast, "old-request-and-grant": withOldChildren,
	}
	kept := map[string]storage.DeviceCodeHash{
		"live-flow": liveFlow, "inside-grace": insideGrace, "live-credential": liveCredentialDevice, "no-expiry-credential": noExpiryDevice,
		"request-inside-grace": requestInsideGrace, "grant-inside-window": grantInsideWindow,
	}
	for name, hash := range purged {
		require.Falsef(t, f.deviceExists(hash), "%s: an expired device authorization nothing needs must be purged", name)
	}
	for name, hash := range kept {
		require.Truef(t, f.deviceExists(hash), "%s: must be kept", name)
	}
	require.Equal(t, len(purged), result.DeviceAuthorizations)
	require.False(t, f.requestExists(oldRequest), "the request behind a purged device authorization goes with it")
	require.Equal(t, 1, f.count("acr.oauth_device_grants"), "the old grant went with its row; the one inside the window stays")
	require.Equal(t, 1, f.count("acr.oauth_authorization_requests"), "the old request went with its row; the one inside the grace stays")

	// And: a second purge is a no-op.
	require.Equal(t, OAuthPurgeResult{}, f.purge(purgeAt, 500))

	// And: once the credential ends, the rows it kept follow (a dead credential never revives).
	afterCredential := credentialEnd.Add(oauthPurgeGrace).Add(time.Hour)
	result = f.purge(afterCredential, 500)
	require.False(t, f.deviceExists(liveCredentialDevice))
	require.False(t, f.deviceExists(requestInsideGrace))
	require.False(t, f.deviceExists(grantInsideWindow))
	require.False(t, f.deviceExists(insideGrace))
	require.False(t, f.deviceExists(liveFlow))
	require.True(t, f.deviceExists(noExpiryDevice), "a credential with no expiry is live forever, so is its device authorization")
	require.Equal(t, 5, result.DeviceAuthorizations)
	require.Equal(t, 1, f.count("acr.device_authorizations"))
}

// The grace boundary is strict and exact: a row whose expires_at equals the
// cutoff is kept, one second earlier is purged.
func TestOAuthStore_PurgeExpired_deviceAuthorizationGraceBoundary(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	device := f.device() // created t0, expires t0+10m
	expiresAt := f.t0.Add(storage.DeviceAuthorizationTTL)

	require.Equal(t, OAuthPurgeResult{}, f.purge(expiresAt.Add(oauthPurgeGrace), 500), "expires_at == now-grace is not past the grace")
	require.True(t, f.deviceExists(device))
	require.Equal(t, OAuthPurgeResult{DeviceAuthorizations: 1}, f.purge(expiresAt.Add(oauthPurgeGrace).Add(time.Second), 500))
	require.False(t, f.deviceExists(device))
}

// A credential that is live when the purge looks keeps its device authorization
// even if the row is otherwise long gone; revoking the credential is what
// releases it.
func TestOAuthStore_PurgeExpired_deviceAuthorizationFollowsItsCredential(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	credentialEnd := purgeAt.Add(24 * time.Hour)
	device, credential := f.redeemedDevice(&credentialEnd)

	require.Equal(t, OAuthPurgeResult{}, f.purge(purgeAt, 500))
	require.True(t, f.deviceExists(device), "a live credential keeps the device authorization it was redeemed through")

	f.revoke(credential)
	require.Equal(t, OAuthPurgeResult{DeviceAuthorizations: 1}, f.purge(purgeAt, 500))
	require.False(t, f.deviceExists(device))
	require.Equal(t, 1, f.count("acr.client_credentials"), "the credential row itself is never touched")
}

// A row an in-flight transaction holds (FOR KEY SHARE, as the foreign-key check
// of an insert that references it does) is skipped, not deleted and not waited
// on; it goes on the next purge.
func TestOAuthStore_purgeSkipsADeviceAuthorizationAnInFlightWriteHasLocked(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	locked := f.device()
	free := f.device()

	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var one int
	require.NoError(t, tx.QueryRowContext(f.ctx, `SELECT 1 FROM acr.device_authorizations WHERE device_code_hash = $1 FOR KEY SHARE`, locked.String()).Scan(&one))

	// A bounded context: a purge that waited on the locked row would hang here.
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	result, err := f.store.PurgeExpired(ctx, purgeAt, oauthPurgeGrace, oauthPurgeIdle, 500)
	require.NoError(t, err, "the purge must skip a device authorization an in-flight write holds, not wait for it")

	require.Equal(t, OAuthPurgeResult{DeviceAuthorizations: 1}, result)
	require.True(t, f.deviceExists(locked))
	require.False(t, f.deviceExists(free))
	require.NoError(t, tx.Commit())
	require.Equal(t, OAuthPurgeResult{DeviceAuthorizations: 1}, f.purge(purgeAt, 500))
	require.False(t, f.deviceExists(locked))
}

// A candidate is deleted only if it is STILL eligible under a fresh snapshot: a
// credential that becomes live between the purge locking the row and deleting
// it (the credential row is not the purge's to lock) keeps its device
// authorization.
func TestOAuthStore_purgeRechecksADeviceAuthorizationUnderAFreshSnapshot(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	credentialEnd := f.t0.Add(45 * 24 * time.Hour) // ended long before purgeAt
	racing, credential := f.redeemedDevice(&credentialEnd)
	idle := f.device()
	f.store.afterDeviceAuthorizationLock = func() {
		_, err := f.db.ExecContext(f.ctx, `UPDATE acr.client_credentials SET expires_at = $2 WHERE credential_id = $1`, credential, purgeAt.Add(time.Hour))
		require.NoError(t, err)
	}

	result := f.purge(purgeAt, 500)

	require.Equal(t, OAuthPurgeResult{DeviceAuthorizations: 1}, result, "only the row that stayed ineligible is deleted")
	require.True(t, f.deviceExists(racing), "a device authorization whose credential became live after the purge's first look must be kept")
	require.False(t, f.deviceExists(idle))
}

// Deleting a device authorization cascades to its request and device grant
// rows. If another transaction holds one of those, the delete waits at most the
// purge lock timeout and then fails the tick (retried next tick) instead of
// hanging it; nothing is deleted, and the next purge after the lock is gone
// takes the row.
func TestOAuthStore_purgeDeviceAuthorizationDoesNotHangOnALockedChild(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	f.store.purgeLockTimeout = 300 * time.Millisecond
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	client := f.client(0xaa, purgeAt.Add(-time.Hour))
	device := f.device()
	handle := f.request(client, device, f.t0)

	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var one int
	require.NoError(t, tx.QueryRowContext(f.ctx, `SELECT 1 FROM acr.oauth_authorization_requests WHERE handle_hash = $1 FOR NO KEY UPDATE`, handle.String()).Scan(&one))

	// The request statement skips the locked request; the device statement's
	// cascade cannot, so it times out.
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	started := time.Now()
	result, err := f.store.PurgeExpired(ctx, purgeAt, oauthPurgeGrace, oauthPurgeIdle, 500)
	require.Error(t, err, "a cascade blocked by a locked child must fail the tick, not hang it")
	require.Less(t, time.Since(started), 15*time.Second)
	require.Equal(t, OAuthPurgeResult{}, result)
	require.True(t, f.deviceExists(device))
	require.True(t, f.requestExists(handle))

	require.NoError(t, tx.Commit())
	require.Equal(t, OAuthPurgeResult{Requests: 1, DeviceAuthorizations: 1}, f.purge(purgeAt, 500))
	require.False(t, f.deviceExists(device))
}
