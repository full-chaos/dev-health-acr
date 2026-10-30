package memory

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-12123: a browser-class credential takes its web session from the
// locked authorization row, as it takes its approving user.

func (f *deviceAuthorizationFixture) browserInput(t *testing.T, grant storage.DeviceAuthorizationGrant, sessionID string) storage.CredentialCreateInput {
	t.Helper()
	expires := f.now.Add(auth.BrowserCredentialLifetime)
	family := f.now.Add(auth.BrowserFamilyLifetime)
	prepared, err := f.service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "browser login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: []string{auth.ScopeContextRead}, CreatedBy: grant.ApprovingSubject, ExpiresAt: &expires,
		Resource: "https://acr.example.test", ClientID: auth.DevBrowserClientID,
		ClientClass: storage.CredentialClientClassBrowser, FamilyID: "fam_1", FamilyExpiresAt: &family,
	})
	require.NoError(t, err)
	input := prepared.StorageInput()
	input.SessionID = sessionID
	return input
}

func TestBrowserSession_redeemTakesTheSessionFromTheRecord(t *testing.T) {
	fixture := newDeviceAuthorizationFixture(t)
	record := fixture.createPending(t, "browser-session")
	grant := validDeviceAuthorizationGrant()
	grant.ApprovingSessionID = "sid_1"
	approved, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.NoError(t, err)
	require.Equal(t, "sid_1", approved.ApprovingSessionID)

	credential, err := fixture.store.Redeem(context.Background(), record.DeviceCodeHash, fixture.browserInput(t, grant, ""))

	require.NoError(t, err)
	require.Equal(t, string(storage.CredentialClientClassBrowser), credential.Provenance.ClientClass)
	require.Equal(t, "sid_1", credential.Provenance.SessionID)
	require.Equal(t, "fam_1", credential.Provenance.FamilyID)
	require.Equal(t, grant.ApprovingSubject, credential.Provenance.ApprovingUserID)
}

func TestBrowserSession_redeemRefusesAForgedOrMissingSession(t *testing.T) {
	for name, tc := range map[string]struct{ recorded, presented string }{
		"a caller session that differs": {"sid_1", "sid_2"},
		"an approval with no session":   {"", ""},
		"a caller session, none stored": {"", "sid_1"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newDeviceAuthorizationFixture(t)
			record := fixture.createPending(t, "browser-forged")
			grant := validDeviceAuthorizationGrant()
			grant.ApprovingSessionID = tc.recorded
			_, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
			require.NoError(t, err)

			_, err = fixture.store.Redeem(context.Background(), record.DeviceCodeHash, fixture.browserInput(t, grant, tc.presented))

			require.ErrorIs(t, err, storage.ErrInvalidDeviceAuthorization)
			stored, getErr := fixture.store.GetByDeviceCodeHash(context.Background(), record.DeviceCodeHash)
			require.NoError(t, getErr)
			require.Equal(t, storage.DeviceAuthorizationStateApproved, stored.State)
		})
	}
}

func TestBrowserSession_aSessionOnANonBrowserCredentialIsRefused(t *testing.T) {
	fixture := newDeviceAuthorizationFixture(t)
	record := fixture.createPending(t, "native-session")
	grant := validDeviceAuthorizationGrant()
	grant.ApprovingSessionID = "sid_1"
	_, err := fixture.store.Approve(context.Background(), record.UserCodeHash, grant)
	require.NoError(t, err)
	input := fixture.prepareCredential(t, grant).StorageInput()
	input.SessionID = "sid_1"

	_, err = fixture.store.Redeem(context.Background(), record.DeviceCodeHash, input)

	require.ErrorIs(t, err, storage.ErrInvalidCredentialInput)
}
