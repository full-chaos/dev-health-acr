package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestDeviceFlow_Start_generatesBoundedCodesAndPersistsOnlyHashes(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(1))

	// When
	started := fixture.start(t)

	// Then
	decoded, err := base64.RawURLEncoding.DecodeString(started.DeviceCode)
	require.NoError(t, err)
	require.Len(t, decoded, deviceCodeBytes)
	require.Len(t, started.UserCode, userCodeLength)
	for _, character := range started.UserCode {
		require.Contains(t, userCodeAlphabet, string(character))
	}
	require.Equal(t, storage.DeviceAuthorizationTTL, started.ExpiresIn)
	require.Equal(t, storage.DeviceAuthorizationPollInterval, started.Interval)
	record, err := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
	require.NoError(t, err)
	require.Equal(t, storage.HashUserCode(started.UserCode), record.UserCodeHash)
	encoded, err := json.Marshal(record)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), started.DeviceCode)
	require.NotContains(t, string(encoded), started.UserCode)
	require.Equal(t, deviceAuthorizationStartRedacted, fmt.Sprint(started))
}

func TestDeviceFlow_Start_retriesHashCollisionsAndFailsBoundedlyWithoutLeakingCodes(t *testing.T) {
	// Given
	seeds := []byte{1, 1, 2}
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(seeds...))
	first := fixture.start(t)

	// When
	second, retryErr := fixture.flow.Start(context.Background())

	// Then
	require.NoError(t, retryErr)
	require.NotEqual(t, first.DeviceCode, second.DeviceCode)
	require.NotEqual(t, first.UserCode, second.UserCode)

	// Given
	constant := newDeviceFlowFixture(t, deviceFlowRandom(repeatedSeed(3, maxDeviceCodeAttempts+1)...))
	colliding := constant.start(t)

	// When
	_, collisionErr := constant.flow.Start(context.Background())

	// Then
	require.ErrorIs(t, collisionErr, ErrDeviceCodeCollision)
	require.NotContains(t, collisionErr.Error(), colliding.DeviceCode)
	require.NotContains(t, collisionErr.Error(), colliding.UserCode)
}

func TestDeviceFlow_Start_redactsRandomSourceFailures(t *testing.T) {
	// Given
	const secret = "fcacr_source_error_must_not_escape"
	fixture := newDeviceFlowFixture(t, readerError{message: secret})

	// When
	started, err := fixture.flow.Start(context.Background())

	// Then
	require.Error(t, err)
	require.Empty(t, started.DeviceCode)
	require.Empty(t, started.UserCode)
	require.NotContains(t, err.Error(), secret)
}

func TestDeviceFlow_UserCodeAlphabet_rejectsConfusableGlyphs(t *testing.T) {
	// Given
	confusable := []string{"0AAAAAAA", "1AAAAAAA", "IAAAAAAA", "OAAAAAAA"}

	// When / Then
	for _, value := range confusable {
		_, ok := normalizeUserCode(value)
		require.False(t, ok, "user code %q was accepted", value)
	}
	for _, character := range []string{"0", "1", "I", "O"} {
		require.NotContains(t, userCodeAlphabet, character)
	}
}

func TestDeviceFlow_Approve_persistsOnlyExactAssertionBoundReadGrant(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(4))
	started := fixture.start(t)
	principal := deviceApprovalPrincipal("full-chaos/dev-health-acr")

	// When
	approved, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStateApproved, approved.State)
	require.Equal(t, principal.OrgID, approved.AuthorizedOrgID)
	require.Equal(t, principal.Subject, approved.ApprovingSubject)
	require.Equal(t, []string{ScopeContextRead, ScopeEvidenceRead}, approved.AuthorizedScopes)
	require.Equal(t, storage.AuthenticationMethodWebAssertion, approved.ApprovingAuthenticationMethod)
	require.Equal(t, storage.CredentialIssuanceProvenanceDeviceAuthorization, approved.IssuanceProvenance)
	_, duplicateErr := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})
	require.ErrorIs(t, duplicateErr, storage.ErrDeviceAuthorizationConflict)
}

func TestDeviceFlow_Approve_acceptsExactRepositorySubsetBoundedByPrincipal(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(5))
	started := fixture.start(t)
	principal := deviceApprovalPrincipal("full-chaos/dev-health-acr", "full-chaos/dev-health-web")
	selected := []string{"full-chaos/dev-health-acr"}

	// When
	approved, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: selected,
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, selected, approved.AuthorizedRepositoryScopes)
}

func TestDeviceFlow_Approve_rejectsMalformedOrWidenedGrantAndLeavesPending(t *testing.T) {
	tests := []struct {
		name         string
		principal    storage.Principal
		repositories []string
	}{
		{name: "machine credential", principal: storage.Principal{
			AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: "cred_1", CredentialID: "cred_1",
			OrgID: deviceFlowTestOrgID, RepositoryScopes: []string{"full-chaos/dev-health-acr"}, Permissions: []string{ScopeContextRead},
		}, repositories: []string{"full-chaos/dev-health-acr"}},
		{name: "mixed assertion permissions", principal: storage.Principal{
			AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user_1", OrgID: deviceFlowTestOrgID,
			RepositoryScopes: []string{"full-chaos/dev-health-acr"}, Permissions: []string{WebAssertionPermissionCredentialIssue, ScopeContextRead},
		}, repositories: []string{"full-chaos/dev-health-acr"}},
		{name: "wider repositories", principal: deviceApprovalPrincipal("full-chaos/dev-health-acr"), repositories: []string{"full-chaos/other"}},
		{name: "wildcard repository", principal: deviceApprovalPrincipal("full-chaos/*"), repositories: []string{"full-chaos/*"}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			fixture := newDeviceFlowFixture(t, deviceFlowRandom(byte(index+10)))
			started := fixture.start(t)

			// When
			_, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
				Principal: test.principal, UserCode: started.UserCode, RepositoryScopes: test.repositories,
			})

			// Then
			require.ErrorIs(t, err, ErrInvalidDeviceFlow)
			require.NotContains(t, err.Error(), started.UserCode)
			record, getErr := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
			require.NoError(t, getErr)
			require.Equal(t, storage.DeviceAuthorizationStatePending, record.State)
		})
	}
}

func TestDeviceFlow_Poll_returnsTypedProtocolStatesAndOneThirtyDayCredential(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(20, 21, 22))
	pending := fixture.start(t)

	// When
	_, pendingErr := fixture.flow.Poll(context.Background(), pending.DeviceCode)
	_, slowDownErr := fixture.flow.Poll(context.Background(), pending.DeviceCode)

	// Then
	requireDevicePollError(t, pendingErr, DevicePollAuthorizationPending)
	slowDown := requireDevicePollError(t, slowDownErr, DevicePollSlowDown)
	require.Equal(t, storage.DeviceAuthorizationPollInterval, slowDown.RetryAfter)

	// Given
	principal := deviceApprovalPrincipal("full-chaos/dev-health-acr")
	fixture.now = fixture.now.Add(storage.DeviceAuthorizationPollInterval)
	_, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: pending.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})
	require.NoError(t, err)
	redeemedAt := fixture.now

	// When
	issued, redeemErr := fixture.flow.Poll(context.Background(), pending.DeviceCode)
	_, repeatedErr := fixture.flow.Poll(context.Background(), pending.DeviceCode)

	// Then
	require.NoError(t, redeemErr)
	require.True(t, IsTokenShapeValid(issued.Token))
	require.Equal(t, []string{ScopeContextRead, ScopeEvidenceRead}, issued.Credential.Scopes)
	require.Equal(t, principal.RepositoryScopes, issued.Credential.RepositoryScopes)
	require.NotNil(t, issued.Credential.ExpiresAt)
	require.Equal(t, redeemedAt.Add(DeviceCredentialLifetime), *issued.Credential.ExpiresAt)
	requireDevicePollError(t, repeatedErr, DevicePollInvalidGrant)
	require.NotContains(t, repeatedErr.Error(), issued.Token)

	// Given
	denied := fixture.start(t)
	_, err = fixture.flow.Deny(context.Background(), DeviceDenialRequest{Principal: principal, UserCode: denied.UserCode})
	require.NoError(t, err)

	// When / Then
	_, deniedErr := fixture.flow.Poll(context.Background(), denied.DeviceCode)
	requireDevicePollError(t, deniedErr, DevicePollAccessDenied)

	// Given
	expired := fixture.start(t)
	fixture.now = fixture.now.Add(storage.DeviceAuthorizationTTL)

	// When / Then
	_, expiredErr := fixture.flow.Poll(context.Background(), expired.DeviceCode)
	requireDevicePollError(t, expiredErr, DevicePollExpiredToken)
	_, malformedErr := fixture.flow.Poll(context.Background(), "not-a-device-code")
	requireDevicePollError(t, malformedErr, DevicePollInvalidGrant)
}

func requireDevicePollError(t *testing.T, err error, kind DevicePollErrorKind) *DevicePollError {
	t.Helper()
	require.Error(t, err)
	var protocolErr *DevicePollError
	require.True(t, errors.As(err, &protocolErr), "error %v is not a DevicePollError", err)
	require.Equal(t, kind, protocolErr.Kind)
	require.False(t, strings.Contains(protocolErr.Error(), TokenPrefix))
	return protocolErr
}

func repeatedSeed(seed byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = seed
	}
	return result
}

type readerError struct{ message string }

func (e readerError) Read([]byte) (int, error) { return 0, errors.New(e.message) }
