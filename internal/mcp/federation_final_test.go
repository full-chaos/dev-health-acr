package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/stretchr/testify/require"
)

func TestFederation_CollectsHostedItemEvidenceIDsForRehash(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)
	candidateItems, candidateRefs, err := runtime.mapBundle("acme/widgets", validLocalBundle(now), map[string]struct{}{})
	require.NoError(t, err)
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		packet := validContextPacketFixture(request.RequestID)
		candidateItems[0].PacketItemID = "packet_item_1"
		packet.Items = candidateItems
		packet.Budget = contractsv1.PacketBudget{MaxItems: request.Options.MaxItems, ItemsUsed: 1, MaxOutputTokens: request.Options.MaxOutputTokens, MaxSerializedBytes: request.Options.MaxSerializedBytes}
		writeJSONFixture(t, w, http.StatusOK, packet)
	}

	// When
	response := federationResponse(t, federationBootstrap(t, fx, validLocalBundle(now), nil))

	// Then
	require.NotEqual(t, candidateRefs[0].EvidenceRefID, response.LocalContext.EvidenceRefs[0].EvidenceRefID)
	require.NotEqual(t, candidateRefs[0].EvidenceRefID+":item", response.LocalContext.Items[0].PacketItemID)
}

func TestFederation_PublicIDGoldenCounterZeroAndOccupiedCounterOne(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)
	const counterZero = localEvidencePrefix + "29264581d7d023ba54e40453b968908ede451f3a8b2885f5c8c941638bd666ef"
	const counterOne = localEvidencePrefix + "d6a117542370625fe4cd5d2ccba2fd7c92f6a837250c5d7dd18a490ef05236b5"

	// When
	_, initial, initialErr := runtime.mapBundle("acme/widgets", validLocalBundle(now), map[string]struct{}{})
	_, rehashed, rehashErr := runtime.mapBundle("acme/widgets", validLocalBundle(now), map[string]struct{}{counterZero: {}})

	// Then
	require.NoError(t, initialErr)
	require.NoError(t, rehashErr)
	require.Equal(t, counterZero, initial[0].EvidenceRefID)
	require.Equal(t, counterOne, rehashed[0].EvidenceRefID)
}

func TestFederation_LocalContextMapsProvenanceWithEmptyWarnings(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := validLocalBundle(now)
	bundle.Warnings = nil
	bundle.IndexedRef = "refs/heads/main"
	bundle.IndexedCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)
	mapped, err := runtime.mapLocalBundle("acme/widgets", bundle, map[string]struct{}{})

	// When
	local := localContext(bundle, mapped, false)

	// Then
	require.NoError(t, err)
	require.NoError(t, local.Validate())
	require.NotNil(t, local.Warnings)
	require.Empty(t, local.Warnings)
	require.Equal(t, bundle.IndexedRef, local.IndexedRef)
	require.Equal(t, bundle.IndexedCommit, local.IndexedCommit)
	require.NotEqual(t, localEvidencePrefix+"29264581d7d023ba54e40453b968908ede451f3a8b2885f5c8c941638bd666ef", local.EvidenceRefs[0].EvidenceRefID)
}

func TestFederation_LocalSuccessPreservesHostedPacketBytes(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx := newFixtureServer(t)
	var expected contractsv1.ContextPacket
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		expected = validContextPacketFixture(request.RequestID)
		expected.Budget = contractsv1.PacketBudget{MaxItems: request.Options.MaxItems, MaxOutputTokens: request.Options.MaxOutputTokens, MaxSerializedBytes: request.Options.MaxSerializedBytes}
		writeJSONFixture(t, w, http.StatusOK, expected)
	}

	// When
	response := federationResponse(t, federationBootstrap(t, fx, validLocalBundle(now), nil))
	expectedBytes, expectedErr := json.Marshal(expected)
	actualBytes, actualErr := json.Marshal(response.Structured)

	// Then
	require.NotNil(t, response.LocalContext)
	require.NoError(t, expectedErr)
	require.NoError(t, actualErr)
	require.Equal(t, expected, response.Structured)
	require.Equal(t, expectedBytes, actualBytes)
}

func TestFederation_HostedRouteBeatsStaleLocalCacheForSameID(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx, calls := newFixtureServer(t), 0
	fx.ContextPacketHandler = packetHandler(t, new(int))
	fx.EvidenceHandler = func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSONFixture(t, w, http.StatusOK, validExpandedEvidenceFixture("authoritative-hosted"))
	}
	boot := federationBootstrap(t, fx, validLocalBundle(now), nil)
	response := federationResponse(t, boot)
	id := response.LocalContext.EvidenceRefs[0].EvidenceRefID
	boot.local.cache.putBatch([]cachedLocalEvidence{{ref: response.LocalContext.EvidenceRefs[0], evidence: sidecar.LocalExpandedEvidence{Excerpt: "stale local"}}})
	boot.hostedRoutes = newHostedRouteCache(1024, time.Minute, func() time.Time { return now })
	boot.hostedRoutes.put(id)

	// When
	result, err := handleSourceEvidence(context.Background(), boot, callToolRequest(t, map[string]any{"evidence_ref_id": id}))

	// Then
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, calls)
}
