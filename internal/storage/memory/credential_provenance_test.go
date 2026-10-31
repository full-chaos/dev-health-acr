package memory

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-12083: every issuance path writes its client class and provenance,
// the approving user comes from the authorization record, and a rotation
// copies the source's provenance unchanged.

func TestCredentialProvenance_cliCreateIsOrgBearerWithNoApprover(t *testing.T) {
	fixture := newDeviceAuthorizationFixture(t)

	issued, err := fixture.service.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: deviceAuthorizationTestOrgID, Name: "operator token", RepositoryScopes: []string{"full-chaos/dev-health-acr"},
		Scopes: []string{auth.ScopeContextRead}, CreatedBy: "operator@example.test",
		ExpiresAt: pointerTime(fixture.now.Add(time.Hour)),
	})

	require.NoError(t, err)
	require.Equal(t, contractsv1.CredentialProvenance{ClientClass: string(storage.CredentialClientClassOrgBearer)}, issued.Credential.Provenance)
	stored := fixture.storedByToken(t, issued.Token)
	require.Equal(t, issued.Credential.Provenance, stored.Provenance)
}

func TestCredentialProvenance_nativeDeviceRedeemTakesApproverFromRecord(t *testing.T) {
	fixture := newDeviceAuthorizationFixture(t)
	record := fixture.createPending(t, "native-provenance")
	grant := validDeviceAuthorizationGrant()
	_, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.NoError(t, err)
	prepared := fixture.prepareCredential(t, grant)

	credential, err := fixture.store.Redeem(context.Background(), record.DeviceCodeHash, prepared.StorageInput())

	require.NoError(t, err)
	require.Equal(t, contractsv1.CredentialProvenance{
		ClientClass: string(storage.CredentialClientClassNativeDevice), ApprovingUserID: grant.ApprovingSubject,
	}, credential.Provenance)
	issued, err := prepared.Complete(credential)
	require.NoError(t, err)
	require.Equal(t, credential.Provenance, fixture.storedByToken(t, issued.Token).Provenance)
}

func TestCredentialProvenance_oauthRedeemRecordsClientAndApprover(t *testing.T) {
	fixture := newDeviceAuthorizationFixture(t)
	record := fixture.createPending(t, "oauth-provenance")
	grant := validDeviceAuthorizationGrant()
	_, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.NoError(t, err)
	prepared, err := fixture.service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "oauth login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: []string{auth.ScopeContextRead}, CreatedBy: grant.ApprovingSubject,
		ExpiresAt: pointerTime(fixture.now.Add(time.Hour)),
		Resource:  "https://mcp.example.test/mcp", ClientID: "acrc_0123456789abcdef0123456789abcdef",
	})
	require.NoError(t, err)

	credential, err := fixture.store.Redeem(context.Background(), record.DeviceCodeHash, prepared.StorageInput())

	require.NoError(t, err)
	require.Equal(t, contractsv1.CredentialProvenance{
		ClientClass: string(storage.CredentialClientClassOAuthUser), ApprovingUserID: grant.ApprovingSubject,
		ClientID: "acrc_0123456789abcdef0123456789abcdef",
	}, credential.Provenance)
	// The class decision and its inputs are on the credential_created audit row.
	events := fixture.audit.Events()
	require.NotEmpty(t, events)
	created := events[len(events)-1]
	require.Equal(t, storage.AuditActionCredentialCreated, created.Action)
	require.Equal(t, "oauth_user", created.Metadata["client_class"])
	require.Equal(t, grant.ApprovingSubject, created.Metadata["approving_user_id"])
	require.Equal(t, "acrc_0123456789abcdef0123456789abcdef", created.Metadata["client_id"])
}

func TestCredentialProvenance_redeemRefusesACallerApproverThatDiffersFromTheRecord(t *testing.T) {
	fixture := newDeviceAuthorizationFixture(t)
	record := fixture.createPending(t, "forged-approver")
	grant := validDeviceAuthorizationGrant()
	_, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.NoError(t, err)
	input := fixture.prepareCredential(t, grant).StorageInput()
	input.ApprovingUserID = "someone_else"

	_, err = fixture.store.Redeem(context.Background(), record.DeviceCodeHash, input)

	require.ErrorIs(t, err, storage.ErrInvalidDeviceAuthorization)
	stored, getErr := fixture.store.GetByDeviceCodeHash(context.Background(), record.DeviceCodeHash)
	require.NoError(t, getErr)
	require.Equal(t, storage.DeviceAuthorizationStateApproved, stored.State)
}

func TestCredentialProvenance_rotationCopiesTheSourceProvenance(t *testing.T) {
	fixture := newDeviceAuthorizationFixture(t)
	record := fixture.createPending(t, "rotate-provenance")
	grant := validDeviceAuthorizationGrant()
	_, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.NoError(t, err)
	source, err := fixture.store.Redeem(context.Background(), record.DeviceCodeHash, fixture.prepareCredential(t, grant).StorageInput())
	require.NoError(t, err)

	// The rotating actor is the credential itself, as a self-rotation names
	// it: it must not become the approving user.
	rotated, err := fixture.service.Rotate(context.Background(), auth.RotateCredentialRequest{
		OrgID: source.OrgID, CredentialID: source.CredentialID, CreatedBy: source.CredentialID,
		ExpiresAt: pointerTime(fixture.now.Add(time.Hour)),
	})

	require.NoError(t, err)
	require.Equal(t, source.Provenance, rotated.Credential.Provenance)
	require.Equal(t, grant.ApprovingSubject, fixture.storedByToken(t, rotated.Token).Provenance.ApprovingUserID)
}

func TestCredentialProvenance_cloneSharesNoFamilyExpiry(t *testing.T) {
	expires := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	original := contractsv1.ClientCredential{Provenance: contractsv1.CredentialProvenance{FamilyExpiresAt: &expires}}

	cloned := cloneCredential(original)
	*cloned.Provenance.FamilyExpiresAt = expires.Add(time.Hour)

	require.Equal(t, time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC), *original.Provenance.FamilyExpiresAt)
}

func (f *deviceAuthorizationFixture) storedByToken(t *testing.T, token string) contractsv1.ClientCredential {
	t.Helper()
	stored, err := f.credentials.FindByTokenHash(context.Background(), auth.HashToken(token))
	require.NoError(t, err)
	return stored
}
