package compose_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"github.com/stretchr/testify/require"
)

// TestAcrDbInit_RuntimeRoleRunsTheOAuthPurge executes the OAuth purge as the
// RESTRICTED runtime role acr-db-init.sh's runtime-acl mode builds (the role
// acr-api connects as), not as the owner every other purge test uses: the
// purge's two statements need SELECT on the two OAuth tables plus
// device_authorizations, client_credentials and oauth_device_grants, FOR
// UPDATE (so UPDATE) and DELETE on the two OAuth tables, and a missing
// privilege anywhere fails acr-api's startup purge. Rows are seeded through
// the migration (owner) connection with the real stores.
func TestAcrDbInit_RuntimeRoleRunsTheOAuthPurge(t *testing.T) {
	h := bootstrapACRDBInit(t)
	ctx := h.ctx
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	purgeAt := t0.Add(40 * 24 * time.Hour)
	credentialEnd := t0.Add(45 * 24 * time.Hour)

	audit, err := storagepostgres.NewAuditStore(h.migrationDB)
	require.NoError(t, err)
	devices, err := storagepostgres.NewDeviceAuthorizationStoreWithOptions(h.migrationDB, audit, storagepostgres.DeviceAuthorizationStoreOptions{Now: func() time.Time { return t0 }})
	require.NoError(t, err)
	seed, err := storagepostgres.NewOAuthStoreWithOptions(h.migrationDB, storagepostgres.OAuthStoreOptions{Now: func() time.Time { return t0 }})
	require.NoError(t, err)

	clientID := func(seedByte byte) string {
		var random [16]byte
		for i := range random {
			random[i] = seedByte
		}
		return storage.NewDynamicOAuthClientID(random)
	}
	serial := 0
	newDevice := func(redeem bool) storage.DeviceCodeHash {
		serial++
		device, err := devices.Create(ctx, storage.DeviceAuthorizationCreateInput{
			DeviceCodeHash: storage.HashDeviceCode(fmt.Sprintf("runtime-purge-device-%d", serial)),
			UserCodeHash:   storage.HashUserCode(fmt.Sprintf("RTPURGE%d", serial)),
		})
		require.NoError(t, err)
		if !redeem {
			return device.DeviceCodeHash
		}
		grant := storage.DeviceAuthorizationGrant{
			OrgID: "11111111-1111-1111-1111-111111111111", RepositoryScopes: []string{"full-chaos/dev-health-acr"},
			Scopes: []string{"context:read"}, ApprovingSubject: "22222222-2222-2222-2222-222222222222",
			ApprovingAuthenticationMethod: storage.AuthenticationMethodWebAssertion,
		}
		_, err = devices.Approve(ctx, device.UserCodeHash, grant)
		require.NoError(t, err)
		_, err = devices.Redeem(ctx, device.DeviceCodeHash, storage.CredentialCreateInput{
			CredentialID: fmt.Sprintf("cred_runtime_purge_%d", serial), OrgID: grant.OrgID, Name: "runtime purge",
			TokenPrefix: fmt.Sprintf("fcacr_runtimepurge%d", serial), TokenHash: fmt.Sprintf("%064x", serial),
			RepositoryScopes: grant.RepositoryScopes, Scopes: grant.Scopes, ActorID: grant.ApprovingSubject,
			ExpiresAt: &credentialEnd, Resource: "https://example.com/resource",
		})
		require.NoError(t, err)
		return device.DeviceCodeHash
	}
	register := func(id string) {
		_, err := seed.RegisterClient(ctx, storage.OAuthClient{ClientID: id, ClientName: "runtime purge", RedirectURIs: []string{"https://example.com/callback"}, CreatedAt: t0})
		require.NoError(t, err)
	}
	request := func(id string, device storage.DeviceCodeHash) storage.OAuthSecretHash {
		serial++
		handle := storage.HashOAuthSecret(fmt.Sprintf("runtime-purge-handle-%d", serial))
		_, err := seed.CreateAuthorizationRequest(ctx, storage.OAuthAuthorizationRequest{
			HandleHash: handle, DeviceCodeHash: device, ClientID: id, ClientKind: storage.OAuthClientKindDynamic,
			RedirectURI: "https://example.com/callback", CodeChallenge: strings.Repeat("A", 43),
			Resource: "https://example.com/resource", Scope: "context:read", State: "s",
			CreatedAt: t0, ExpiresAt: t0.Add(storage.DeviceAuthorizationTTL),
		})
		require.NoError(t, err)
		return handle
	}

	expiredClient, liveRequestClient, idleClient, liveGrantClient, deadGrantClient := clientID(0xd1), clientID(0xd2), clientID(0xd3), clientID(0xd4), clientID(0xd5)
	for _, id := range []string{expiredClient, liveRequestClient, idleClient, liveGrantClient, deadGrantClient} {
		register(id)
	}
	expiredRequest := request(expiredClient, newDevice(false))
	liveRequest := request(liveRequestClient, newDevice(true))
	grantDevice := newDevice(true)
	_, err = seed.CreateDeviceGrant(ctx, storage.OAuthDeviceGrant{
		DeviceCodeHash: grantDevice, ClientID: liveGrantClient, ClientKind: storage.OAuthClientKindDynamic,
		Resource: "https://example.com/resource", Scope: "context:read", CreatedAt: t0, ExpiresAt: t0.Add(storage.DeviceAuthorizationTTL),
	})
	require.NoError(t, err)
	// CHAOS-7229: a device authorization whose credential was revoked, with a
	// device grant behind it. The runtime role has no DELETE on
	// acr.oauth_device_grants; the purge takes the grant only through the
	// device authorization's ON DELETE CASCADE.
	deadGrantDevice := newDevice(true)
	deadCredential := fmt.Sprintf("cred_runtime_purge_%d", serial)
	_, err = h.migrationDB.ExecContext(ctx, `UPDATE acr.client_credentials SET revoked_at = $2 WHERE credential_id = $1`, deadCredential, t0.Add(time.Hour))
	require.NoError(t, err)
	_, err = seed.CreateDeviceGrant(ctx, storage.OAuthDeviceGrant{
		DeviceCodeHash: deadGrantDevice, ClientID: deadGrantClient, ClientKind: storage.OAuthClientKindDynamic,
		Resource: "https://example.com/resource", Scope: "context:read", CreatedAt: t0, ExpiresAt: t0.Add(storage.DeviceAuthorizationTTL),
	})
	require.NoError(t, err)

	// When: the purge runs as the restricted runtime role
	runtimeStore, err := storagepostgres.NewOAuthStore(h.runtimeDB)
	require.NoError(t, err)
	_, err = h.runtimeDB.ExecContext(ctx, `DELETE FROM acr.oauth_device_grants WHERE device_code_hash = $1`, deadGrantDevice.String())
	require.Error(t, err, "the runtime role must NOT be able to delete a device grant directly")
	require.Contains(t, err.Error(), "permission denied")
	result, err := runtimeStore.PurgeExpired(ctx, purgeAt, 30*24*time.Hour, 30*24*time.Hour, 500)

	// Then: it ran (no permission error) and deleted exactly the eligible rows
	require.NoError(t, err, "the runtime role must hold every privilege the OAuth purge statements need")
	require.Equal(t, storagepostgres.OAuthPurgeResult{Requests: 1, Clients: 3, DeviceAuthorizations: 2}, result)
	// The remaining-eligible probe the purge tick logs (CHAOS-7249) needs no
	// privilege beyond the SELECTs the purge already holds.
	remaining, err := runtimeStore.CountPurgeRemaining(ctx, purgeAt, 30*24*time.Hour, 30*24*time.Hour, 500)
	require.NoError(t, err, "the runtime role must be able to run the remaining-eligible probe")
	require.Equal(t, storagepostgres.OAuthPurgeRemaining{}, remaining)
	exists := func(table, column, value string) bool {
		var count int
		require.NoError(t, h.migrationDB.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM acr.%s WHERE %s = $1`, table, column), value).Scan(&count))
		return count == 1
	}
	require.False(t, exists("oauth_authorization_requests", "handle_hash", expiredRequest.String()))
	require.True(t, exists("oauth_authorization_requests", "handle_hash", liveRequest.String()), "a request backing a live credential is kept")
	require.False(t, exists("oauth_clients", "client_id", expiredClient), "its request is gone, so the idle client goes in the same call")
	require.False(t, exists("oauth_clients", "client_id", idleClient))
	require.True(t, exists("oauth_clients", "client_id", liveRequestClient))
	require.True(t, exists("oauth_clients", "client_id", liveGrantClient), "a client whose device grant holds a live credential is kept")
	require.False(t, exists("oauth_clients", "client_id", deadGrantClient), "a client whose only credential was revoked is idle")

	// CHAOS-7229: through the runtime role's DELETE on device_authorizations alone
	require.False(t, exists("device_authorizations", "device_code_hash", deadGrantDevice.String()), "the revoked credential's device authorization is purged")
	require.False(t, exists("oauth_device_grants", "device_code_hash", deadGrantDevice.String()), "and its device grant goes with it by cascade, without the role holding DELETE on it")
	require.True(t, exists("oauth_device_grants", "device_code_hash", grantDevice.String()), "a device grant behind a live credential is kept")
	require.True(t, exists("device_authorizations", "device_code_hash", grantDevice.String()))
}
