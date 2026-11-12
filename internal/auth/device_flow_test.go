package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestDeviceFlow_Start_countsHintRunesLikeContract(t *testing.T) {
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(1))
	valid := strings.Repeat("界", 128)
	invalid := strings.Repeat("界", 129)
	_, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{OrganizationIDHint: valid})
	require.NoError(t, err)
	_, err = fixture.flow.Start(context.Background(), DeviceAuthorizationHints{OrganizationIDHint: invalid})
	require.ErrorIs(t, err, ErrInvalidDeviceFlow)
}

func TestDeviceFlow_Start_generatesBoundedCodesAndPersistsOnlyHashes(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(1))

	// When
	started := fixture.start(t)

	// Then
	decoded, err := base64.RawURLEncoding.DecodeString(started.DeviceCode)
	require.NoError(t, err)
	require.Len(t, decoded, deviceCodeBytes)
	require.Len(t, started.UserCode, contractsv1.DeviceUserCodeLength)
	for _, character := range started.UserCode {
		require.Contains(t, contractsv1.DeviceUserCodeAlphabet, string(character))
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

func TestDeviceFlow_Start_persistsOptionalUntrustedHintsWithoutGrantingThem(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(2))
	hints := DeviceAuthorizationHints{
		OrganizationIDHint: "11111111-1111-1111-1111-111111111111",
		RepositoryHints:    []string{"Full-Chaos/Dev-Health-Web", "full-chaos/dev-health-acr"},
	}

	// When
	started, err := fixture.flow.Start(context.Background(), hints)

	// Then
	require.NoError(t, err)
	record, err := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
	require.NoError(t, err)
	require.Equal(t, hints.OrganizationIDHint, record.OrganizationIDHint)
	require.Equal(t, []string{"full-chaos/dev-health-acr", "full-chaos/dev-health-web"}, record.RepositoryHints)
	require.Empty(t, record.AuthorizedOrgID)
	require.Empty(t, record.AuthorizedRepositoryScopes)
}

func TestDeviceFlow_Preview_returnsHintsWithoutChangingPendingStateOrGrant(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(3))
	started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{
		OrganizationIDHint: "11111111-1111-1111-1111-111111111111",
		RepositoryHints:    []string{"full-chaos/dev-health-acr"},
	})
	require.NoError(t, err)

	// When
	preview, err := fixture.flow.Preview(context.Background(), DeviceApprovalPreviewRequest{
		Principal: deviceApprovalPrincipal("full-chaos/dev-health-acr"), UserCode: started.UserCode,
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, "11111111-1111-1111-1111-111111111111", preview.OrganizationIDHint)
	require.Equal(t, []string{"full-chaos/dev-health-acr"}, preview.RepositoryHints)
	record, err := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStatePending, record.State)
	require.Empty(t, record.AuthorizedOrgID)
	require.Empty(t, record.AuthorizedRepositoryScopes)
}

func TestDeviceFlow_PreviewFiltersHintsWithoutUsingThemAsAuthorization(t *testing.T) {
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(6))
	started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{
		OrganizationIDHint: deviceFlowTestOrgID,
		RepositoryHints:    []string{"full-chaos/dev-health-acr", "full-chaos/dev-health-web"},
	})
	require.NoError(t, err)
	principal := deviceApprovalPrincipal("full-chaos/dev-health-acr")
	preview, err := fixture.flow.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: principal, UserCode: started.UserCode})
	require.NoError(t, err)
	require.Equal(t, []string{"full-chaos/dev-health-acr"}, preview.RepositoryHints)

	approved, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: deviceApprovalPrincipal("full-chaos/other"),
		UserCode:  started.UserCode, RepositoryScopes: []string{"full-chaos/other"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"full-chaos/other"}, approved.AuthorizedRepositoryScopes)
}

func TestDeviceFlow_Preview_failsClosedWhenHintOrganizationOrRepositoryIntersectionIsEmpty(t *testing.T) {
	for _, hints := range []DeviceAuthorizationHints{
		{OrganizationIDHint: "22222222-2222-2222-2222-222222222222"},
		{RepositoryHints: []string{"full-chaos/dev-health-web"}},
	} {
		fixture := newDeviceFlowFixture(t, deviceFlowRandom(7))
		started, err := fixture.flow.Start(context.Background(), hints)
		require.NoError(t, err)
		_, err = fixture.flow.Preview(context.Background(), DeviceApprovalPreviewRequest{
			Principal: deviceApprovalPrincipal("full-chaos/dev-health-acr"), UserCode: started.UserCode,
		})
		require.ErrorIs(t, err, ErrInvalidDeviceFlow)
	}
}

func TestDeviceFlow_Approve_failsClosedForMismatchedOrganizationHint(t *testing.T) {
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(8))
	started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{
		OrganizationIDHint: "22222222-2222-2222-2222-222222222222",
		RepositoryHints:    []string{"full-chaos/dev-health-acr"},
	})
	require.NoError(t, err)
	_, err = fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: deviceApprovalPrincipal("*"),
		UserCode:  started.UserCode, RepositoryScopes: []string{"*"},
	})
	require.ErrorIs(t, err, ErrInvalidDeviceFlow)
	record, err := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStatePending, record.State)
}

func TestDeviceFlow_Start_retriesHashCollisionsAndFailsBoundedlyWithoutLeakingCodes(t *testing.T) {
	// Given
	seeds := []byte{1, 1, 2}
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(seeds...))
	first := fixture.start(t)

	// When
	second, retryErr := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{})

	// Then
	require.NoError(t, retryErr)
	require.NotEqual(t, first.DeviceCode, second.DeviceCode)
	require.NotEqual(t, first.UserCode, second.UserCode)

	// Given
	constant := newDeviceFlowFixture(t, deviceFlowRandom(repeatedSeed(3, maxDeviceCodeAttempts+1)...))
	colliding := constant.start(t)

	// When
	_, collisionErr := constant.flow.Start(context.Background(), DeviceAuthorizationHints{})

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
	started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{})

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
		require.NotContains(t, contractsv1.DeviceUserCodeAlphabet, character)
	}
}

func TestDeviceFlow_Approve_persistsOrganizationWideGrantEvenWithRepositoryHints(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(4))
	started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{
		RepositoryHints: []string{"full-chaos/dev-health-acr"},
	})
	require.NoError(t, err)
	principal := deviceApprovalPrincipal("*")

	// When
	approved, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})

	// Then
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStateApproved, approved.State)
	require.Equal(t, principal.OrgID, approved.AuthorizedOrgID)
	require.Equal(t, principal.Subject, approved.ApprovingSubject)
	// CHAOS-7100: a typed-code approval authorizes the requestable ceiling;
	// the credential carries only what the device grant requested.
	require.Equal(t, []string{ScopeContextRead, ScopeEvidenceRead, ScopeDataRead}, approved.AuthorizedScopes)
	require.Equal(t, storage.AuthenticationMethodWebAssertion, approved.ApprovingAuthenticationMethod)
	require.Equal(t, storage.CredentialIssuanceProvenanceDeviceAuthorization, approved.IssuanceProvenance)
	require.Equal(t, []string{"*"}, approved.AuthorizedRepositoryScopes)
	_, duplicateErr := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})
	require.ErrorIs(t, duplicateErr, storage.ErrDeviceAuthorizationConflict)
}

// CHAOS-13562: an assertion that carries the repository_scopes claim must
// name the body's list; a subset is refused and leaves the request pending,
// the equal list is approved.
func TestDeviceFlow_Approve_requiresTheClaimToEqualTheBodyList(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(5))
	started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{
		RepositoryHints: []string{"full-chaos/dev-health-acr", "full-chaos/dev-health-web"},
	})
	require.NoError(t, err)
	principal := deviceApprovalPrincipal("full-chaos/dev-health-acr", "full-chaos/dev-health-web")

	// When a subset is approved
	_, err = fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: []string{"full-chaos/dev-health-acr"},
	})

	// Then it is refused, and the equal list is approved
	require.ErrorIs(t, err, ErrApprovalScopesDiffer)
	require.ErrorIs(t, err, ErrInvalidDeviceFlow)
	approved, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: []string{"full-chaos/dev-health-web", "full-chaos/dev-health-acr"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"full-chaos/dev-health-acr", "full-chaos/dev-health-web"}, approved.AuthorizedRepositoryScopes)
}

// CHAOS-13562: an assertion without the claim approves exactly the body's
// list, bounded by contractsv1.MaxRepositoryScopes.
func TestDeviceFlow_Approve_withoutTheClaimTakesTheBodyListUpToTheBound(t *testing.T) {
	bodyPrincipal := func() storage.Principal {
		principal := deviceApprovalPrincipal()
		principal.RepositoryScopes = nil
		principal.RepositoryScopesInBody = true
		return principal
	}
	scopes := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("full-chaos/repo-%04d", i)
		}
		return out
	}
	for _, cell := range []struct {
		count int
		want  error
	}{{1, nil}, {contractsv1.MaxRepositoryScopes, nil}, {contractsv1.MaxRepositoryScopes + 1, ErrTooManyRepositories}} {
		fixture := newDeviceFlowFixture(t, deviceFlowRandom(byte(40+cell.count%200)))
		started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{})
		require.NoError(t, err)
		approved, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
			Principal: bodyPrincipal(), UserCode: started.UserCode, RepositoryScopes: scopes(cell.count),
		})
		if cell.want != nil {
			require.ErrorIs(t, err, cell.want, "%d repositories", cell.count)
			require.ErrorIs(t, err, contractsv1.ErrTooManyRepositoryScopes)
			continue
		}
		require.NoError(t, err, "%d repositories", cell.count)
		require.Len(t, approved.AuthorizedRepositoryScopes, cell.count)
	}
	// A body-scoped principal that still carries scopes is malformed.
	malformed := bodyPrincipal()
	malformed.RepositoryScopes = []string{"full-chaos/repo-0000"}
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(39))
	started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{})
	require.NoError(t, err)
	_, err = fixture.flow.Approve(context.Background(), DeviceApprovalRequest{Principal: malformed, UserCode: started.UserCode, RepositoryScopes: []string{"full-chaos/repo-0000"}})
	require.ErrorIs(t, err, ErrInvalidDeviceFlow)
	// The preview needs the claim to filter the hints.
	_, err = fixture.flow.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: bodyPrincipal(), UserCode: started.UserCode})
	require.ErrorIs(t, err, ErrInvalidDeviceFlow)
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
		{name: "owner wildcard repository", principal: deviceApprovalPrincipal("full-chaos/*"), repositories: []string{"full-chaos/*"}},
		{name: "organization wildcard without credential issue", principal: storage.Principal{
			AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user_1", OrgID: deviceFlowTestOrgID,
			RepositoryScopes: []string{"*"}, Permissions: []string{ScopeContextRead},
		}, repositories: []string{"*"}},
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
	principal := deviceApprovalPrincipal("*")
	fixture.now = fixture.now.Add(storage.DeviceAuthorizationPollInterval)
	_, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: pending.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})
	require.NoError(t, err)
	redeemedAt := fixture.now

	// When
	issued, redeemErr := fixture.flow.Poll(context.Background(), pending.DeviceCode)
	fixture.now = fixture.now.Add(storage.DeviceCredentialAckWindow)
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

// CHAOS-13562: the preview reads the grant from the body under the approval's
// rule; a body without a list keeps the claim form.
func TestDeviceFlow_Preview_takesTheGrantFromTheBody(t *testing.T) {
	hints := []string{"full-chaos/dev-health-acr", "full-chaos/dev-health-web"}
	claimLess := deviceApprovalPrincipal()
	claimLess.RepositoryScopes = nil
	claimLess.RepositoryScopesInBody = true
	over := make([]string, contractsv1.MaxRepositoryScopes+1)
	for i := range over {
		over[i] = fmt.Sprintf("full-chaos/repo-%04d", i)
	}
	for i, cell := range []struct {
		name      string
		principal storage.Principal
		body      []string
		want      []string
		wantErr   error
	}{
		{"claim-less, body names one", claimLess, []string{"full-chaos/dev-health-web"}, []string{"full-chaos/dev-health-web"}, nil},
		{"claim equal to body", deviceApprovalPrincipal("full-chaos/dev-health-acr"), []string{"full-chaos/dev-health-acr"}, []string{"full-chaos/dev-health-acr"}, nil},
		{"claim wider than body", deviceApprovalPrincipal("full-chaos/dev-health-acr", "full-chaos/dev-health-web"), []string{"full-chaos/dev-health-acr"}, nil, ErrApprovalScopesDiffer},
		{"body over the bound", claimLess, over, nil, ErrTooManyRepositories},
		{"body malformed", claimLess, []string{"not a slug"}, nil, ErrInvalidDeviceFlow},
		{"no body list, claim", deviceApprovalPrincipal("full-chaos/dev-health-web"), nil, []string{"full-chaos/dev-health-web"}, nil},
		{"no body list, no claim", claimLess, nil, nil, ErrInvalidDeviceFlow},
	} {
		t.Run(cell.name, func(t *testing.T) {
			fixture := newDeviceFlowFixture(t, deviceFlowRandom(byte(60+i)))
			started, err := fixture.flow.Start(context.Background(), DeviceAuthorizationHints{RepositoryHints: hints})
			require.NoError(t, err)
			preview, err := fixture.flow.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: cell.principal, UserCode: started.UserCode, RepositoryScopes: cell.body})
			if cell.wantErr != nil {
				require.ErrorIs(t, err, cell.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, cell.want, preview.RepositoryHints)
		})
	}
}
