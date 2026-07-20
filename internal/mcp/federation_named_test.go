package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/stretchr/testify/require"
)

func federationResponse(t *testing.T, boot *Bootstrap) contractsv1.MCPContextForTaskResponse {
	t.Helper()
	initTempGitRepo(t, "acme/widgets")
	result, err := handleContextForTask(context.Background(), boot, callToolRequest(t, map[string]any{"goal": "inspect widget"}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	var response contractsv1.MCPContextForTaskResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &response))
	return response
}

func packetHandler(t *testing.T, calls *int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		(*calls)++
		var received contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		packet := validContextPacketFixture(received.RequestID)
		packet.Budget = contractsv1.PacketBudget{MaxItems: received.Options.MaxItems, MaxOutputTokens: received.Options.MaxOutputTokens, MaxSerializedBytes: received.Options.MaxSerializedBytes}
		writeJSONFixture(t, w, http.StatusOK, packet)
	}
}

func TestFederation_LocalRouting(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx, calls := newFixtureServer(t), 0
	fx.ContextPacketHandler = packetHandler(t, &calls)
	boot := federationBootstrap(t, fx, validLocalBundle(now), nil)

	// When
	response := federationResponse(t, boot)
	result, err := handleSourceEvidence(context.Background(), boot, callToolRequest(t, map[string]any{"evidence_ref_id": response.LocalContext.EvidenceRefs[0].EvidenceRefID}))

	// Then
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, calls)
	var expanded contractsv1.MCPSourceEvidenceResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &expanded))
	require.Equal(t, response.LocalContext.EvidenceRefs[0].EvidenceRefID, expanded.Structured.Evidence.EvidenceRefID)
	require.Equal(t, "safe local evidence", expanded.Structured.Excerpt)
	require.Empty(t, expanded.Structured.Structured)
}

func TestFederation_HostedRouting(t *testing.T) {
	// Given
	fx, calls := newFixtureServer(t), 0
	fx.EvidenceHandler = func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSONFixture(t, w, http.StatusOK, validExpandedEvidenceFixture("hosted-evidence"))
	}
	boot := federationBootstrap(t, fx, sidecar.LocalEvidenceBundle{}, sidecar.ErrLocalIndexUnavailable)

	// When
	result, err := handleSourceEvidence(context.Background(), boot, callToolRequest(t, map[string]any{"evidence_ref_id": "hosted-evidence"}))

	// Then
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, calls)
	var response contractsv1.MCPSourceEvidenceResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &response))
	require.Equal(t, "hosted-evidence", response.Structured.Evidence.EvidenceRefID)
}

func TestFederation_LocalContentOverflow(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := validLocalBundle(now)
	second := bundle.Evidence[0]
	second.ID = "private-secondary"
	second.Locator = "secondary-symbol"
	bundle.Evidence = append(bundle.Evidence, second)
	fx, calls := newFixtureServer(t), 0
	fx.ContextPacketHandler = packetHandler(t, &calls)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)
	mapped, err := runtime.mapLocalBundle("acme/widgets", bundle, map[string]struct{}{})

	// When
	_ = federationResponse(t, federationBootstrap(t, fx, bundle, nil))
	trimmed := mapped.trimTo(1, 10, localJSONBytes(mapped.items[:1], mapped.refs[:1]))

	// Then
	require.Equal(t, 1, calls)
	require.NoError(t, err)
	require.True(t, trimmed)
	require.Len(t, mapped.items, 1)
	require.Contains(t, localContext(bundle, mapped, trimmed).Warnings, "local_budget_exhausted")
}

func TestFederation_ForcedIDCollision(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, func([]byte) [32]byte { return [32]byte{} })

	// When
	_, _, noCollision := runtime.mapBundle("acme/widgets", validLocalBundle(now), map[string]struct{}{})
	_, _, exhausted := runtime.mapBundle("acme/widgets", validLocalBundle(now), map[string]struct{}{localEvidencePrefix + "0000000000000000000000000000000000000000000000000000000000000000": {}})

	// Then
	require.NoError(t, noCollision)
	require.Error(t, exhausted)
}

func TestFederation_UnknownLocalID(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	boot := federationBootstrap(t, fx, sidecar.LocalEvidenceBundle{}, sidecar.ErrLocalIndexUnavailable)

	// When
	result, err := handleSourceEvidence(context.Background(), boot, callToolRequest(t, map[string]any{"evidence_ref_id": localEvidencePrefix + "unknown"}))

	// Then
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestFederation_EvictedLocalID(t *testing.T) {
	// Given
	cache := newLocalEvidenceCache(1, time.Minute, time.Now)
	cache.putBatch([]cachedLocalEvidence{{ref: contractsv1.EvidenceRef{EvidenceRefID: localEvidencePrefix + "one"}}, {ref: contractsv1.EvidenceRef{EvidenceRefID: localEvidencePrefix + "two"}}})
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	boot.local = &localFederationRuntime{cache: cache, clock: time.Now}

	// When
	result, err := handleSourceEvidence(context.Background(), boot, callToolRequest(t, map[string]any{"evidence_ref_id": localEvidencePrefix + "one"}))

	// Then
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestFederation_ProviderTimeout(t *testing.T) {
	// Given
	fx, calls := newFixtureServer(t), 0
	fx.ContextPacketHandler = packetHandler(t, &calls)
	boot := federationBootstrap(t, fx, sidecar.LocalEvidenceBundle{}, context.DeadlineExceeded)

	// When
	response := federationResponse(t, boot)

	// Then
	require.Equal(t, 1, calls)
	require.Nil(t, response.LocalContext)
	require.Nil(t, response.FederatedBudget)
}

func TestFederation_HostedError(t *testing.T) {
	// Given
	now := time.Now().UTC()
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		writeErrorFixture(t, w, http.StatusInternalServerError, "internal", false)
	}
	boot := federationBootstrap(t, fx, validLocalBundle(now), nil)

	// When
	initTempGitRepo(t, "acme/widgets")
	result, err := handleContextForTask(context.Background(), boot, callToolRequest(t, map[string]any{"goal": "inspect widget"}))

	// Then
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Zero(t, boot.local.cache.lru.Len())
}

func TestFederation_DiscoveryOnce(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{MaxItems: 5, MaxOutputTokens: 1000, MaxSerializedBytes: 65536}, func() time.Time { return now }, sha256.Sum256)
	scope := resolvedTaskScope{Workspace: &sidecar.LocalWorkspaceSnapshot{}}
	runtime.providerFactory = func(sidecar.LocalIndexConfig, sidecar.LocalWorkspaceSnapshot) sidecar.LocalIndexProvider {
		return federationProvider{bundle: validLocalBundle(now)}
	}

	// When
	_, firstErr := runtime.bundle(context.Background(), scope, contractsv1.MCPContextForTaskRequest{Goal: "goal"}, contractsv1.PacketOptions{MaxItems: 20, MaxOutputTokens: 4000, MaxSerializedBytes: 262144})
	_, secondErr := runtime.bundle(context.Background(), scope, contractsv1.MCPContextForTaskRequest{Goal: "goal"}, contractsv1.PacketOptions{MaxItems: 20, MaxOutputTokens: 4000, MaxSerializedBytes: 262144})

	// Then
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
}

func TestFederation_ZeroReserveAndValidationBeforeCache(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{MaxItems: 5, MaxOutputTokens: 1000, MaxSerializedBytes: 65536}, func() time.Time { return now }, sha256.Sum256)
	scope := resolvedTaskScope{Workspace: &sidecar.LocalWorkspaceSnapshot{}}

	// When
	_, err := runtime.bundle(context.Background(), scope, contractsv1.MCPContextForTaskRequest{Goal: "goal"}, contractsv1.PacketOptions{MaxItems: 1, MaxOutputTokens: 500, MaxSerializedBytes: 8192})
	invalid := validLocalBundle(now)
	invalid.ProviderID = ""
	_, mapErr := runtime.mapLocalBundle("acme/widgets", invalid, map[string]struct{}{})

	// Then
	require.ErrorIs(t, err, sidecar.ErrLocalIndexUnavailable)
	require.NoError(t, mapErr)
	require.Zero(t, runtime.cache.lru.Len())
}

func TestFederation_WarningDistinctionAndOrder(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := validLocalBundle(now)
	bundle.Warnings = []string{"provider_degraded", "indexed_commit_unknown", "provider_degraded"}
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)
	mapped, err := runtime.mapLocalBundle("acme/widgets", bundle, map[string]struct{}{})

	// When
	context := localContext(bundle, mapped, true)

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{"provider_degraded", "indexed_commit_unknown", "local_budget_exhausted"}, context.Warnings)
}

func TestFederation_MixedGoldenCompatibility(t *testing.T) {
	// Given
	path := filepath.Join("..", "..", "contracts", "examples", "v1", "mcp_context_for_task_response_mixed.v1.json")
	payload, err := os.ReadFile(path)
	require.NoError(t, err)

	// When
	var golden contractsv1.MCPContextForTaskResponse
	err = json.Unmarshal(payload, &golden)

	// Then
	require.NoError(t, err)
	require.NotNil(t, golden.LocalContext)
	require.NotNil(t, golden.FederatedBudget)
	require.Equal(t, len(golden.LocalContext.Items), golden.FederatedBudget.LocalItemsUsed)
	require.Equal(t, golden.Structured.Budget.ItemsUsed+golden.FederatedBudget.LocalItemsUsed, golden.FederatedBudget.TotalItemsUsed)
}
