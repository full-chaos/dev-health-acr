package auth

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func approvedDeviceCode(t *testing.T, fixture *deviceFlowFixture) string {
	t.Helper()
	started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{})
	require.NoError(t, err)
	principal := deviceApprovalPrincipal("*")
	_, err = fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})
	require.NoError(t, err)
	return started.DeviceCode
}

func liveCredentialIDs(t *testing.T, fixture *deviceFlowFixture) []string {
	t.Helper()
	all, err := fixture.credentials.List(context.Background(), deviceFlowTestOrgID)
	require.NoError(t, err)
	var live []string
	for _, credential := range all {
		if credential.RevokedAt == nil {
			live = append(live, credential.CredentialID)
		}
	}
	return live
}

func TestDeviceFlow_Poll_retryAfterLostResponseYieldsOneLiveCredential(t *testing.T) {
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(60))
	code := approvedDeviceCode(t, fixture)

	lost, err := fixture.flow.Poll(context.Background(), code)
	require.NoError(t, err)
	require.True(t, IsTokenShapeValid(lost.Token))
	fixture.now = fixture.now.Add(storage.DeviceAuthorizationPollInterval)
	retried, err := fixture.flow.Poll(context.Background(), code)

	require.NoError(t, err)
	require.True(t, IsTokenShapeValid(retried.Token))
	require.NotEqual(t, lost.Token, retried.Token)
	require.NotEqual(t, lost.Credential.CredentialID, retried.Credential.CredentialID)
	require.Equal(t, []string{retried.Credential.CredentialID}, liveCredentialIDs(t, fixture))
	_, err = fixture.credentials.FindByTokenHash(context.Background(), HashToken(lost.Token))
	require.Error(t, err, "the lost credential no longer authenticates")
	lostRecord, err := fixture.credentials.GetByID(context.Background(), deviceFlowTestOrgID, lost.Credential.CredentialID)
	require.NoError(t, err)
	require.NotNil(t, lostRecord.RevokedAt, "the lost credential is revoked, not deleted")
}

func TestDeviceFlow_Poll_retryAfterAckWindowOrAcknowledgementIsInvalidGrant(t *testing.T) {
	t.Run("window elapsed", func(t *testing.T) {
		fixture := newDeviceFlowFixture(t, deviceFlowRandom(61))
		code := approvedDeviceCode(t, fixture)
		_, err := fixture.flow.Poll(context.Background(), code)
		require.NoError(t, err)

		fixture.now = fixture.now.Add(storage.DeviceCredentialAckWindow)
		_, err = fixture.flow.Poll(context.Background(), code)

		require.ErrorIs(t, err, ErrDeviceInvalidGrant)
	})
	t.Run("acknowledged", func(t *testing.T) {
		fixture := newDeviceFlowFixture(t, deviceFlowRandom(62))
		code := approvedDeviceCode(t, fixture)
		issued, err := fixture.flow.Poll(context.Background(), code)
		require.NoError(t, err)
		principal := storage.Principal{
			AuthenticationMethod: storage.AuthenticationMethodCredential, OrgID: deviceFlowTestOrgID,
			CredentialID: issued.Credential.CredentialID,
		}
		_, err = fixture.flow.AcknowledgeCredential(context.Background(), principal, issued.Credential.CredentialID)
		require.NoError(t, err)

		fixture.now = fixture.now.Add(time.Second)
		_, err = fixture.flow.Poll(context.Background(), code)

		require.ErrorIs(t, err, ErrDeviceInvalidGrant)
		require.Equal(t, []string{issued.Credential.CredentialID}, liveCredentialIDs(t, fixture))
	})
}

func TestDeviceFlow_AcknowledgeCredential_acknowledgesOnlyTheCallersOwnCredential(t *testing.T) {
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(63))
	code := approvedDeviceCode(t, fixture)
	issued, err := fixture.flow.Poll(context.Background(), code)
	require.NoError(t, err)
	own := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, OrgID: deviceFlowTestOrgID, CredentialID: issued.Credential.CredentialID}

	_, err = fixture.flow.AcknowledgeCredential(context.Background(), own, "cred_someone_else_000")
	require.ErrorIs(t, err, ErrDeviceCredentialAckRejected)
	web := own
	web.AuthenticationMethod = storage.AuthenticationMethodWebAssertion
	_, err = fixture.flow.AcknowledgeCredential(context.Background(), web, own.CredentialID)
	require.ErrorIs(t, err, ErrDeviceCredentialAckRejected)
	other := own
	other.OrgID = "00000000-0000-4000-8000-000000000999"
	_, err = fixture.flow.AcknowledgeCredential(context.Background(), other, own.CredentialID)
	require.ErrorIs(t, err, ErrDeviceCredentialAckRejected)
}

func TestDeviceFlow_oauthDeviceGrantCredentialNeedsNoAcknowledgement(t *testing.T) {
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(64))
	code := approvedDeviceCode(t, fixture)
	hash := storage.HashDeviceCode(code)

	issued, err := fixture.flow.PollDeviceGrant(context.Background(), hash, "https://acr.example.test/mcp", []string{ScopeContextRead})
	require.NoError(t, err)
	fixture.now = fixture.now.Add(24 * time.Hour)
	revoked, err := fixture.store.RevokeUnacknowledged(context.Background(), 10)

	require.NoError(t, err)
	require.Zero(t, revoked)
	require.Equal(t, []string{issued.Credential.CredentialID}, liveCredentialIDs(t, fixture))
}
