package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestDeviceAuthorizationStore_persistsOnlyHashesAndRedeemsExactlyOnce(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	deviceCode := "a-secure-32-byte-device-code-value"
	userCode := "BCDFGHJK"
	created, err := store.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode(deviceCode), UserCodeHash: storage.HashUserCode(userCode),
	})
	require.NoError(t, err)
	grant := postgresDeviceAuthorizationGrant()
	_, err = store.Approve(ctx, created.UserCodeHash, grant)
	require.NoError(t, err)
	prepared, err := service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "device login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: grant.Scopes, CreatedBy: grant.ApprovingSubject,
		ExpiresAt: postgresPointerTime(now.Add(30 * 24 * time.Hour)),
	})
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan postgresRedemptionResult, 2)
	var workers sync.WaitGroup

	// When
	for range 2 {
		workers.Go(func() {
			<-start
			credential, redeemErr := store.Redeem(ctx, created.DeviceCodeHash, prepared.StorageInput())
			results <- postgresRedemptionResult{credential: credential, err: redeemErr}
		})
	}
	close(start)
	workers.Wait()
	close(results)

	// Then
	var success contractsv1.ClientCredential
	var successes, conflicts int
	for result := range results {
		switch {
		case result.err == nil:
			success = result.credential
			successes++
		case errors.Is(result.err, storage.ErrDeviceAuthorizationConflict):
			conflicts++
		default:
			t.Fatalf("Redeem() error = %v", result.err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	issued := prepared.Issued(success)
	var storedHash, rowJSON string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT token_hash FROM acr.client_credentials WHERE credential_id = $1", success.CredentialID).Scan(&storedHash))
	require.Equal(t, auth.HashToken(issued.Token), storedHash)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT row_to_json(device)::text FROM acr.device_authorizations AS device WHERE device_code_hash = $1", created.DeviceCodeHash.String()).Scan(&rowJSON))
	require.NotContains(t, rowJSON, deviceCode)
	require.NotContains(t, rowJSON, userCode)
	assertCredentialAndAuditCounts(t, ctx, db, 1, 1)
	var state, provenance string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state, issuance_provenance FROM acr.device_authorizations WHERE device_code_hash = $1", created.DeviceCodeHash.String()).Scan(&state, &provenance))
	require.Equal(t, string(storage.DeviceAuthorizationStateRedeemed), state)
	require.Equal(t, string(storage.CredentialIssuanceProvenanceDeviceAuthorization), provenance)
}

func TestDeviceAuthorizationStore_enforcesPollCASExpiryAndTerminalAbsorption(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	pending, err := store.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("postgres-expiry-device"),
		UserCodeHash:   storage.HashUserCode("PGEXPIRE"),
	})
	require.NoError(t, err)

	// When
	_, firstPollErr := store.Poll(ctx, pending.DeviceCodeHash)
	_, earlyPollErr := store.Poll(ctx, pending.DeviceCodeHash)
	now = now.Add(storage.DeviceAuthorizationTTL)
	_, expiryErr := store.GetByDeviceCodeHash(ctx, pending.DeviceCodeHash)
	_, approveErr := store.Approve(ctx, pending.UserCodeHash, postgresDeviceAuthorizationGrant())
	denied, denyCreateErr := store.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("postgres-denied-device"),
		UserCodeHash:   storage.HashUserCode("PGDENIED"),
	})
	require.NoError(t, denyCreateErr)
	now = now.Add(-storage.DeviceAuthorizationTTL)
	_, denyErr := store.Deny(ctx, denied.UserCodeHash)
	_, approveDeniedErr := store.Approve(ctx, denied.UserCodeHash, postgresDeviceAuthorizationGrant())

	// Then
	require.NoError(t, firstPollErr)
	require.ErrorIs(t, earlyPollErr, storage.ErrDeviceAuthorizationPollTooSoon)
	require.ErrorIs(t, expiryErr, storage.ErrDeviceAuthorizationExpired)
	require.ErrorIs(t, approveErr, storage.ErrDeviceAuthorizationExpired)
	require.NoError(t, denyErr)
	require.ErrorIs(t, approveDeniedErr, storage.ErrDeviceAuthorizationConflict)
	var state string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state FROM acr.device_authorizations WHERE device_code_hash = $1", pending.DeviceCodeHash.String()).Scan(&state))
	require.Equal(t, string(storage.DeviceAuthorizationStateExpired), state)
}

func TestDeviceAuthorizationStore_rollsBackRedeemWhenAuditFails(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	pending, err := store.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("postgres-rollback-device"),
		UserCodeHash:   storage.HashUserCode("PGROLLBK"),
	})
	require.NoError(t, err)
	grant := postgresDeviceAuthorizationGrant()
	_, err = store.Approve(ctx, pending.UserCodeHash, grant)
	require.NoError(t, err)
	prepared, err := service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "device login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: grant.Scopes, CreatedBy: grant.ApprovingSubject,
	})
	require.NoError(t, err)
	audit.GenerateID = func() (string, error) { return "", errAuditUnavailable }

	// When
	_, err = store.Redeem(ctx, pending.DeviceCodeHash, prepared.StorageInput())

	// Then
	require.ErrorIs(t, err, errAuditUnavailable)
	var state string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state FROM acr.device_authorizations WHERE device_code_hash = $1", pending.DeviceCodeHash.String()).Scan(&state))
	require.Equal(t, string(storage.DeviceAuthorizationStateApproved), state)
	assertCredentialAndAuditCounts(t, ctx, db, 0, 0)
}

type postgresRedemptionResult struct {
	credential contractsv1.ClientCredential
	err        error
}

func postgresDeviceAuthorizationGrant() storage.DeviceAuthorizationGrant {
	return storage.DeviceAuthorizationGrant{
		OrgID: credentialTestOrgID, RepositoryScopes: []string{"full-chaos/dev-health-acr"},
		Scopes: []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, ApprovingSubject: credentialTestActorID,
		ApprovingAuthenticationMethod: storage.AuthenticationMethodWebAssertion,
	}
}

func postgresPointerTime(value time.Time) *time.Time { return &value }
