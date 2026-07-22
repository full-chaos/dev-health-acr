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

type federationProvider struct {
	bundle sidecar.LocalEvidenceBundle
	err    error
}

func (p federationProvider) Capabilities(context.Context) (sidecar.LocalIndexCapabilities, error) {
	return sidecar.LocalIndexCapabilities{}, nil
}
func (p federationProvider) ContextForTask(context.Context, sidecar.LocalContextRequest) (sidecar.LocalEvidenceBundle, error) {
	return p.bundle, p.err
}
func (p federationProvider) ResolveEvidence(context.Context, string) (sidecar.LocalExpandedEvidence, error) {
	return sidecar.LocalExpandedEvidence{}, sidecar.ErrLocalEvidenceNotFound
}

func validLocalBundle(now time.Time) sidecar.LocalEvidenceBundle {
	return sidecar.LocalEvidenceBundle{ProviderID: "codegraph", ProviderVersion: "v1", QueryID: "query", QueryVersion: "v1", IndexedAt: &now, Warnings: []string{"indexed_commit_unknown"}, Status: sidecar.LocalIndexStatusAvailable, Freshness: sidecar.LocalIndexFreshnessFresh, Evidence: []sidecar.LocalExpandedEvidence{{ID: "private", Locator: "symbol", Title: "Widget", Excerpt: "safe local evidence", EstimatedTokens: 10, QueryID: "query", Relation: "definition", RepositoryPath: "internal/widget.go", StartLine: 12}}}
}

func federationBootstrap(t *testing.T, fx *fixtureServer, bundle sidecar.LocalEvidenceBundle, err error) *Bootstrap {
	t.Helper()
	boot := newFixtureBootstrap(t, fx)
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	local := newLocalFederationRuntime(sidecar.LocalIndexConfig{Provider: sidecar.LocalIndexProviderCodeGraph, Timeout: time.Second, MaxItems: 5, MaxOutputTokens: 1000, MaxSerializedBytes: 65536}, func() time.Time { return now }, sha256.Sum256)
	local.providerFactory = func(sidecar.LocalIndexConfig, sidecar.LocalWorkspaceSnapshot) sidecar.LocalIndexProvider {
		return federationProvider{bundle: bundle, err: err}
	}
	boot.local = local
	return boot
}

func callFederatedContext(t *testing.T, boot *Bootstrap) contractsv1.MCPContextForTaskResponse {
	t.Helper()
	initTempGitRepo(t, "acme/widgets")
	result, err := handleContextForTask(context.Background(), boot, callToolRequest(t, map[string]any{"goal": "inspect widget"}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	var response contractsv1.MCPContextForTaskResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &response))
	return response
}

func TestFederation_BudgetPartition(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx := newFixtureServer(t)
	var requested contractsv1.ContextPacketRequest
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&requested))
		packet := validContextPacketFixture(requested.RequestID)
		packet.Budget = contractsv1.PacketBudget{MaxItems: requested.Options.MaxItems, MaxOutputTokens: requested.Options.MaxOutputTokens, MaxSerializedBytes: requested.Options.MaxSerializedBytes}
		writeJSONFixture(t, w, http.StatusOK, packet)
	}

	// When
	response := callFederatedContext(t, federationBootstrap(t, fx, validLocalBundle(now), nil))

	// Then
	require.NotNil(t, response.LocalContext)
	require.NotNil(t, response.FederatedBudget)
	require.Equal(t, 15, requested.Options.MaxItems)
	require.Equal(t, 3000, requested.Options.MaxOutputTokens)
	require.Equal(t, 196608, requested.Options.MaxSerializedBytes)
	require.Empty(t, response.LocalContext.IndexedRef)
	require.Empty(t, response.LocalContext.IndexedCommit)
	require.Equal(t, []string{"indexed_commit_unknown"}, response.LocalContext.Warnings)
	require.Equal(t, response.Structured.Budget.ItemsUsed+response.FederatedBudget.LocalItemsUsed, response.FederatedBudget.TotalItemsUsed)
	require.NoError(t, response.Validate())
}

func TestFederation_HostedOnlyBudget(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	var requested contractsv1.ContextPacketRequest
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&requested))
		writeJSONFixture(t, w, http.StatusOK, validContextPacketFixture(requested.RequestID))
	}
	boot := federationBootstrap(t, fx, sidecar.LocalEvidenceBundle{}, sidecar.ErrLocalIndexUnavailable)

	// When
	response := callFederatedContext(t, boot)

	// Then
	require.Nil(t, response.LocalContext)
	require.Nil(t, response.FederatedBudget)
	require.Equal(t, defaultMaxItems, requested.Options.MaxItems)
	require.Equal(t, defaultMaxOutputTokens, requested.Options.MaxOutputTokens)
	require.Equal(t, defaultMaxSerializedBytes, requested.Options.MaxSerializedBytes)
}
