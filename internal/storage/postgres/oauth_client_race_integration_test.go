package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// The idle-client purge and the routes that store a request or a device grant
// for a dynamic client race: a purge that commits between the route resolving
// the client and storing the row must never leave a row pointing at a purged
// client. These tests pin each half of the mechanism against real PostgreSQL.

// A request or device grant for a dynamic client whose row is gone stores
// nothing and says so; a metadata-document client has no row and is unaffected.
func TestOAuthStore_createRefusesAPurgedClient(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	gone := dynamicOAuthClientIDPG(0xf1)

	request := validOAuthAuthorizationRequestPG(f.t0, f.device(), "gone-client-request")
	request.ClientID = gone
	_, err := f.store.CreateAuthorizationRequest(f.ctx, request)
	require.ErrorIs(t, err, storage.ErrOAuthClientGone)
	require.ErrorIs(t, err, storage.ErrNotFound, "it wraps ErrNotFound: callers answer it like an unknown client")

	grant := validOAuthDeviceGrantPG(f.t0, f.device())
	grant.ClientID = gone
	_, err = f.store.CreateDeviceGrant(f.ctx, grant)
	require.ErrorIs(t, err, storage.ErrOAuthClientGone)

	require.Equal(t, 0, f.count("acr.oauth_authorization_requests"))
	require.Equal(t, 0, f.count("acr.oauth_device_grants"))

	// Registered: both store.
	registered := f.client(0xf2, f.t0)
	request = validOAuthAuthorizationRequestPG(f.t0, f.device(), "registered-client-request")
	request.ClientID = registered
	_, err = f.store.CreateAuthorizationRequest(f.ctx, request)
	require.NoError(t, err)
	grant = validOAuthDeviceGrantPG(f.t0, f.device())
	grant.ClientID = registered
	_, err = f.store.CreateDeviceGrant(f.ctx, grant)
	require.NoError(t, err)
	require.Equal(t, 1, f.count("acr.oauth_authorization_requests"))
	require.Equal(t, 1, f.count("acr.oauth_device_grants"))
}

// A client whose row an in-flight /authorize holds FOR KEY SHARE (between its
// own client check and commit) is skipped by the purge, not deleted and not
// waited on.
func TestOAuthStore_purgeSkipsAClientAnInFlightInsertHasLocked(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	locked := f.client(0xf3, f.t0)
	idle := f.client(0xf4, f.t0)

	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var one int
	require.NoError(t, tx.QueryRowContext(f.ctx, `SELECT 1 FROM acr.oauth_clients WHERE client_id = $1 FOR KEY SHARE`, locked).Scan(&one))

	// A bounded context: a purge that waited on the locked row would hang here.
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	result, err := f.store.PurgeExpired(ctx, purgeAt, oauthPurgeGrace, oauthPurgeIdle, 500)
	require.NoError(t, err, "the purge must skip a client an in-flight insert holds, not wait for it")

	require.Equal(t, OAuthPurgeResult{Clients: 1}, result, "only the unlocked idle client is purged; the locked one is skipped without waiting")
	require.True(t, f.clientExists(locked))
	require.False(t, f.clientExists(idle))
	require.NoError(t, tx.Commit())
}

// A purge that locked the client first (and is about to delete it) makes a
// concurrent request for that client wait for the purge to finish, then refuse
// it: the insert's client check takes the row lock, it does not just read the row.
func TestOAuthStore_createWaitsForAPurgeThatLockedTheClientFirst(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	clientID := f.client(0xf9, f.t0)
	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var locked string
	require.NoError(t, tx.QueryRowContext(f.ctx, `SELECT client_id FROM acr.oauth_clients WHERE client_id = $1 FOR UPDATE`, clientID).Scan(&locked))
	_, err = tx.ExecContext(f.ctx, `DELETE FROM acr.oauth_clients WHERE client_id = $1`, clientID)
	require.NoError(t, err)

	request := validOAuthAuthorizationRequestPG(f.t0, f.device(), "waits-for-purge")
	request.ClientID = clientID
	done := make(chan error, 1)
	go func() {
		_, err := f.store.CreateAuthorizationRequest(f.ctx, request)
		done <- err
	}()
	select {
	case err := <-done:
		require.Failf(t, "the insert did not wait for the purge that holds the client row", "returned early with err=%v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case err := <-done:
		require.ErrorIs(t, err, storage.ErrOAuthClientGone)
	case <-time.After(20 * time.Second):
		require.Fail(t, "the insert never finished after the purge committed")
	}
	require.Equal(t, 0, f.count("acr.oauth_authorization_requests"))
}

// A request that commits after the purge chose its candidates, inside the
// window before it re-checks them, keeps its client: the re-check runs under a
// fresh snapshot and sees the request.
func TestOAuthStore_purgeRechecksCandidatesUnderAFreshSnapshot(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	racing := f.client(0xf5, f.t0)
	idle := f.client(0xf6, f.t0)
	device := f.device()
	f.store.afterClientLock = func() {
		// A raw insert on another connection, as an /authorize that committed
		// after the purge's first snapshot would have left it.
		request := validOAuthAuthorizationRequestPG(purgeAt, device, "racing-request")
		request.ClientID = racing
		request.ExpiresAt = purgeAt.Add(storage.DeviceAuthorizationTTL)
		_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.oauth_authorization_requests (`+oauthAuthorizationRequestColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULL, NULL, NULL, NULL, NULL)`,
			request.HandleHash.String(), request.DeviceCodeHash.String(), request.ClientID, request.ClientKind,
			request.RedirectURI, request.CodeChallenge, request.Resource, request.Scope, request.State, request.CreatedAt, request.ExpiresAt)
		require.NoError(t, err)
	}

	result := f.purge(purgeAt, 500)

	require.Equal(t, OAuthPurgeResult{Clients: 1}, result)
	require.True(t, f.clientExists(racing), "a client whose request committed after the purge's first look must be kept")
	require.False(t, f.clientExists(idle))
}

// purgeAfterGetClient runs a purge right after the wrapped store resolves a
// client, which is exactly the window between OAuthService resolving the client
// and storing the row it resolved the client for.
type purgeAfterGetClient struct {
	storage.OAuthStore
	purge func()
}

func (s purgeAfterGetClient) GetClient(ctx context.Context, clientID string) (storage.OAuthClient, error) {
	client, err := s.OAuthStore.GetClient(ctx, clientID)
	if err == nil {
		s.purge()
	}
	return client, err
}

func TestOAuthService_purgeBetweenResolveAndStoreNeverLeavesADanglingRow(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	audit, err := NewAuditStore(f.db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(f.db, audit)
	require.NoError(t, err)
	credentialService, err := auth.NewService(credentials, auth.ServiceOptions{})
	require.NoError(t, err)
	devices, err := NewDeviceAuthorizationStore(f.db, audit)
	require.NoError(t, err)
	deviceFlow, err := auth.NewDeviceFlowService(devices, credentialService, auth.DeviceFlowOptions{OAuthDeviceGrants: f.store})
	require.NoError(t, err)
	const resource = "https://mcp.example.test/mcp"
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	racing := purgeAfterGetClient{OAuthStore: f.store, purge: func() {
		_, err := f.store.PurgeExpired(f.ctx, purgeAt, oauthPurgeGrace, oauthPurgeIdle, 500)
		require.NoError(t, err)
	}}
	service, err := auth.NewOAuthService(racing, deviceFlow, auth.OAuthConfig{Issuer: "https://acr.example.test", Resources: []string{resource}})
	require.NoError(t, err)

	requireInvalidClient := func(t *testing.T, err error) {
		t.Helper()
		var refusal *auth.OAuthError
		require.True(t, errors.As(err, &refusal), "want an OAuth refusal, got %v", err)
		require.Equal(t, "invalid_client", refusal.Code, "answered exactly like a client that was never registered")
	}

	// /authorize: the client is resolved, purged, then the request is stored
	authorizeClient := f.client(0xf7, f.t0)
	_, err = service.Authorize(f.ctx, auth.OAuthAuthorizeRequest{
		ResponseType: "code", ClientID: authorizeClient, RedirectURI: "https://example.com/callback",
		CodeChallenge: strings.Repeat("A", 43), CodeChallengeMethod: "S256", Resource: resource,
	})
	requireInvalidClient(t, err)
	require.False(t, f.clientExists(authorizeClient), "the purge did run between resolve and store")
	require.Equal(t, 0, f.count("acr.oauth_authorization_requests"), "and no request points at the purged client")

	// /device_authorization: same window
	deviceClient := f.client(0xf8, f.t0)
	_, err = service.StartDeviceAuthorization(f.ctx, auth.OAuthDeviceAuthorizationRequest{ClientID: deviceClient, Resource: resource})
	requireInvalidClient(t, err)
	require.False(t, f.clientExists(deviceClient))
	require.Equal(t, 0, f.count("acr.oauth_device_grants"), "and no device grant points at the purged client")
}
