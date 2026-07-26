package auth

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestDeviceFlow_Poll_concurrentRedemptionReturnsPlaintextOnce(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(30))
	started := fixture.start(t)
	principal := deviceApprovalPrincipal("full-chaos/dev-health-acr")
	_, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})
	require.NoError(t, err)
	start := make(chan struct{})
	type pollResult struct {
		issued IssuedCredential
		err    error
	}
	results := make(chan pollResult, 2)
	var workers sync.WaitGroup

	// When
	for range 2 {
		workers.Go(func() {
			<-start
			issued, pollErr := fixture.flow.Poll(context.Background(), started.DeviceCode)
			results <- pollResult{issued: issued, err: pollErr}
		})
	}
	close(start)
	workers.Wait()
	close(results)

	// Then
	var tokens []string
	var rejected int
	for result := range results {
		if result.err == nil {
			tokens = append(tokens, result.issued.Token)
			continue
		}
		var protocolErr *DevicePollError
		require.True(t, errors.As(result.err, &protocolErr))
		require.Contains(t, []DevicePollErrorKind{DevicePollSlowDown, DevicePollInvalidGrant}, protocolErr.Kind)
		require.Empty(t, result.issued.Token)
		rejected++
	}
	require.Len(t, tokens, 1)
	require.Equal(t, 1, rejected)
	credentials, err := fixture.credentials.List(context.Background(), principal.OrgID)
	require.NoError(t, err)
	require.Len(t, credentials, 1)
	require.Len(t, fixture.audit.Events(), 1)
	require.NotContains(t, fixture.audit.Events()[0].Metadata, tokens[0])

	redeemed, err := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStateRedeemed, redeemed.State)
}
