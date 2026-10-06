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
	principal := deviceApprovalPrincipal("*")
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
	// A concurrent retry for a still-unacknowledged credential replaces it, so
	// both polls may succeed; what must hold is that exactly one of the
	// issued tokens still authenticates and exactly one credential is live.
	require.NotEmpty(t, tokens)
	require.Equal(t, 2, len(tokens)+rejected)
	authenticating := 0
	for _, token := range tokens {
		if _, findErr := fixture.credentials.FindByTokenHash(context.Background(), HashToken(token)); findErr == nil {
			authenticating++
		}
	}
	require.Equal(t, 1, authenticating)
	require.Len(t, liveCredentialIDs(t, fixture), 1)

	redeemed, err := fixture.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(started.DeviceCode))
	require.NoError(t, err)
	require.Equal(t, storage.DeviceAuthorizationStateRedeemed, redeemed.State)
}
