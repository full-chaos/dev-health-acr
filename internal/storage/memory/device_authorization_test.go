package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

const deviceAuthorizationTestOrgID = "11111111-1111-1111-1111-111111111111"

type deviceAuthorizationFixture struct {
	now         time.Time
	store       storage.DeviceAuthorizationStore
	credentials *storage.CredentialLifecycle
	audit       *AuditStore
	service     *auth.Service
}

func TestDeviceAuthorizationStore_Create_usesFixedLifetimeAndHashedValues(t *testing.T) {
	// Given
	fixture := newDeviceAuthorizationFixture(t)
	deviceCode := "a-secure-32-byte-device-code-value"
	userCode := "BCDFGHJK"

	// When
	record, err := fixture.store.Create(context.Background(), storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode(deviceCode),
		UserCodeHash:   storage.HashUserCode(userCode),
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStatePending, record.State)
	require.Equal(t, fixture.now.Add(storage.DeviceAuthorizationTTL), record.ExpiresAt)
	require.Equal(t, storage.DeviceAuthorizationPollInterval, record.PollInterval)
	require.Equal(t, storage.CredentialIssuanceProvenanceDeviceAuthorization, record.IssuanceProvenance)
	require.NotEqual(t, deviceCode, record.DeviceCodeHash.String())
	require.NotEqual(t, userCode, record.UserCodeHash.String())
}

func TestDeviceAuthorizationStore_Redeem_transitionsApprovedAndCreatesOneCredential(t *testing.T) {
	// Given
	fixture := newDeviceAuthorizationFixture(t)
	record := fixture.createPending(t, "approved")
	grant := validDeviceAuthorizationGrant()
	approved, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.NoError(t, err)
	_, duplicateApprovalErr := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.ErrorIs(t, duplicateApprovalErr, storage.ErrDeviceAuthorizationConflict)
	prepared := fixture.prepareCredential(t, grant)

	// When
	credential, err := fixture.store.Redeem(context.Background(), record.DeviceCodeHash, prepared.StorageInput())

	// Then
	require.NoError(t, err)
	require.Equal(t, grant.OrgID, credential.OrgID)
	require.Equal(t, grant.RepositoryScopes, credential.RepositoryScopes)
	require.Equal(t, grant.Scopes, credential.Scopes)
	require.NotNil(t, approved.ApprovedAt)
	redeemed, err := fixture.store.GetByDeviceCodeHash(context.Background(), record.DeviceCodeHash)
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStateRedeemed, redeemed.State)
	require.NotNil(t, redeemed.RedeemedAt)
	issued, err := prepared.Complete(credential)
	require.NoError(t, err)
	stored, err := fixture.credentials.FindByTokenHash(context.Background(), auth.HashToken(issued.Token))
	require.NoError(t, err)
	require.Equal(t, credential.CredentialID, stored.CredentialID)
	events := fixture.audit.Events()
	require.Len(t, events, 1)
	require.Equal(t, storage.AuditActionCredentialCreated, events[0].Action)
	require.Equal(t, string(storage.CredentialIssuanceProvenanceDeviceAuthorization), events[0].Metadata["issuance_provenance"])
}

func TestDeviceAuthorizationStore_Redeem_rejectsCredentialOutsideApprovedGrant(t *testing.T) {
	// Given
	fixture := newDeviceAuthorizationFixture(t)
	pending := fixture.createPending(t, "grant-mismatch")
	grant := validDeviceAuthorizationGrant()
	_, err := fixture.store.Approve(context.Background(), pending.UserCodeHash, grant)
	require.NoError(t, err)
	widerGrant := grant
	widerGrant.RepositoryScopes = []string{"full-chaos/other"}
	prepared := fixture.prepareCredential(t, widerGrant)

	// When
	_, err = fixture.store.Redeem(context.Background(), pending.DeviceCodeHash, prepared.StorageInput())

	// Then
	require.ErrorIs(t, err, storage.ErrInvalidDeviceAuthorization)
	credentials, listErr := fixture.credentials.List(context.Background(), grant.OrgID)
	require.NoError(t, listErr)
	require.Empty(t, credentials)
	require.Empty(t, fixture.audit.Events())
	unchanged, getErr := fixture.store.GetByDeviceCodeHash(context.Background(), pending.DeviceCodeHash)
	require.NoError(t, getErr)
	require.Equal(t, storage.DeviceAuthorizationStateApproved, unchanged.State)
}

func TestDeviceAuthorizationStore_TerminalStates_absorbMalformedTransitions(t *testing.T) {
	// Given
	fixture := newDeviceAuthorizationFixture(t)
	pending := fixture.createPending(t, "denied")
	prepared := fixture.prepareCredential(t, validDeviceAuthorizationGrant())

	// When
	_, redeemPendingErr := fixture.store.Redeem(context.Background(), pending.DeviceCodeHash, prepared.StorageInput())
	denied, denyErr := fixture.store.Deny(context.Background(), pending.UserCodeHash)
	_, approveDeniedErr := fixture.store.Approve(context.Background(), pending.UserCodeHash, validDeviceAuthorizationGrant())
	_, denyAgainErr := fixture.store.Deny(context.Background(), pending.UserCodeHash)

	// Then
	require.ErrorIs(t, redeemPendingErr, storage.ErrDeviceAuthorizationConflict)
	require.NoError(t, denyErr)
	require.Equal(t, storage.DeviceAuthorizationStateDenied, denied.State)
	require.ErrorIs(t, approveDeniedErr, storage.ErrDeviceAuthorizationConflict)
	require.ErrorIs(t, denyAgainErr, storage.ErrDeviceAuthorizationConflict)
	unchanged, err := fixture.store.GetByDeviceCodeHash(context.Background(), pending.DeviceCodeHash)
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStateDenied, unchanged.State)
}

func TestDeviceAuthorizationStore_Expiry_isTerminalAtTenMinutes(t *testing.T) {
	// Given
	fixture := newDeviceAuthorizationFixture(t)
	pending := fixture.createPending(t, "expired")
	fixture.now = fixture.now.Add(storage.DeviceAuthorizationTTL)

	// When
	_, err := fixture.store.GetByDeviceCodeHash(context.Background(), pending.DeviceCodeHash)

	// Then
	require.ErrorIs(t, err, storage.ErrDeviceAuthorizationExpired)
	var stateErr *storage.DeviceAuthorizationError
	require.ErrorAs(t, err, &stateErr)
	require.Equal(t, storage.DeviceAuthorizationStateExpired, stateErr.State)
	_, approveErr := fixture.store.Approve(context.Background(), pending.UserCodeHash, validDeviceAuthorizationGrant())
	require.ErrorIs(t, approveErr, storage.ErrDeviceAuthorizationExpired)
}

func TestDeviceAuthorizationStore_Poll_persistsIntervalAndRejectsEarlyRepeat(t *testing.T) {
	// Given
	fixture := newDeviceAuthorizationFixture(t)
	pending := fixture.createPending(t, "poll")

	// When
	first, firstErr := fixture.store.Poll(context.Background(), pending.DeviceCodeHash)
	_, earlyErr := fixture.store.Poll(context.Background(), pending.DeviceCodeHash)
	fixture.now = fixture.now.Add(storage.DeviceAuthorizationPollInterval)
	second, secondErr := fixture.store.Poll(context.Background(), pending.DeviceCodeHash)

	// Then
	require.NoError(t, firstErr)
	require.NotNil(t, first.LastPollAt)
	require.ErrorIs(t, earlyErr, storage.ErrDeviceAuthorizationPollTooSoon)
	var pollErr *storage.DeviceAuthorizationError
	require.ErrorAs(t, earlyErr, &pollErr)
	require.Equal(t, storage.DeviceAuthorizationPollInterval, pollErr.RetryAfter)
	require.NoError(t, secondErr)
	require.Equal(t, fixture.now, *second.LastPollAt)
}

func TestDeviceAuthorizationStore_ConcurrentRedeem_createsExactlyOneCredentialAndAudit(t *testing.T) {
	// Given
	fixture := newDeviceAuthorizationFixture(t)
	pending := fixture.createPending(t, "concurrent")
	grant := validDeviceAuthorizationGrant()
	_, err := fixture.store.Approve(context.Background(), pending.UserCodeHash, grant)
	require.NoError(t, err)
	prepared := fixture.prepareCredential(t, grant)
	start := make(chan struct{})
	errorsByWorker := make(chan error, 2)
	var workers sync.WaitGroup

	// When
	for range 2 {
		workers.Go(func() {
			<-start
			_, redeemErr := fixture.store.Redeem(context.Background(), pending.DeviceCodeHash, prepared.StorageInput())
			errorsByWorker <- redeemErr
		})
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)

	// Then
	var successes, conflicts int
	for redeemErr := range errorsByWorker {
		switch {
		case redeemErr == nil:
			successes++
		case errors.Is(redeemErr, storage.ErrDeviceAuthorizationConflict):
			conflicts++
		default:
			t.Fatalf("Redeem() error = %v", redeemErr)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	credentials, err := fixture.credentials.List(context.Background(), grant.OrgID)
	require.NoError(t, err)
	require.Len(t, credentials, 1)
	require.Len(t, fixture.audit.Events(), 1)
}

func newDeviceAuthorizationFixture(t *testing.T) *deviceAuthorizationFixture {
	t.Helper()
	fixture := &deviceAuthorizationFixture{now: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
	fixture.audit = NewAuditStore()
	var err error
	fixture.credentials, err = NewCredentialStoreWithOptions(CredentialStoreOptions{Audit: fixture.audit, Now: func() time.Time { return fixture.now }})
	require.NoError(t, err)
	fixture.store, err = NewDeviceAuthorizationStore(DeviceAuthorizationStoreOptions{
		Credentials: fixture.credentials,
		Now:         func() time.Time { return fixture.now },
	})
	require.NoError(t, err)
	fixture.service, err = auth.NewService(fixture.credentials, auth.ServiceOptions{
		Now:                  func() time.Time { return fixture.now },
		GenerateToken:        auth.GenerateToken,
		GenerateCredentialID: auth.GenerateCredentialID,
	})
	require.NoError(t, err)
	return fixture
}

func (f *deviceAuthorizationFixture) createPending(t *testing.T, suffix string) storage.DeviceAuthorization {
	t.Helper()
	record, err := f.store.Create(context.Background(), storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("device-code-" + suffix),
		UserCodeHash:   storage.HashUserCode("USER-" + suffix),
	})
	require.NoError(t, err)
	return record
}

func (f *deviceAuthorizationFixture) prepareCredential(t *testing.T, grant storage.DeviceAuthorizationGrant) auth.PreparedCredential {
	t.Helper()
	prepared, err := f.service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "device login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: grant.Scopes, CreatedBy: grant.ApprovingSubject,
		ExpiresAt: pointerTime(f.now.Add(30 * 24 * time.Hour)),
	})
	require.NoError(t, err)
	return prepared
}

func validDeviceAuthorizationGrant() storage.DeviceAuthorizationGrant {
	return storage.DeviceAuthorizationGrant{
		OrgID: deviceAuthorizationTestOrgID, RepositoryScopes: []string{"full-chaos/dev-health-acr"},
		Scopes: []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, ApprovingSubject: "user_1",
		ApprovingAuthenticationMethod: storage.AuthenticationMethodWebAssertion,
	}
}

func pointerTime(value time.Time) *time.Time { return &value }
