package auth

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestDeviceFlow_Start_repeatedRandomnessInterruptionsCreateNoCredential(t *testing.T) {
	// Given
	random := io.LimitReader(deviceFlowRandom(50), 10)
	fixture := newDeviceFlowFixture(t, random)

	// When
	first, firstErr := fixture.flow.Start(context.Background())
	second, secondErr := fixture.flow.Start(context.Background())

	// Then
	require.Error(t, firstErr)
	require.Error(t, secondErr)
	require.Empty(t, first.DeviceCode)
	require.Empty(t, second.DeviceCode)
	require.NotContains(t, firstErr.Error(), TokenPrefix)
	require.NotContains(t, secondErr.Error(), TokenPrefix)
	credentials, err := fixture.credentials.List(context.Background(), deviceFlowTestOrgID)
	require.NoError(t, err)
	require.Empty(t, credentials)
	require.Empty(t, fixture.audit.Events())
}

func TestDeviceFlow_cancelledOperationsDoNotConsumeOrMutateState(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(51))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	_, cancelledStartErr := fixture.flow.Start(cancelled)
	started, startErr := fixture.flow.Start(context.Background())

	// Then
	require.ErrorIs(t, cancelledStartErr, context.Canceled)
	require.NoError(t, startErr)

	// Given
	principal := deviceApprovalPrincipal("full-chaos/dev-health-acr")
	_, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})
	require.NoError(t, err)

	// When
	cancelledIssued, cancelledPollErr := fixture.flow.Poll(cancelled, started.DeviceCode)
	record, getErr := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
	issued, redeemErr := fixture.flow.Poll(context.Background(), started.DeviceCode)

	// Then
	require.ErrorIs(t, cancelledPollErr, context.Canceled)
	require.Empty(t, cancelledIssued.Token)
	require.NoError(t, getErr)
	require.Equal(t, storage.DeviceAuthorizationStateApproved, record.State)
	require.NoError(t, redeemErr)
	require.True(t, IsTokenShapeValid(issued.Token))
	require.NotContains(t, cancelledPollErr.Error(), started.DeviceCode)
	require.False(t, errors.Is(cancelledPollErr, ErrDeviceInvalidGrant))
}
