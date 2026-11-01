package postgres

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// grantedClient runs one real authorization-code sign-in for a new dynamic
// client, in the order the service does it: register at t0, /authorize
// (request row), consent (approve), token exchange (redeem with the client's
// id). The credential ends at credentialEnd.
func (f *oauthPurgeFixture) grantedClient(seed byte, credentialEnd time.Time) (string, storage.OAuthSecretHash) {
	f.t.Helper()
	clientID := f.client(seed, f.t0)
	n := f.next()
	device, err := f.devices.Create(f.ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode(fmt.Sprintf("granted-device-%d", n)),
		UserCodeHash:   storage.HashUserCode(fmt.Sprintf("GRANT%03d", n)),
	})
	require.NoError(f.t, err)
	handle := f.request(clientID, device.DeviceCodeHash, f.t0)
	grant := postgresDeviceAuthorizationGrant()
	_, err = f.devices.Approve(f.ctx, device.UserCodeHash, grant)
	require.NoError(f.t, err)
	_, err = f.devices.Redeem(f.ctx, device.DeviceCodeHash, storage.CredentialCreateInput{
		CredentialID: fmt.Sprintf("cred_granted_%d", n), OrgID: grant.OrgID, Name: "granted fixture",
		TokenPrefix: fmt.Sprintf("fcacr_grant%d", n), TokenHash: fmt.Sprintf("%064x", 1000+n),
		RepositoryScopes: grant.RepositoryScopes, Scopes: grant.Scopes, ActorID: grant.ApprovingSubject,
		ExpiresAt: &credentialEnd, Resource: "https://example.com/resource", ClientID: clientID,
	})
	require.NoError(f.t, err)
	return clientID, handle
}

func (f *oauthPurgeFixture) lastGrantedAt(clientID string) sql.NullTime {
	f.t.Helper()
	var at sql.NullTime
	require.NoError(f.t, f.db.QueryRowContext(f.ctx, `SELECT last_granted_at FROM acr.oauth_clients WHERE client_id = $1`, clientID).Scan(&at))
	return at
}

// CHAOS-12320: a dynamic client that completed a grant is never purged. Its
// 30-day credential (auth.DeviceCredentialLifetime) has no refresh, so the
// next sign-in reuses the stored client_id; purging the client at credential
// expiry left that sign-in at unknown_client. A client that never completed a
// grant keeps the CHAOS-6191 30-day rule.
func TestOAuthStore_PurgeExpired_neverPurgesAGrantedClient(t *testing.T) {
	// Given: one sign-in at t0 with a 30-day credential, and a registration
	// at t0 that never signed in
	f := newOAuthPurgeFixture(t)
	credentialEnd := f.t0.Add(30 * 24 * time.Hour)
	granted, handle := f.grantedClient(0x51, credentialEnd)
	neverGranted := f.client(0x52, f.t0)

	// When / Then: past credential expiry, and far past it
	for _, after := range []time.Duration{-time.Minute, 11 * time.Minute, 200 * 24 * time.Hour, 400 * 24 * time.Hour} {
		f.purge(credentialEnd.Add(after), 100)
		require.Truef(t, f.clientExists(granted), "purge at credential end %+v: a granted client is kept", after)
	}
	require.False(t, f.clientExists(neverGranted), "a never-granted client still goes once it is 30 days old")
	require.False(t, f.requestExists(handle), "the request row still ages out as before")
	marked := f.lastGrantedAt(granted)
	require.Truef(t, marked.Valid && marked.Time.Equal(f.t0), "the token exchange records the grant at its own clock, got %+v", marked)
}

// The grant mark is written in the redeem transaction: a redeem that fails
// after it leaves no mark.
func TestOAuthStore_grantMarkRollsBackWithTheRedeem(t *testing.T) {
	// Given: a client with a request and an approved device authorization
	f := newOAuthPurgeFixture(t)
	audit, err := NewAuditStore(f.db)
	require.NoError(t, err)
	devices, err := NewDeviceAuthorizationStoreWithOptions(f.db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return f.t0 }})
	require.NoError(t, err)
	clientID := f.client(0x53, f.t0)
	device, err := devices.Create(f.ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("granted-rollback-device"),
		UserCodeHash:   storage.HashUserCode("GRANTRB1"),
	})
	require.NoError(t, err)
	f.request(clientID, device.DeviceCodeHash, f.t0)
	grant := postgresDeviceAuthorizationGrant()
	_, err = devices.Approve(f.ctx, device.UserCodeHash, grant)
	require.NoError(t, err)
	audit.GenerateID = func() (string, error) { return "", errAuditUnavailable }
	credentialEnd := f.t0.Add(30 * 24 * time.Hour)

	// When: the audit write after the mark fails
	_, err = devices.Redeem(f.ctx, device.DeviceCodeHash, storage.CredentialCreateInput{
		CredentialID: "cred_granted_rollback", OrgID: grant.OrgID, Name: "granted rollback",
		TokenPrefix: "fcacr_grantrb", TokenHash: fmt.Sprintf("%064x", 4242),
		RepositoryScopes: grant.RepositoryScopes, Scopes: grant.Scopes, ActorID: grant.ApprovingSubject,
		ExpiresAt: &credentialEnd, Resource: "https://example.com/resource", ClientID: clientID,
	})

	// Then
	require.ErrorIs(t, err, errAuditUnavailable)
	require.False(t, f.lastGrantedAt(clientID).Valid, "the mark rolls back with the redeem")
}
