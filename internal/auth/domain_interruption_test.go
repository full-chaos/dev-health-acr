package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/stretchr/testify/require"
)

func TestDeviceFlow_repeatedCanceledOperationsLeavePendingState(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(71))
	started := fixture.start(t)
	principal := deviceApprovalPrincipal("full-chaos/dev-health-acr")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	for range 3 {
		_, startErr := fixture.flow.Start(ctx, DeviceAuthorizationHints{})
		_, pollErr := fixture.flow.Poll(ctx, started.DeviceCode)
		_, approveErr := fixture.flow.Approve(ctx, DeviceApprovalRequest{
			Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
		})
		_, denyErr := fixture.flow.Deny(ctx, DeviceDenialRequest{Principal: principal, UserCode: started.UserCode})
		for _, err := range []error{startErr, pollErr, approveErr, denyErr} {
			require.True(t, errors.Is(err, context.Canceled), "error = %v", err)
			require.NotContains(t, err.Error(), started.DeviceCode)
			require.NotContains(t, err.Error(), started.UserCode)
		}
	}

	// Then
	record, err := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStatePending, record.State)
}

func TestService_repeatedCanceledSelfLifecycleLeavesCredentialActive(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 16, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	issued := createSelfCredential(t, service, []string{"owner/repo"})
	principal := selfPrincipal(issued.Credential)
	beforeEvents := len(audit.Events())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	for range 3 {
		rotation, rotateErr := service.RotateSelf(ctx, principal)
		_, revokeErr := service.RevokeSelf(ctx, principal)
		require.True(t, errors.Is(rotateErr, context.Canceled), "error = %v", rotateErr)
		require.True(t, errors.Is(revokeErr, context.Canceled), "error = %v", revokeErr)
		require.Empty(t, rotation.Issued.Token)
	}

	// Then
	stored, err := store.GetByID(context.Background(), principal.OrgID, principal.CredentialID)
	require.NoError(t, err)
	require.Nil(t, stored.RevokedAt)
	require.Nil(t, stored.ExpiresAt)
	credentials, err := store.List(context.Background(), principal.OrgID)
	require.NoError(t, err)
	require.Len(t, credentials, 1)
	require.Len(t, audit.Events(), beforeEvents)
}
