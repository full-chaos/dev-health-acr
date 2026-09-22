package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func validOAuthClientPG(clientID string) storage.OAuthClient {
	return storage.OAuthClient{
		ClientID:     clientID,
		ClientName:   "pg test client",
		RedirectURIs: []string{"https://example.com/callback"},
		CreatedAt:    time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
}

func dynamicOAuthClientIDPG(seed byte) string {
	var random [16]byte
	for i := range random {
		random[i] = seed
	}
	return storage.NewDynamicOAuthClientID(random)
}

func validOAuthAuthorizationRequestPG(now time.Time, deviceCodeHash storage.DeviceCodeHash, handleSeed string) storage.OAuthAuthorizationRequest {
	return storage.OAuthAuthorizationRequest{
		HandleHash:     storage.HashOAuthSecret(handleSeed),
		DeviceCodeHash: deviceCodeHash,
		ClientID:       dynamicOAuthClientIDPG(0xcd),
		ClientKind:     storage.OAuthClientKindDynamic,
		RedirectURI:    "https://example.com/callback",
		CodeChallenge:  strings.Repeat("A", 43),
		Resource:       "https://example.com/resource",
		Scope:          "context:read",
		State:          "state-value",
		CreatedAt:      now,
		ExpiresAt:      now.Add(storage.OAuthAuthorizationCodeTTL),
	}
}

func validOAuthDeviceGrantPG(now time.Time, deviceCodeHash storage.DeviceCodeHash) storage.OAuthDeviceGrant {
	return storage.OAuthDeviceGrant{
		DeviceCodeHash: deviceCodeHash,
		ClientID:       dynamicOAuthClientIDPG(0xef),
		ClientKind:     storage.OAuthClientKindDynamic,
		Resource:       "https://example.com/resource",
		Scope:          "context:read",
		CreatedAt:      now,
		ExpiresAt:      now.Add(storage.DeviceAuthorizationTTL),
	}
}

func TestOAuthStore_DeviceGrantLifecycle(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	deviceStore, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	device, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("device-grant-lifecycle"),
		UserCodeHash:   storage.HashUserCode("DEVGRANT"),
	})
	require.NoError(t, err)
	grant := validOAuthDeviceGrantPG(now, device.DeviceCodeHash)

	// When
	created, err := store.CreateDeviceGrant(ctx, grant)
	fetched, getErr := store.GetDeviceGrant(ctx, grant.DeviceCodeHash)
	_, duplicateErr := store.CreateDeviceGrant(ctx, grant)
	_, unknownErr := store.GetDeviceGrant(ctx, storage.HashDeviceCode("device-grant-unknown"))

	// Then
	require.NoError(t, err)
	require.Equal(t, grant.ClientID, created.ClientID)
	require.NoError(t, getErr)
	require.Equal(t, grant.DeviceCodeHash, fetched.DeviceCodeHash)
	require.Equal(t, grant.ClientID, fetched.ClientID)
	require.Equal(t, grant.ClientKind, fetched.ClientKind)
	require.Equal(t, grant.Resource, fetched.Resource)
	require.Equal(t, grant.Scope, fetched.Scope)
	require.True(t, grant.CreatedAt.Equal(fetched.CreatedAt))
	require.True(t, grant.ExpiresAt.Equal(fetched.ExpiresAt))
	require.ErrorIs(t, duplicateErr, storage.ErrConflict)
	require.ErrorIs(t, unknownErr, storage.ErrNotFound)
}

func TestOAuthStore_ClientLifecycle(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewOAuthStore(db)
	require.NoError(t, err)
	client := validOAuthClientPG(dynamicOAuthClientIDPG(0x11))

	// When
	registered, err := store.RegisterClient(ctx, client)
	require.NoError(t, err)
	fetched, getErr := store.GetClient(ctx, client.ClientID)
	_, duplicateErr := store.RegisterClient(ctx, client)
	_, unknownErr := store.GetClient(ctx, dynamicOAuthClientIDPG(0x12))

	// Then
	require.Equal(t, client.ClientID, registered.ClientID)
	require.NoError(t, getErr)
	require.Equal(t, client.RedirectURIs, fetched.RedirectURIs)
	require.ErrorIs(t, duplicateErr, storage.ErrConflict)
	require.ErrorIs(t, unknownErr, storage.ErrNotFound)
}

// TestOAuthStore_RegisterClient_deviceOnlyClientWithNoRedirectURIsPersists
// pins CHAOS-6233's device-only DCR path all the way through the REAL
// Postgres INSERT, not just app-layer validation: a client with zero
// redirect_uris (grant_types=[device_code] only, no authorization_code
// support) is exactly what a nil (never-appended-to) Go []string produces,
// and encoding/json marshals that as the JSON literal `null` -- which fails
// migration 0040's `CHECK (jsonb_typeof(redirect_uris) = 'array')` on
// acr.oauth_clients (constraint name oauth_clients_redirect_uris_check).
// Before the fix in RegisterClient (marshalJSONStringArray, credentials.go),
// this test fails RED with exactly that Postgres error; the fix makes it
// pass by never letting a nil slice reach the marshal call.
func TestOAuthStore_RegisterClient_deviceOnlyClientWithNoRedirectURIsPersists(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewOAuthStore(db)
	require.NoError(t, err)
	var noRedirectURIs []string // deliberately nil, not []string{} -- the shape Register() actually builds for a device-only client
	client := storage.OAuthClient{
		ClientID:     dynamicOAuthClientIDPG(0x13),
		ClientName:   "device-only pg test client",
		RedirectURIs: noRedirectURIs,
		CreatedAt:    time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
	}
	require.Nil(t, client.RedirectURIs)

	// When
	registered, err := store.RegisterClient(ctx, client)

	// Then: the INSERT actually reached Postgres and the row exists -- a
	// constraint violation on oauth_clients_redirect_uris_check is exactly
	// the failure this pins closed.
	require.NoError(t, err, "expected the row to persist, not violate oauth_clients_redirect_uris_check")
	require.NotNil(t, registered.RedirectURIs, "the returned client must reflect what Postgres actually stored ([]), not a bare nil")
	require.Empty(t, registered.RedirectURIs)

	fetched, err := store.GetClient(ctx, client.ClientID)
	require.NoError(t, err)
	require.NotNil(t, fetched.RedirectURIs)
	require.Empty(t, fetched.RedirectURIs)
}

func TestOAuthStore_AuthorizationRequestIssueAndConsumeLifecycle(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	deviceStore, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	device, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("lifecycle-device"),
		UserCodeHash:   storage.HashUserCode("LIFECYCL"),
	})
	require.NoError(t, err)
	request := validOAuthAuthorizationRequestPG(now, device.DeviceCodeHash, "lifecycle-handle")

	// When
	created, err := store.CreateAuthorizationRequest(ctx, request)
	require.NoError(t, err)
	fetched, err := store.GetAuthorizationRequest(ctx, created.HandleHash)
	require.NoError(t, err)
	require.Nil(t, fetched.CodeHash)

	code := storage.HashOAuthSecret("lifecycle-code")
	issued, err := store.IssueAuthorizationCode(ctx, created.HandleHash, code, now.Add(storage.OAuthAuthorizationCodeTTL))
	require.NoError(t, err)
	require.NotNil(t, issued.CodeHash)
	require.Equal(t, code, *issued.CodeHash)

	consumed, err := store.ConsumeAuthorizationCode(ctx, code)
	require.NoError(t, err)

	// Then
	require.NotNil(t, consumed.ConsumedAt)
	require.Equal(t, request.Resource, consumed.Resource)
	require.Equal(t, request.ClientID, consumed.ClientID)

	var consumedAtColumn *time.Time
	require.NoError(t, db.QueryRowContext(ctx, "SELECT consumed_at FROM acr.oauth_authorization_requests WHERE handle_hash = $1", created.HandleHash.String()).Scan(&consumedAtColumn))
	require.NotNil(t, consumedAtColumn)

	// Consuming again must fail -- unavailable, indistinguishable from unknown/expired.
	_, err = store.ConsumeAuthorizationCode(ctx, code)
	require.ErrorIs(t, err, storage.ErrOAuthAuthorizationCodeUnavailable)
}

func TestOAuthStore_IssueAuthorizationCode_conflictsAndNotFound(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	deviceStore, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)

	// unknown handle
	_, err = store.IssueAuthorizationCode(ctx, storage.HashOAuthSecret("unknown-handle-pg"), storage.HashOAuthSecret("code-a"), now.Add(time.Minute))
	require.ErrorIs(t, err, storage.ErrNotFound)

	// twice
	deviceTwice, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("issue-twice-device"),
		UserCodeHash:   storage.HashUserCode("ISSUTWIC"),
	})
	require.NoError(t, err)
	requestTwice, err := store.CreateAuthorizationRequest(ctx, validOAuthAuthorizationRequestPG(now, deviceTwice.DeviceCodeHash, "issue-twice-handle"))
	require.NoError(t, err)
	_, err = store.IssueAuthorizationCode(ctx, requestTwice.HandleHash, storage.HashOAuthSecret("issue-twice-code-1"), now.Add(storage.OAuthAuthorizationCodeTTL))
	require.NoError(t, err)
	_, err = store.IssueAuthorizationCode(ctx, requestTwice.HandleHash, storage.HashOAuthSecret("issue-twice-code-2"), now.Add(storage.OAuthAuthorizationCodeTTL))
	require.ErrorIs(t, err, storage.ErrConflict)

	// expired
	deviceExpired, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("issue-expired-device"),
		UserCodeHash:   storage.HashUserCode("ISSUEXPI"),
	})
	require.NoError(t, err)
	expiredRequest := validOAuthAuthorizationRequestPG(now, deviceExpired.DeviceCodeHash, "issue-expired-handle")
	requestExpired, err := store.CreateAuthorizationRequest(ctx, expiredRequest)
	require.NoError(t, err)
	laterStore, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now.Add(storage.OAuthAuthorizationCodeTTL + time.Second) }})
	require.NoError(t, err)
	_, err = laterStore.IssueAuthorizationCode(ctx, requestExpired.HandleHash, storage.HashOAuthSecret("issue-expired-code"), now.Add(2*storage.OAuthAuthorizationCodeTTL))
	require.ErrorIs(t, err, storage.ErrConflict)
}

func TestOAuthStore_ConsumeAuthorizationCode_unavailableCases(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	deviceStore, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)

	// unknown code
	_, err = store.ConsumeAuthorizationCode(ctx, storage.HashOAuthSecret("never-issued-pg-code"))
	require.ErrorIs(t, err, storage.ErrOAuthAuthorizationCodeUnavailable)

	// expired code
	device, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("consume-expired-device"),
		UserCodeHash:   storage.HashUserCode("CNSMEXPI"),
	})
	require.NoError(t, err)
	request, err := store.CreateAuthorizationRequest(ctx, validOAuthAuthorizationRequestPG(now, device.DeviceCodeHash, "consume-expired-handle"))
	require.NoError(t, err)
	code := storage.HashOAuthSecret("consume-expired-code")
	_, err = store.IssueAuthorizationCode(ctx, request.HandleHash, code, now.Add(time.Minute))
	require.NoError(t, err)
	laterStore, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now.Add(2 * time.Minute) }})
	require.NoError(t, err)
	_, err = laterStore.ConsumeAuthorizationCode(ctx, code)
	require.ErrorIs(t, err, storage.ErrOAuthAuthorizationCodeUnavailable)
}

// TestOAuthStore_ConsumeAuthorizationCode_concurrentConsumersExactlyOneSucceeds
// proves the single atomic UPDATE ... RETURNING serializes correctly under
// real concurrent Postgres traffic: 16 goroutines race to consume the SAME
// code, and exactly one observes success.
func TestOAuthStore_ConsumeAuthorizationCode_concurrentConsumersExactlyOneSucceeds(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	deviceStore, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	device, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("concurrent-device"),
		UserCodeHash:   storage.HashUserCode("CONCURR1"),
	})
	require.NoError(t, err)
	request, err := store.CreateAuthorizationRequest(ctx, validOAuthAuthorizationRequestPG(now, device.DeviceCodeHash, "concurrent-handle"))
	require.NoError(t, err)
	code := storage.HashOAuthSecret("concurrent-pg-code")
	_, err = store.IssueAuthorizationCode(ctx, request.HandleHash, code, now.Add(storage.OAuthAuthorizationCodeTTL))
	require.NoError(t, err)

	const workers = 16
	start := make(chan struct{})
	var successes, failures int32
	var mu sync.Mutex
	var wg sync.WaitGroup

	// When
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, consumeErr := store.ConsumeAuthorizationCode(ctx, code)
			mu.Lock()
			defer mu.Unlock()
			if consumeErr == nil {
				successes++
				return
			}
			if !errors.Is(consumeErr, storage.ErrOAuthAuthorizationCodeUnavailable) {
				t.Errorf("ConsumeAuthorizationCode() error = %v, want nil or ErrOAuthAuthorizationCodeUnavailable", consumeErr)
			}
			failures++
		}()
	}
	close(start)
	wg.Wait()

	// Then
	require.EqualValues(t, 1, successes)
	require.EqualValues(t, workers-1, failures)
}

// TestCredentialStore_ResourceRoundTripsThroughDeviceRedeem proves the
// off-wire Resource binding survives a real Postgres INSERT (via device
// authorization redemption) and comes back out of FindByTokenHash.
func TestCredentialStore_ResourceRoundTripsThroughDeviceRedeem(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	deviceStore, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: time.Now})
	require.NoError(t, err)
	device, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("resource-roundtrip-device"),
		UserCodeHash:   storage.HashUserCode("RESROUND"),
	})
	require.NoError(t, err)
	grant := postgresDeviceAuthorizationGrant()
	_, err = deviceStore.Approve(ctx, device.UserCodeHash, grant)
	require.NoError(t, err)

	// When
	redeemed, err := deviceStore.Redeem(ctx, device.DeviceCodeHash, storage.CredentialCreateInput{
		CredentialID: "cred_resource_roundtrip", OrgID: grant.OrgID, Name: "resource roundtrip",
		TokenPrefix: "fcacr_resourceroundtrip", TokenHash: strings.Repeat("d", 64),
		RepositoryScopes: grant.RepositoryScopes, Scopes: grant.Scopes, ActorID: grant.ApprovingSubject,
		Resource: "https://example.com/resource",
	})
	require.NoError(t, err)

	// Then
	require.Equal(t, "https://example.com/resource", redeemed.Resource)
	found, err := credentials.FindByTokenHash(ctx, strings.Repeat("d", 64))
	require.NoError(t, err)
	require.Equal(t, "https://example.com/resource", found.Resource)
}

// TestCredentialStore_RotationInheritsSourceResourceIntegration mirrors the
// memory adapter's equivalent test against real Postgres: a rotated
// successor's resource comes from the SOURCE row, not the rotation request.
func TestCredentialStore_RotationInheritsSourceResourceIntegration(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	sourceInput := credentialCreateRequestWithResource("resource-rotation-source", "https://example.com/resource")
	source, err := store.CreateCredential(ctx, sourceInput)
	require.NoError(t, err)

	// When
	replacement, err := store.RotateCredential(ctx, storage.CredentialRotationInput{
		OrgID: source.OrgID, SourceCredentialID: source.CredentialID, ActorID: credentialTestActorID,
		Replacement: storage.CredentialRotationReplacement{
			CredentialID: "cred_resource_rotation_replacement", Name: "resource rotation replacement",
			TokenPrefix: "fcacr_abcdefghij", TokenHash: strings.Repeat("e", 64),
			RepositoryScopes: sourceInput.RepositoryScopes, Scopes: sourceInput.Scopes, Overlap: 0, Immediate: true,
		},
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, source.Resource, replacement.Resource)
}

func credentialCreateRequestWithResource(suffix, resource string) storage.CredentialCreateInput {
	return storage.CredentialCreateInput{
		CredentialID: "cred_" + suffix, OrgID: credentialTestOrgID, Name: "credential " + suffix,
		TokenPrefix: "fcacr_abcdefghij", TokenHash: strings.Repeat("f", 64),
		RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{"context:read"},
		ActorID: credentialTestActorID, Resource: resource,
	}
}

// TestOAuthStore_AuthorizationRequestForAMetadataDocumentClient: a request
// whose client_id is a client ID metadata document URL is stored and read
// back with client_kind metadata_document (migration 0041 widened the CHECK).
func TestOAuthStore_AuthorizationRequestForAMetadataDocumentClient(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	deviceStore, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	device, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("cimd-device"),
		UserCodeHash:   storage.HashUserCode("CIMDCODE"),
	})
	require.NoError(t, err)
	request := validOAuthAuthorizationRequestPG(now, device.DeviceCodeHash, "cimd-handle")
	request.ClientKind = storage.OAuthClientKindMetadataDocument
	request.ClientID = "https://client.example.test/oauth/client.json"

	created, err := store.CreateAuthorizationRequest(ctx, request)
	require.NoError(t, err)
	fetched, err := store.GetAuthorizationRequest(ctx, created.HandleHash)
	require.NoError(t, err)
	require.Equal(t, storage.OAuthClientKindMetadataDocument, fetched.ClientKind)
	require.Equal(t, request.ClientID, fetched.ClientID)
}
