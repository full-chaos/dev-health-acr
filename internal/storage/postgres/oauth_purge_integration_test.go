package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// oauthPurgeFixture builds rows for the CHAOS-6191 purge against real
// PostgreSQL. Everything is created at t0 unless a test says otherwise; the
// purge's own clock is passed explicitly, so no test sleeps.
type oauthPurgeFixture struct {
	t       *testing.T
	ctx     context.Context
	db      *sql.DB
	devices *DeviceAuthorizationStore
	store   *OAuthStore
	t0      time.Time
	serial  int
}

const (
	oauthPurgeGrace = 24 * time.Hour
	oauthPurgeIdle  = 30 * 24 * time.Hour
)

func newOAuthPurgeFixture(t *testing.T) *oauthPurgeFixture {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	devices, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return t0 }})
	require.NoError(t, err)
	store, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return t0 }})
	require.NoError(t, err)
	return &oauthPurgeFixture{t: t, ctx: ctx, db: db, devices: devices, store: store, t0: t0}
}

func (f *oauthPurgeFixture) next() int {
	f.serial++
	return f.serial
}

// device creates a pending device authorization and returns its code hash.
func (f *oauthPurgeFixture) device() storage.DeviceCodeHash {
	f.t.Helper()
	n := f.next()
	device, err := f.devices.Create(f.ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode(fmt.Sprintf("purge-device-%d", n)),
		UserCodeHash:   storage.HashUserCode(fmt.Sprintf("PURGE%03d", n)),
	})
	require.NoError(f.t, err)
	return device.DeviceCodeHash
}

// redeemedDevice creates a device authorization, approves and redeems it (so
// it points at a real acr.client_credentials row expiring at expiresAt, nil =
// no expiry), and returns the device code hash and the credential id.
func (f *oauthPurgeFixture) redeemedDevice(expiresAt *time.Time) (storage.DeviceCodeHash, string) {
	f.t.Helper()
	n := f.next()
	device, err := f.devices.Create(f.ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode(fmt.Sprintf("purge-redeemed-device-%d", n)),
		UserCodeHash:   storage.HashUserCode(fmt.Sprintf("REDEEM%03d", n)),
	})
	require.NoError(f.t, err)
	grant := postgresDeviceAuthorizationGrant()
	_, err = f.devices.Approve(f.ctx, device.UserCodeHash, grant)
	require.NoError(f.t, err)
	credentialID := fmt.Sprintf("cred_purge_%d", n)
	_, err = f.devices.Redeem(f.ctx, device.DeviceCodeHash, storage.CredentialCreateInput{
		CredentialID: credentialID, OrgID: grant.OrgID, Name: "purge fixture",
		TokenPrefix: fmt.Sprintf("fcacr_purge%d", n), TokenHash: fmt.Sprintf("%064x", n),
		RepositoryScopes: grant.RepositoryScopes, Scopes: grant.Scopes, ActorID: grant.ApprovingSubject,
		ExpiresAt: expiresAt, Resource: "https://example.com/resource",
	})
	require.NoError(f.t, err)
	return device.DeviceCodeHash, credentialID
}

func (f *oauthPurgeFixture) revoke(credentialID string) {
	f.t.Helper()
	_, err := f.db.ExecContext(f.ctx, `UPDATE acr.client_credentials SET revoked_at = $2 WHERE credential_id = $1`, credentialID, f.t0.Add(time.Hour))
	require.NoError(f.t, err)
}

func (f *oauthPurgeFixture) expireCredential(credentialID string, at time.Time) {
	f.t.Helper()
	_, err := f.db.ExecContext(f.ctx, `UPDATE acr.client_credentials SET expires_at = $2 WHERE credential_id = $1`, credentialID, at)
	require.NoError(f.t, err)
}

// client registers a dynamic client created at createdAt.
func (f *oauthPurgeFixture) client(seed byte, createdAt time.Time) string {
	f.t.Helper()
	client := validOAuthClientPG(dynamicOAuthClientIDPG(seed))
	client.CreatedAt = createdAt
	_, err := f.store.RegisterClient(f.ctx, client)
	require.NoError(f.t, err)
	return client.ClientID
}

// request stores an /authorize request created at createdAt (expiring
// storage.DeviceAuthorizationTTL later, like the real one) on device.
func (f *oauthPurgeFixture) request(clientID string, device storage.DeviceCodeHash, createdAt time.Time) storage.OAuthSecretHash {
	f.t.Helper()
	request := validOAuthAuthorizationRequestPG(createdAt, device, fmt.Sprintf("purge-handle-%d", f.next()))
	request.ClientID = clientID
	request.ExpiresAt = createdAt.Add(storage.DeviceAuthorizationTTL)
	_, err := f.store.CreateAuthorizationRequest(f.ctx, request)
	require.NoError(f.t, err)
	return request.HandleHash
}

func (f *oauthPurgeFixture) deviceGrant(clientID string, device storage.DeviceCodeHash, createdAt time.Time) {
	f.t.Helper()
	grant := validOAuthDeviceGrantPG(createdAt, device)
	grant.ClientID = clientID
	_, err := f.store.CreateDeviceGrant(f.ctx, grant)
	require.NoError(f.t, err)
}

func (f *oauthPurgeFixture) requestExists(handle storage.OAuthSecretHash) bool {
	f.t.Helper()
	var count int
	require.NoError(f.t, f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM acr.oauth_authorization_requests WHERE handle_hash = $1`, handle.String()).Scan(&count))
	return count == 1
}

func (f *oauthPurgeFixture) clientExists(clientID string) bool {
	f.t.Helper()
	var count int
	require.NoError(f.t, f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM acr.oauth_clients WHERE client_id = $1`, clientID).Scan(&count))
	return count == 1
}

func (f *oauthPurgeFixture) count(table string) int {
	f.t.Helper()
	var count int
	require.NoError(f.t, f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM `+table).Scan(&count))
	return count
}

func (f *oauthPurgeFixture) purge(now time.Time, limit int) OAuthPurgeResult {
	f.t.Helper()
	result, err := f.store.PurgeExpired(f.ctx, now, oauthPurgeGrace, oauthPurgeIdle, limit)
	require.NoError(f.t, err)
	return result
}

// The request statement deletes a request only once it is past
// expires_at + grace AND its device authorization did not redeem a credential
// that is still live. A revoked or expired credential does not keep it.
func TestOAuthStore_PurgeExpired_requests(t *testing.T) {
	// Given: a purge 10 days after t0 (a 30-day credential issued at t0 is live)
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(10 * 24 * time.Hour)
	thirtyDays := f.t0.Add(30 * 24 * time.Hour)
	client := f.client(0xa1, f.t0)

	old := f.request(client, f.device(), f.t0)
	fresh := f.request(client, f.device(), purgeAt.Add(-time.Hour))
	justInsideGrace := f.request(client, f.device(), purgeAt.Add(-oauthPurgeGrace).Add(-storage.DeviceAuthorizationTTL).Add(time.Minute))

	liveDevice, _ := f.redeemedDevice(&thirtyDays)
	live := f.request(client, liveDevice, f.t0)
	noExpiryDevice, _ := f.redeemedDevice(nil)
	noExpiry := f.request(client, noExpiryDevice, f.t0)

	revokedDevice, revokedCredential := f.redeemedDevice(&thirtyDays)
	f.revoke(revokedCredential)
	revoked := f.request(client, revokedDevice, f.t0)
	expiredDevice, expiredCredential := f.redeemedDevice(&thirtyDays)
	f.expireCredential(expiredCredential, f.t0.Add(24*time.Hour))
	expired := f.request(client, expiredDevice, f.t0)

	// A request whose device authorization ALSO backs a device grant row: no
	// foreign key points at oauth_authorization_requests, so the delete is not
	// blocked, and the grant row (its own FK is to device_authorizations) stays.
	referencedDevice := f.device()
	referenced := f.request(client, referencedDevice, f.t0)
	f.deviceGrant(client, referencedDevice, f.t0)

	// When
	result := f.purge(purgeAt, 500)

	// Then
	require.Equal(t, OAuthPurgeResult{Requests: 4, Clients: 0}, result)
	for name, handle := range map[string]storage.OAuthSecretHash{"old": old, "revoked-credential": revoked, "expired-credential": expired, "referenced": referenced} {
		require.Falsef(t, f.requestExists(handle), "%s: an expired request past the grace with no live credential must be purged", name)
	}
	for name, handle := range map[string]storage.OAuthSecretHash{"fresh": fresh, "inside-grace": justInsideGrace, "live-credential": live, "no-expiry-credential": noExpiry} {
		require.Truef(t, f.requestExists(handle), "%s: must be kept", name)
	}
	require.Equal(t, 1, f.count("acr.oauth_device_grants"), "purging a request must not delete the device grant that shares its device authorization")

	// And: once the live credential itself has ended, its request goes too
	// (with fresh and inside-grace, which are past the grace by then); a
	// credential with no expiry is live forever, so its request never does,
	// and while it stays the client is not idle.
	afterCredential := thirtyDays.Add(oauthPurgeGrace)
	require.Equal(t, OAuthPurgeResult{Requests: 3, Clients: 0}, f.purge(afterCredential, 500))
	require.False(t, f.requestExists(live))
	require.True(t, f.requestExists(noExpiry))
	require.True(t, f.clientExists(client))
}

// The client statement deletes only a dynamic client registered before
// now-idle that has no request row, no device grant inside the window and no
// live credential reachable through a device grant; requests go first, so a
// client whose last request just aged out is collected in the same call.
func TestOAuthStore_PurgeExpired_clients(t *testing.T) {
	// Given: a purge 40 days after t0; a credential issued at t0 for 45 days is live
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(40 * 24 * time.Hour)
	credentialEnd := f.t0.Add(45 * 24 * time.Hour)

	idle := f.client(0xb1, f.t0)
	young := f.client(0xb2, purgeAt.Add(-24*time.Hour))
	withRecentRequest := f.client(0xb3, f.t0)
	recentRequest := f.request(withRecentRequest, f.device(), purgeAt.Add(-time.Hour))

	// The session that must never be cut: idle for 30+ days as far as
	// /authorize is concerned, but its credential is live. Through a request
	// row (kept by the request statement because the credential is live) ...
	liveThroughRequest := f.client(0xb4, f.t0)
	liveRequestDevice, _ := f.redeemedDevice(&credentialEnd)
	liveRequest := f.request(liveThroughRequest, liveRequestDevice, f.t0)
	// ... and through an old device grant (device grants are never purged).
	liveThroughGrant := f.client(0xb5, f.t0)
	liveGrantDevice, _ := f.redeemedDevice(&credentialEnd)
	f.deviceGrant(liveThroughGrant, liveGrantDevice, f.t0)

	deadThroughGrant := f.client(0xb6, f.t0)
	deadGrantDevice, deadCredential := f.redeemedDevice(&credentialEnd)
	f.revoke(deadCredential)
	f.deviceGrant(deadThroughGrant, deadGrantDevice, f.t0)

	recentGrant := f.client(0xb7, f.t0)
	f.deviceGrant(recentGrant, f.device(), purgeAt.Add(-24*time.Hour))

	justAgedOut := f.client(0xb8, f.t0)
	agedOutRequest := f.request(justAgedOut, f.device(), f.t0)

	// When
	result := f.purge(purgeAt, 500)

	// Then
	require.Equal(t, OAuthPurgeResult{Requests: 1, Clients: 3}, result)
	for name, id := range map[string]string{"idle": idle, "revoked-credential-via-grant": deadThroughGrant, "last-request-just-aged-out": justAgedOut} {
		require.Falsef(t, f.clientExists(id), "%s: an idle client must be purged", name)
	}
	for name, id := range map[string]string{
		"young": young, "recent-request": withRecentRequest, "live-credential-via-request": liveThroughRequest,
		"live-credential-via-grant": liveThroughGrant, "recent-grant": recentGrant,
	} {
		require.Truef(t, f.clientExists(id), "%s: must survive", name)
	}
	require.True(t, f.requestExists(recentRequest))
	require.True(t, f.requestExists(liveRequest), "the request that links a client to its live credential must be kept")
	require.False(t, f.requestExists(agedOutRequest))

	// And: a second purge is a no-op (nothing new became eligible).
	require.Equal(t, OAuthPurgeResult{}, f.purge(purgeAt, 500))

	// And: when the credential ends, the client follows within the grace.
	// 46 days after t0 the two live-credential clients and the recent-request
	// client are collectable (their requests aged past the grace, requests
	// first); "young" and "recent-grant" are still inside the 30-day window.
	afterCredential := credentialEnd.Add(oauthPurgeGrace).Add(time.Hour)
	require.Equal(t, OAuthPurgeResult{Requests: 2, Clients: 3}, f.purge(afterCredential, 500))
	for name, id := range map[string]string{"recent-request": withRecentRequest, "live-via-request": liveThroughRequest, "live-via-grant": liveThroughGrant} {
		require.Falsef(t, f.clientExists(id), "%s: purged once its credential ended and its requests aged out", name)
	}
	require.True(t, f.clientExists(young))
	require.True(t, f.clientExists(recentGrant))
}

func TestOAuthStore_PurgeExpired_batchIsBounded(t *testing.T) {
	// Given: 5 purgeable requests and 5 idle clients
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(40 * 24 * time.Hour)
	requestClient := f.client(0xc0, purgeAt.Add(-time.Hour))
	for range 5 {
		f.request(requestClient, f.device(), f.t0)
	}
	for seed := byte(0xc1); seed <= 0xc5; seed++ {
		f.client(seed, f.t0)
	}

	// When / Then: two, two, one, none -- per statement
	require.Equal(t, OAuthPurgeResult{Requests: 2, Clients: 2}, f.purge(purgeAt, 2))
	require.Equal(t, OAuthPurgeResult{Requests: 2, Clients: 2}, f.purge(purgeAt, 2))
	require.Equal(t, OAuthPurgeResult{Requests: 1, Clients: 1}, f.purge(purgeAt, 2))
	require.Equal(t, OAuthPurgeResult{}, f.purge(purgeAt, 2))
	require.Equal(t, 0, f.count("acr.oauth_authorization_requests"))
	require.Equal(t, 1, f.count("acr.oauth_clients"), "only the young client is left")
}

// Every row of acr.oauth_clients is a dynamic client by construction (the
// table's CHECK), so a pre-registered client cannot be stored there to be
// purged; the purge input is validated before any statement runs.
func TestOAuthStore_PurgeExpired_inputAndInvariants(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	now := f.t0.Add(100 * 24 * time.Hour)

	_, err := f.db.ExecContext(f.ctx, `INSERT INTO acr.oauth_clients (client_id, client_name, redirect_uris, created_at) VALUES ('preregistered-client', 'static', '[]'::jsonb, $1)`, f.t0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth_clients_client_id_check", "a non-dynamic client id must be refused by the table itself")

	result, err := f.store.PurgeExpired(f.ctx, now, oauthPurgeGrace, oauthPurgeIdle, 0)
	require.NoError(t, err)
	require.Equal(t, OAuthPurgeResult{}, result, "a non-positive batch limit deletes nothing")
	for _, windows := range [][2]time.Duration{{0, oauthPurgeIdle}, {-time.Hour, oauthPurgeIdle}, {oauthPurgeGrace, oauthPurgeGrace}, {oauthPurgeGrace, 0}} {
		_, err = f.store.PurgeExpired(f.ctx, now, windows[0], windows[1], 10)
		require.ErrorIs(t, err, storage.ErrInvalidOAuthClient, "grace=%v idle=%v", windows[0], windows[1])
	}
}
