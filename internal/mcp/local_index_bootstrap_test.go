package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostedBootstrapIgnoresLocalIndexFailure(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	setFixtureEnv(t, fx, fixtureToken(91))
	t.Setenv("ACR_LOCAL_INDEX_TIMEOUT", "malformed-local-value")

	// When
	bootstrap, err := NewBootstrap(context.Background(), "dev")

	// Then
	require.NoError(t, err)
	require.Equal(t, "dev-health-acr", bootstrap.Capabilities.Service)
}
