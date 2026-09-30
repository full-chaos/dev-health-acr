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
	// the defaults: request grace == client idle window
	oauthPurgeGrace = 30 * 24 * time.Hour
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
	// Given: a purge 50 days after t0 (a credential issued at t0 that ends at t0+60d is live)
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(50 * 24 * time.Hour)
	credentialEnd := f.t0.Add(60 * 24 * time.Hour)
	client := f.client(0xa1, f.t0)

	old := f.request(client, f.device(), f.t0)
	fresh := f.request(client, f.device(), purgeAt.Add(-time.Hour))
	justInsideGrace := f.request(client, f.device(), purgeAt.Add(-oauthPurgeGrace).Add(-storage.DeviceAuthorizationTTL).Add(time.Minute))

	liveDevice, _ := f.redeemedDevice(&credentialEnd)
	live := f.request(client, liveDevice, f.t0)
	noExpiryDevice, _ := f.redeemedDevice(nil)
	noExpiry := f.request(client, noExpiryDevice, f.t0)

	revokedDevice, revokedCredential := f.redeemedDevice(&credentialEnd)
	f.revoke(revokedCredential)
	revoked := f.request(client, revokedDevice, f.t0)
	expiredDevice, expiredCredential := f.redeemedDevice(&credentialEnd)
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
	require.Equal(t, OAuthPurgeResult{Requests: 4, Clients: 0, DeviceAuthorizations: 4}, result, "the four purged requests' device authorizations follow in the same call")
	for name, handle := range map[string]storage.OAuthSecretHash{"old": old, "revoked-credential": revoked, "expired-credential": expired, "referenced": referenced} {
		require.Falsef(t, f.requestExists(handle), "%s: an expired request past the grace with no live credential must be purged", name)
	}
	for name, handle := range map[string]storage.OAuthSecretHash{"fresh": fresh, "inside-grace": justInsideGrace, "live-credential": live, "no-expiry-credential": noExpiry} {
		require.Truef(t, f.requestExists(handle), "%s: must be kept", name)
	}
	// The request delete itself is not blocked by the device grant that shares its
	// device authorization (no foreign key points at a request); the grant goes
	// with the device authorization (ON DELETE CASCADE, CHAOS-7229), which the
	// same call then purges because it is past the grace.
	require.Equal(t, 0, f.count("acr.oauth_device_grants"), "the grant is deleted with its expired device authorization")
	require.Equal(t, 4, f.count("acr.device_authorizations"), "fresh, inside-grace, live-credential and no-expiry-credential device authorizations are kept")

	// And: once the live credential itself has ended, its request goes too
	// (with fresh and inside-grace, which are past the grace by then); a
	// credential with no expiry is live forever, so its request never does,
	// and while it stays the client is not idle.
	afterCredential := credentialEnd.Add(oauthPurgeGrace)
	require.Equal(t, OAuthPurgeResult{Requests: 3, Clients: 0, DeviceAuthorizations: 3}, f.purge(afterCredential, 500))
	require.False(t, f.requestExists(live))
	require.True(t, f.requestExists(noExpiry))
	require.True(t, f.clientExists(client))
	require.Equal(t, 1, f.count("acr.device_authorizations"), "only the no-expiry credential's device authorization is left")
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
	require.Equal(t, OAuthPurgeResult{Requests: 1, Clients: 3, DeviceAuthorizations: 2}, result,
		"device authorizations purged: the aged-out request's, and the revoked credential's (its grant is past the window); kept: recent request, recent grant, both live credentials")
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
	// 75 days after t0 the credential has ended and every remaining request is
	// past the 30 day grace (requests first), and every remaining client's last
	// activity is older than the 30 day idle window: all five are collected.
	afterCredential := credentialEnd.Add(oauthPurgeGrace).Add(time.Hour)
	require.Equal(t, OAuthPurgeResult{Requests: 2, Clients: 5, DeviceAuthorizations: 4}, f.purge(afterCredential, 500))
	for name, id := range map[string]string{
		"recent-request": withRecentRequest, "live-via-request": liveThroughRequest, "live-via-grant": liveThroughGrant,
		"young": young, "recent-grant": recentGrant,
	} {
		require.Falsef(t, f.clientExists(id), "%s: purged once its credential ended and its activity aged out", name)
	}
	require.Equal(t, 0, f.count("acr.oauth_clients"))
	require.Equal(t, 0, f.count("acr.device_authorizations"))
	require.Equal(t, 0, f.count("acr.oauth_device_grants"))
}

func TestOAuthStore_PurgeExpired_batchIsBounded(t *testing.T) {
	// Given: 5 purgeable requests (each on its own device authorization) and 5 idle clients
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(40 * 24 * time.Hour)
	requestClient := f.client(0xc0, purgeAt.Add(-time.Hour))
	for range 5 {
		f.request(requestClient, f.device(), f.t0)
	}
	for seed := byte(0xc1); seed <= 0xc5; seed++ {
		f.client(seed, f.t0)
	}

	// When / Then: the first call takes exactly a batch from each statement ...
	require.Equal(t, OAuthPurgeResult{Requests: 2, Clients: 2, DeviceAuthorizations: 2}, f.purge(purgeAt, 2))

	// ... and no call ever takes more than a batch from any statement (a deleted
	// device authorization also takes its own request with it, so the request
	// count of later calls is not fixed), until nothing is left.
	total := OAuthPurgeResult{Requests: 2, Clients: 2, DeviceAuthorizations: 2}
	for range 10 {
		result := f.purge(purgeAt, 2)
		require.LessOrEqual(t, result.Requests, 2)
		require.LessOrEqual(t, result.Clients, 2)
		require.LessOrEqual(t, result.DeviceAuthorizations, 2)
		total.Clients += result.Clients
		total.DeviceAuthorizations += result.DeviceAuthorizations
		if result == (OAuthPurgeResult{}) {
			break
		}
	}
	require.Equal(t, 5, total.Clients)
	require.Equal(t, 5, total.DeviceAuthorizations)
	require.Equal(t, OAuthPurgeResult{}, f.purge(purgeAt, 2), "the purge reaches a fixed point")
	require.Equal(t, 0, f.count("acr.oauth_authorization_requests"))
	require.Equal(t, 0, f.count("acr.device_authorizations"))
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
	for _, windows := range [][2]time.Duration{
		{0, oauthPurgeIdle}, {-time.Hour, oauthPurgeIdle}, {oauthPurgeGrace, 0}, {oauthPurgeGrace, -time.Hour},
		{oauthPurgeIdle - time.Second, oauthPurgeIdle}, {time.Hour, 2 * time.Hour},
	} {
		_, err = f.store.PurgeExpired(f.ctx, now, windows[0], windows[1], 10)
		require.ErrorIs(t, err, storage.ErrInvalidOAuthClient, "grace=%v idle=%v", windows[0], windows[1])
	}
	_, err = f.store.PurgeExpired(f.ctx, now, oauthPurgeGrace, oauthPurgeGrace, 10)
	require.NoError(t, err, "an idle window equal to the request grace is valid (it is the default)")
	_, err = f.store.PurgeExpired(f.ctx, now, 2*time.Hour, time.Hour, 10)
	require.NoError(t, err, "a request grace longer than the idle window is valid")
}

// With the default windows (request grace == client idle window == 30d) "idle
// for 30 days" is exact: request rows are the only record of when a client
// last asked to authorize, and they are kept as long as the idle window.
func TestOAuthStore_PurgeExpired_defaultWindowsMeasureIdlenessExactly(t *testing.T) {
	// Given: a purge 100 days after t0 with grace == idle == 30d
	f := newOAuthPurgeFixture(t)
	const window = 30 * 24 * time.Hour
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	credentialEnd := purgeAt.Add(24 * time.Hour)
	day := 24 * time.Hour

	// registered 40 days ago, last request 2 days ago (e.g. a client authorizing
	// again, or one whose credential has no traceable link): must survive
	usedRecently := f.client(0xe1, purgeAt.Add(-40*day))
	recent := f.request(usedRecently, f.device(), purgeAt.Add(-2*day))
	// registered 40 days ago, last request 29 days ago: still inside the window
	insideWindow := f.client(0xe2, purgeAt.Add(-40*day))
	inside := f.request(insideWindow, f.device(), purgeAt.Add(-29*day))
	// registered 40 days ago, last request 31 days ago, no live credential: idle, purged
	idleSince31 := f.client(0xe3, purgeAt.Add(-40*day))
	old := f.request(idleSince31, f.device(), purgeAt.Add(-31*day))
	// same last request age, but its credential is still live: kept with its request
	liveDevice, _ := f.redeemedDevice(&credentialEnd)
	liveClient := f.client(0xe4, purgeAt.Add(-40*day))
	live := f.request(liveClient, liveDevice, purgeAt.Add(-31*day))
	// registered 10 days ago, no request yet: young, kept
	young := f.client(0xe5, purgeAt.Add(-10*day))

	// When
	result, err := f.store.PurgeExpired(f.ctx, purgeAt, window, window, 500)
	require.NoError(t, err)

	// Then
	require.Equal(t, OAuthPurgeResult{Requests: 1, Clients: 1, DeviceAuthorizations: 1}, result)
	require.True(t, f.clientExists(usedRecently), "a client with a request 2 days ago is not idle")
	require.True(t, f.requestExists(recent))
	require.True(t, f.clientExists(insideWindow), "a request 29 days ago is inside the 30 day window")
	require.True(t, f.requestExists(inside))
	require.False(t, f.requestExists(old), "a request older than the window is purged")
	require.False(t, f.clientExists(idleSince31), "last request 31 days ago and no live credential: idle")
	require.True(t, f.requestExists(live), "the request linking a client to a live credential is kept")
	require.True(t, f.clientExists(liveClient))
	require.True(t, f.clientExists(young))
}
