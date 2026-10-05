package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestDeviceFlowPreviewRefusesEveryNonPendingStateLikeAnUnknownCode(t *testing.T) {
	approve := func(t *testing.T, fixture *deviceFlowFixture, started DeviceAuthorizationStart) {
		t.Helper()
		principal := deviceApprovalPrincipal("*")
		_, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
			Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
		})
		require.NoError(t, err)
	}
	cases := []struct {
		name    string
		state   storage.DeviceAuthorizationState
		advance func(t *testing.T, fixture *deviceFlowFixture, started DeviceAuthorizationStart)
	}{
		{name: "approved", state: storage.DeviceAuthorizationStateApproved, advance: approve},
		{name: "denied", state: storage.DeviceAuthorizationStateDenied, advance: func(t *testing.T, fixture *deviceFlowFixture, started DeviceAuthorizationStart) {
			t.Helper()
			_, err := fixture.flow.Deny(context.Background(), DeviceDenialRequest{Principal: deviceApprovalPrincipal("*"), UserCode: started.UserCode})
			require.NoError(t, err)
		}},
		{name: "redeemed", state: storage.DeviceAuthorizationStateRedeemed, advance: func(t *testing.T, fixture *deviceFlowFixture, started DeviceAuthorizationStart) {
			t.Helper()
			approve(t, fixture, started)
			_, err := fixture.flow.Poll(context.Background(), started.DeviceCode)
			require.NoError(t, err)
		}},
		{name: "redeemed then past expiry", state: storage.DeviceAuthorizationStateRedeemed, advance: func(t *testing.T, fixture *deviceFlowFixture, started DeviceAuthorizationStart) {
			t.Helper()
			approve(t, fixture, started)
			_, err := fixture.flow.Poll(context.Background(), started.DeviceCode)
			require.NoError(t, err)
			fixture.now = fixture.now.Add(storage.DeviceAuthorizationTTL)
		}},
		{name: "denied then past expiry", state: storage.DeviceAuthorizationStateDenied, advance: func(t *testing.T, fixture *deviceFlowFixture, started DeviceAuthorizationStart) {
			t.Helper()
			_, err := fixture.flow.Deny(context.Background(), DeviceDenialRequest{Principal: deviceApprovalPrincipal("*"), UserCode: started.UserCode})
			require.NoError(t, err)
			fixture.now = fixture.now.Add(storage.DeviceAuthorizationTTL)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDeviceFlowFixture(t, deviceFlowRandom(41))
			started := fixture.start(t)
			principal := deviceApprovalPrincipal("*")
			_, err := fixture.flow.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: principal, UserCode: started.UserCode})
			require.NoError(t, err)

			tc.advance(t, fixture, started)
			record, err := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
			require.NoError(t, err)
			require.Equal(t, tc.state, record.State)

			preview, previewErr := fixture.flow.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: principal, UserCode: started.UserCode})
			require.Zero(t, preview)
			require.ErrorIs(t, previewErr, storage.ErrDeviceAuthorizationNotFound)
			unknownCode := unknownUserCode(t, fixture, started)
			_, unknownErr := fixture.flow.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: principal, UserCode: unknownCode})
			require.ErrorIs(t, unknownErr, storage.ErrDeviceAuthorizationNotFound)
			require.Equal(t, unknownErr.Error(), previewErr.Error())
		})
	}
}

func unknownUserCode(t *testing.T, fixture *deviceFlowFixture, issued DeviceAuthorizationStart) string {
	t.Helper()
	for _, candidate := range []string{"ZZZZZZZZ", "YYYYYYYY"} {
		if candidate == issued.UserCode {
			continue
		}
		if _, err := fixture.store.GetByUserCodeHash(context.Background(), storage.HashUserCode(candidate)); err != nil {
			require.ErrorIs(t, err, storage.ErrDeviceAuthorizationNotFound)
			return candidate
		}
	}
	t.Fatal("no unissued user code available")
	return ""
}

type failingPreviewStore struct {
	storage.DeviceAuthorizationStore
	err error
}

func (s failingPreviewStore) Preview(context.Context, storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	return storage.DeviceAuthorization{}, s.err
}

func TestDeviceFlowPreviewKeepsAStoreFailureDistinctFromAnUnknownCode(t *testing.T) {
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(43))
	started := fixture.start(t)
	storeFailure := errors.New("device authorization store unavailable")
	flow, err := NewDeviceFlowService(failingPreviewStore{DeviceAuthorizationStore: fixture.store, err: storeFailure}, newTestService(t, fixture.credentials, fixture.audit, fixture.now), DeviceFlowOptions{
		Now: func() time.Time { return fixture.now }, Random: deviceFlowRandom(44), OAuthDeviceGrants: NoOAuthDeviceGrants{},
	})
	require.NoError(t, err)

	_, previewErr := flow.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: deviceApprovalPrincipal("*"), UserCode: started.UserCode})

	require.ErrorIs(t, previewErr, storeFailure)
	require.NotErrorIs(t, previewErr, storage.ErrDeviceAuthorizationNotFound)
}
