package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/stretchr/testify/require"
)

func TestFederation_PacketContentAccounting(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx, calls := newFixtureServer(t), 0
	fx.ContextPacketHandler = packetHandler(t, &calls)
	boot := federationBootstrap(t, fx, validLocalBundle(now), nil)

	// When
	response := federationResponse(t, boot)

	// Then
	require.Equal(t, 1, calls)
	require.NotNil(t, response.LocalContext)
	require.Equal(t, localJSONBytes(response.LocalContext.Items, response.LocalContext.EvidenceRefs), response.FederatedBudget.LocalSerializedBytes)
	require.Equal(t, 10, response.FederatedBudget.LocalEstimatedTokens)
	require.Equal(t, response.FederatedBudget.LocalItemsUsed, len(response.LocalContext.Items))
	require.Equal(t, response.FederatedBudget.LocalSerializedBytes+response.FederatedBudget.HostedSerializedBytes, response.FederatedBudget.TotalSerializedBytes)
}

func TestFederation_EnvelopeExcluded(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx, calls := newFixtureServer(t), 0
	fx.ContextPacketHandler = packetHandler(t, &calls)

	// When
	response := federationResponse(t, federationBootstrap(t, fx, validLocalBundle(now), nil))
	encoded, err := json.Marshal(response)

	// Then
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Greater(t, len(encoded), response.FederatedBudget.TotalSerializedBytes)
	require.Equal(t, localJSONBytes(response.LocalContext.Items, response.LocalContext.EvidenceRefs), response.FederatedBudget.LocalSerializedBytes)
}

func TestFederation_Provenance(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx := newFixtureServer(t)

	// When
	response := federationResponse(t, federationBootstrap(t, fx, validLocalBundle(now), nil))

	// Then
	local := response.LocalContext
	require.Equal(t, contractsv1.MCPLocalContextAvailable, local.Status)
	require.Equal(t, contractsv1.MCPLocalFreshnessFresh, local.Freshness)
	require.Equal(t, []string{"indexed_commit_unknown"}, local.Warnings)
	require.Empty(t, local.IndexedRef)
	require.Empty(t, local.IndexedCommit)
	require.Equal(t, contractsv1.ClaimObserved, local.Items[0].ClaimKind)
	require.Equal(t, "local_index", local.EvidenceRefs[0].Source.System)
	require.NoError(t, local.EvidenceRefs[0].Validate())
	require.NotContains(t, local.Items[0].Summary, "internal/widget.go")
}

func TestFederation_DeterministicIDs(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)

	// When
	firstItems, firstRefs, firstErr := runtime.mapBundle("acme/widgets", validLocalBundle(now), map[string]struct{}{})
	secondItems, secondRefs, secondErr := runtime.mapBundle("acme/widgets", validLocalBundle(now), map[string]struct{}{})

	// Then
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	require.Equal(t, firstItems[0].PacketItemID, secondItems[0].PacketItemID)
	require.Equal(t, firstRefs[0].EvidenceRefID, secondRefs[0].EvidenceRefID)
}

func TestFederation_DisjointIDs(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)
	occupied := map[string]struct{}{"hosted": {}, "request": {}}

	// When
	items, refs, err := runtime.mapBundle("acme/widgets", validLocalBundle(now), occupied)

	// Then
	require.NoError(t, err)
	require.NotContains(t, []string{"hosted", "request"}, refs[0].EvidenceRefID)
	require.Equal(t, refs[0].EvidenceRefID+":item", items[0].PacketItemID)
	require.Contains(t, occupied, refs[0].EvidenceRefID)
	require.Contains(t, occupied, items[0].PacketItemID)
	require.Len(t, occupied, 4)
}
