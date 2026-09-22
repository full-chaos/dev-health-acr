package memory

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

type oauthFixture struct {
	now   time.Time
	store *OAuthStore
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	fixture := &oauthFixture{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	fixture.store = NewOAuthStore(func() time.Time { return fixture.now })
	return fixture
}

func validOAuthClient(clientID string) storage.OAuthClient {
	return storage.OAuthClient{
		ClientID:     clientID,
		ClientName:   "test client",
		RedirectURIs: []string{"https://example.com/callback"},
		CreatedAt:    time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
}

func dynamicOAuthClientID(seed byte) string {
	var random [16]byte
	for i := range random {
		random[i] = seed
	}
	return storage.NewDynamicOAuthClientID(random)
}

func validOAuthAuthorizationRequest(now time.Time, deviceCodeSeed, handleSeed string) storage.OAuthAuthorizationRequest {
	return storage.OAuthAuthorizationRequest{
		HandleHash:     storage.HashOAuthSecret(handleSeed),
		DeviceCodeHash: storage.HashDeviceCode(deviceCodeSeed),
		ClientID:       dynamicOAuthClientID(0xab),
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

func validOAuthDeviceGrant(now time.Time, deviceCodeSeed string) storage.OAuthDeviceGrant {
	return storage.OAuthDeviceGrant{
		DeviceCodeHash: storage.HashDeviceCode(deviceCodeSeed),
		ClientID:       dynamicOAuthClientID(0xab),
		ClientKind:     storage.OAuthClientKindDynamic,
		Resource:       "https://example.com/resource",
		Scope:          "context:read",
		CreatedAt:      now,
		ExpiresAt:      now.Add(storage.DeviceAuthorizationTTL),
	}
}

func TestOAuthStore_CreateDeviceGrant_duplicateIsConflict(t *testing.T) {
	fixture := newOAuthFixture(t)
	grant := validOAuthDeviceGrant(fixture.now, "device-1")
	_, err := fixture.store.CreateDeviceGrant(context.Background(), grant)
	require.NoError(t, err)

	_, err = fixture.store.CreateDeviceGrant(context.Background(), grant)
	require.ErrorIs(t, err, storage.ErrConflict)
}

func TestOAuthStore_CreateDeviceGrant_rejectsInvalid(t *testing.T) {
	fixture := newOAuthFixture(t)
	invalid := validOAuthDeviceGrant(fixture.now, "device-invalid")
	invalid.Resource = "not-a-url"
	_, err := fixture.store.CreateDeviceGrant(context.Background(), invalid)
	require.Error(t, err)
}

func TestOAuthStore_GetDeviceGrant_missingIsNotFound(t *testing.T) {
	fixture := newOAuthFixture(t)
	_, err := fixture.store.GetDeviceGrant(context.Background(), storage.HashDeviceCode("never-created"))
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestOAuthStore_GetDeviceGrant_returnsWhatWasStored(t *testing.T) {
	fixture := newOAuthFixture(t)
	grant := validOAuthDeviceGrant(fixture.now, "device-2")
	_, err := fixture.store.CreateDeviceGrant(context.Background(), grant)
	require.NoError(t, err)

	got, err := fixture.store.GetDeviceGrant(context.Background(), grant.DeviceCodeHash)
	require.NoError(t, err)
	require.Equal(t, grant, got)
}

func TestOAuthStore_RegisterClient_duplicateIsConflict(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	client := validOAuthClient(dynamicOAuthClientID(0x01))
	_, err := fixture.store.RegisterClient(context.Background(), client)
	require.NoError(t, err)

	// When
	_, err = fixture.store.RegisterClient(context.Background(), client)

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
}

func TestOAuthStore_GetClient_unknownIsNotFound(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)

	// When
	_, err := fixture.store.GetClient(context.Background(), dynamicOAuthClientID(0x02))

	// Then
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestOAuthStore_RegisterClient_invalidInputIsInvalidError(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	client := validOAuthClient(dynamicOAuthClientID(0x03))
	client.RedirectURIs = []string{"not-a-valid-redirect-uri"}

	// When
	_, err := fixture.store.RegisterClient(context.Background(), client)

	// Then
	require.ErrorIs(t, err, storage.ErrInvalidOAuthClient)
}

// TestOAuthStore_RegisterClient_noRedirectURIsIsValid pins the CHAOS-6233
// device-only-client case: a client with zero redirect_uris (registering for
// the RFC 8628 device-code grant only, which has no redirect step) is a
// valid stored client, not an error -- the requirement of at least one
// redirect_uri belongs to OAuthService.Register (only when the client also
// wants authorization_code), not to this storage-layer validation.
func TestOAuthStore_RegisterClient_noRedirectURIsIsValid(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	client := validOAuthClient(dynamicOAuthClientID(0x04))
	client.RedirectURIs = nil

	// When
	registered, err := fixture.store.RegisterClient(context.Background(), client)

	// Then
	require.NoError(t, err)
	require.Empty(t, registered.RedirectURIs)
}

func TestOAuthStore_CreateAuthorizationRequest_invalidInputIsInvalidError(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	request := validOAuthAuthorizationRequest(fixture.now, "device-invalid", "handle-invalid")
	request.RedirectURI = "not-a-uri"

	// When
	_, err := fixture.store.CreateAuthorizationRequest(context.Background(), request)

	// Then
	require.ErrorIs(t, err, storage.ErrInvalidOAuthAuthorizationRequest)
}

func TestOAuthStore_GetAuthorizationRequest_unknownIsNotFound(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)

	// When
	_, err := fixture.store.GetAuthorizationRequest(context.Background(), storage.HashOAuthSecret("nonexistent-handle"))

	// Then
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestOAuthStore_IssueAuthorizationCode_twiceIsConflict(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	request := validOAuthAuthorizationRequest(fixture.now, "device-issue-twice", "handle-issue-twice")
	created, err := fixture.store.CreateAuthorizationRequest(context.Background(), request)
	require.NoError(t, err)
	_, err = fixture.store.IssueAuthorizationCode(context.Background(), created.HandleHash, storage.HashOAuthSecret("code-1"), fixture.now.Add(storage.OAuthAuthorizationCodeTTL))
	require.NoError(t, err)

	// When
	_, err = fixture.store.IssueAuthorizationCode(context.Background(), created.HandleHash, storage.HashOAuthSecret("code-2"), fixture.now.Add(storage.OAuthAuthorizationCodeTTL))

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
}

func TestOAuthStore_IssueAuthorizationCode_expiredIsConflict(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	request := validOAuthAuthorizationRequest(fixture.now, "device-issue-expired", "handle-issue-expired")
	created, err := fixture.store.CreateAuthorizationRequest(context.Background(), request)
	require.NoError(t, err)
	fixture.now = fixture.now.Add(storage.OAuthAuthorizationCodeTTL + time.Second)

	// When
	_, err = fixture.store.IssueAuthorizationCode(context.Background(), created.HandleHash, storage.HashOAuthSecret("code-after-expiry"), fixture.now.Add(time.Minute))

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
}

func TestOAuthStore_IssueAuthorizationCode_unknownHandleIsNotFound(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)

	// When
	_, err := fixture.store.IssueAuthorizationCode(context.Background(), storage.HashOAuthSecret("unknown-handle"), storage.HashOAuthSecret("some-code"), fixture.now.Add(time.Minute))

	// Then
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestOAuthStore_ConsumeAuthorizationCode_twiceIsUnavailable(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	request := validOAuthAuthorizationRequest(fixture.now, "device-consume-twice", "handle-consume-twice")
	created, err := fixture.store.CreateAuthorizationRequest(context.Background(), request)
	require.NoError(t, err)
	code := storage.HashOAuthSecret("consume-twice-code")
	_, err = fixture.store.IssueAuthorizationCode(context.Background(), created.HandleHash, code, fixture.now.Add(storage.OAuthAuthorizationCodeTTL))
	require.NoError(t, err)
	_, err = fixture.store.ConsumeAuthorizationCode(context.Background(), code)
	require.NoError(t, err)

	// When
	_, err = fixture.store.ConsumeAuthorizationCode(context.Background(), code)

	// Then
	require.ErrorIs(t, err, storage.ErrOAuthAuthorizationCodeUnavailable)
}

func TestOAuthStore_ConsumeAuthorizationCode_expiredIsUnavailable(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	request := validOAuthAuthorizationRequest(fixture.now, "device-consume-expired", "handle-consume-expired")
	created, err := fixture.store.CreateAuthorizationRequest(context.Background(), request)
	require.NoError(t, err)
	code := storage.HashOAuthSecret("consume-expired-code")
	_, err = fixture.store.IssueAuthorizationCode(context.Background(), created.HandleHash, code, fixture.now.Add(time.Minute))
	require.NoError(t, err)
	fixture.now = fixture.now.Add(2 * time.Minute)

	// When
	_, err = fixture.store.ConsumeAuthorizationCode(context.Background(), code)

	// Then
	require.ErrorIs(t, err, storage.ErrOAuthAuthorizationCodeUnavailable)
}

func TestOAuthStore_ConsumeAuthorizationCode_unknownIsUnavailable(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)

	// When
	_, err := fixture.store.ConsumeAuthorizationCode(context.Background(), storage.HashOAuthSecret("never-issued-code"))

	// Then
	require.ErrorIs(t, err, storage.ErrOAuthAuthorizationCodeUnavailable)
}

// TestOAuthStore_ConsumeAuthorizationCode_concurrentConsumersExactlyOneSucceeds
// proves the map-backed store serializes ConsumeAuthorizationCode under its
// mutex: 16 goroutines race to consume the SAME code, and exactly one must
// observe success.
func TestOAuthStore_ConsumeAuthorizationCode_concurrentConsumersExactlyOneSucceeds(t *testing.T) {
	// Given
	fixture := newOAuthFixture(t)
	request := validOAuthAuthorizationRequest(fixture.now, "device-concurrent", "handle-concurrent")
	created, err := fixture.store.CreateAuthorizationRequest(context.Background(), request)
	require.NoError(t, err)
	code := storage.HashOAuthSecret("concurrent-code")
	_, err = fixture.store.IssueAuthorizationCode(context.Background(), created.HandleHash, code, fixture.now.Add(storage.OAuthAuthorizationCodeTTL))
	require.NoError(t, err)

	const workers = 16
	start := make(chan struct{})
	var successes, failures int32
	var wg sync.WaitGroup
	var mu sync.Mutex

	// When
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := fixture.store.ConsumeAuthorizationCode(context.Background(), code)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else {
				require.ErrorIs(t, err, storage.ErrOAuthAuthorizationCodeUnavailable)
				failures++
			}
		}()
	}
	close(start)
	wg.Wait()

	// Then
	require.EqualValues(t, 1, successes)
	require.EqualValues(t, workers-1, failures)
}
