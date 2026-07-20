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

func TestFederation_HostedOccupiedIDsPrecedeLocalMapping(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)
	_, refs, err := runtime.mapBundle("acme/widgets", validLocalBundle(now), map[string]struct{}{})
	require.NoError(t, err)
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var received contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		packet := validContextPacketFixture(received.RequestID)
		packet.ContextPacketID = refs[0].EvidenceRefID
		packet.Budget = contractsv1.PacketBudget{MaxItems: received.Options.MaxItems, MaxOutputTokens: received.Options.MaxOutputTokens, MaxSerializedBytes: received.Options.MaxSerializedBytes}
		writeJSONFixture(t, w, http.StatusOK, packet)
	}

	// When
	response := federationResponse(t, federationBootstrap(t, fx, validLocalBundle(now), nil))

	// Then
	require.NotEqual(t, refs[0].EvidenceRefID, response.LocalContext.EvidenceRefs[0].EvidenceRefID)
}

func TestFederation_PublicID_isIndependentOfItemPosition(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := validLocalBundle(now)
	second := bundle.Evidence[0]
	second.ID = "second"
	second.Locator = "second-symbol"
	bundle.Evidence = append(bundle.Evidence, second)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)

	// When
	_, allRefs, allErr := runtime.mapBundle("acme/widgets", bundle, map[string]struct{}{})
	_, singleRefs, singleErr := runtime.mapBundle("acme/widgets", sidecar.LocalEvidenceBundle{ProviderID: bundle.ProviderID, ProviderVersion: bundle.ProviderVersion, QueryID: bundle.QueryID, QueryVersion: bundle.QueryVersion, IndexedAt: bundle.IndexedAt, Status: bundle.Status, Freshness: bundle.Freshness, Evidence: []sidecar.LocalExpandedEvidence{second}}, map[string]struct{}{})

	// Then
	require.NoError(t, allErr)
	require.NoError(t, singleErr)
	require.Equal(t, allRefs[1].EvidenceRefID, singleRefs[0].EvidenceRefID)
}

func TestFederation_MapBundle_rejectsDuplicateLocators(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := validLocalBundle(now)
	duplicate := bundle.Evidence[0]
	duplicate.ID = "another-id"
	bundle.Evidence = append(bundle.Evidence, duplicate)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)

	// When
	_, err := runtime.mapLocalBundle("acme/widgets", bundle, map[string]struct{}{})

	// Then
	require.Error(t, err)
}

func TestFederation_KnownHostedLocalPrefixRoutesHosted(t *testing.T) {
	// Given
	const hostedID = localEvidencePrefix + "hosted"
	fx, calls := newFixtureServer(t), 0
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var received contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		packet := validContextPacketFixture(received.RequestID)
		packet.ContextPacketID = hostedID
		writeJSONFixture(t, w, http.StatusOK, packet)
	}
	fx.EvidenceHandler = func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSONFixture(t, w, http.StatusOK, validExpandedEvidenceFixture(hostedID))
	}
	boot := federationBootstrap(t, fx, sidecar.LocalEvidenceBundle{}, sidecar.ErrLocalIndexUnavailable)
	boot.hostedRoutes = newHostedRouteCache(1024, 30*time.Minute, time.Now)
	initTempGitRepo(t, "acme/widgets")
	_, err := handleContextForTask(context.Background(), boot, callToolRequest(t, map[string]any{"goal": "inspect widget"}))
	require.NoError(t, err)

	// When
	result, err := handleSourceEvidence(context.Background(), boot, callToolRequest(t, map[string]any{"evidence_ref_id": hostedID}))

	// Then
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, calls)
}

func TestFederation_ResponseTrimsLocalOverflow(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := validLocalBundle(now)
	second := bundle.Evidence[0]
	second.ID = "private-secondary"
	second.Locator = "secondary-symbol"
	bundle.Evidence = append(bundle.Evidence, second)
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var received contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		packet := validContextPacketFixture(received.RequestID)
		packet.Budget = contractsv1.PacketBudget{MaxItems: received.Options.MaxItems, ItemsUsed: received.Options.MaxItems, MaxOutputTokens: received.Options.MaxOutputTokens, MaxSerializedBytes: received.Options.MaxSerializedBytes}
		writeJSONFixture(t, w, http.StatusOK, packet)
	}

	// When
	boot := federationBootstrap(t, fx, bundle, nil)
	initTempGitRepo(t, "acme/widgets")
	result, err := handleContextForTask(context.Background(), boot, callToolRequest(t, map[string]any{"goal": "inspect widget", "budget": map[string]any{"max_items": 4}}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	var response contractsv1.MCPContextForTaskResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &response))

	// Then
	require.Len(t, response.LocalContext.Items, 1)
	require.True(t, response.FederatedBudget.LocalTruncated)
}
