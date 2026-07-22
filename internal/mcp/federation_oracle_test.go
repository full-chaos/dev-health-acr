package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/stretchr/testify/require"
)

func TestFederation_RehashesWhenHostedOccupiesCandidateItemID(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, func(payload []byte) [32]byte {
		var digest [32]byte
		if strings.Contains(string(payload), `"Counter":1`) {
			digest[0] = 1
		}
		return digest
	})
	firstID := localEvidencePrefix + strings.Repeat("0", 64)
	occupied := map[string]struct{}{firstID + ":item": {}}

	// When
	items, refs, err := runtime.mapBundle("acme/widgets", validLocalBundle(now), occupied)

	// Then
	require.NoError(t, err)
	require.Equal(t, localEvidencePrefix+"01"+strings.Repeat("0", 62), refs[0].EvidenceRefID)
	require.Equal(t, refs[0].EvidenceRefID+":item", items[0].PacketItemID)
	require.Contains(t, occupied, refs[0].EvidenceRefID)
	require.Contains(t, occupied, items[0].PacketItemID)
}

func TestFederation_HostedRouteOverridesStaleLocalCache(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx, hostedCalls := newFixtureServer(t), 0
	fx.ContextPacketHandler = packetHandler(t, new(int))
	fx.EvidenceHandler = func(w http.ResponseWriter, r *http.Request) {
		hostedCalls++
		writeJSONFixture(t, w, http.StatusOK, validExpandedEvidenceFixture("hosted-id"))
	}
	boot := federationBootstrap(t, fx, validLocalBundle(now), nil)
	boot.hostedRoutes = newHostedRouteCache(1024, time.Minute, func() time.Time { return now })
	response := federationResponse(t, boot)
	id := response.LocalContext.EvidenceRefs[0].EvidenceRefID
	boot.hostedRoutes.put(id)

	// When
	result, err := handleSourceEvidence(context.Background(), boot, callToolRequest(t, map[string]any{"evidence_ref_id": id}))

	// Then
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, hostedCalls)
	var expanded contractsv1.MCPSourceEvidenceResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &expanded))
	require.Equal(t, "hosted-id", expanded.Structured.Evidence.EvidenceRefID)
}

func TestFederation_LocalProvenanceRoundTripsAndChangesPublicID(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	first := validLocalBundle(now)
	first.IndexedRef, first.IndexedCommit = "refs/heads/main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	second := first
	second.IndexedCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)
	firstMapped, firstErr := runtime.mapLocalBundle("acme/widgets", first, map[string]struct{}{})
	secondMapped, secondErr := runtime.mapLocalBundle("acme/widgets", second, map[string]struct{}{})

	// When
	context := localContext(first, firstMapped, false)

	// Then
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	require.Equal(t, first.IndexedRef, context.IndexedRef)
	require.Equal(t, first.IndexedCommit, context.IndexedCommit)
	require.NotEqual(t, firstMapped.refs[0].EvidenceRefID, secondMapped.refs[0].EvidenceRefID)
	codeGraph := validLocalBundle(now)
	require.Empty(t, localContext(codeGraph, firstMapped, false).IndexedCommit)
}

func TestFederation_HandlerRejectsDuplicateLocalBeforeHostedReservation(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := validLocalBundle(now)
	bundle.Evidence = append(bundle.Evidence, bundle.Evidence[0])
	fx := newFixtureServer(t)
	var received contractsv1.ContextPacketRequest
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		writeJSONFixture(t, w, http.StatusOK, validContextPacketFixture(received.RequestID))
	}
	boot := federationBootstrap(t, fx, bundle, nil)

	// When
	response := federationResponse(t, boot)

	// Then
	require.Equal(t, defaultMaxItems, received.Options.MaxItems)
	require.Nil(t, response.LocalContext)
	require.Nil(t, response.FederatedBudget)
	require.Zero(t, boot.local.cache.lru.Len())
}

func TestFederation_HandlerValidationFailureDoesNotCacheOrRoute(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = packetHandler(t, new(int))
	boot := federationBootstrap(t, fx, validLocalBundle(now), nil)
	boot.hostedRoutes = newHostedRouteCache(10, time.Minute, func() time.Time { return now })
	previous := validateFederatedResponse
	validateFederatedResponse = func(contractsv1.MCPContextForTaskResponse) error { return context.Canceled }
	t.Cleanup(func() { validateFederatedResponse = previous })
	initTempGitRepo(t, "acme/widgets")

	// When
	result, err := handleContextForTask(context.Background(), boot, callToolRequest(t, map[string]any{"goal": "inspect widget"}))

	// Then
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Zero(t, boot.local.cache.lru.Len())
	require.Zero(t, boot.hostedRoutes.lru.Len())
}

func TestFederation_HandlerPreservesHostedPacketAndRendering(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	var expected contractsv1.ContextPacket
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		expected = validContextPacketFixture(request.RequestID)
		writeJSONFixture(t, w, http.StatusOK, expected)
	}
	boot := federationBootstrap(t, fx, sidecar.LocalEvidenceBundle{}, sidecar.ErrLocalIndexUnavailable)

	// When
	response := federationResponse(t, boot)

	// Then
	require.Equal(t, expected, response.Structured)
	markdown, truncated := sidecar.RenderContextPacketMarkdown(expected, renderedMarkdownMaxBytes)
	require.Equal(t, contractsv1.MCPRenderedMarkdown{Markdown: markdown, Untrusted: true, Truncated: truncated}, response.RenderedMarkdown)
}

func TestFederation_HandlerDiscoversOnceForCompatibleExplicitRequest(t *testing.T) {
	// Given
	root := initTempGitRepo(t, "acme/widgets")
	previous := discoverWorkspace
	calls := 0
	discoverWorkspace = func(context.Context, sidecar.DiscoverOptions) (sidecar.WorkspaceInfo, error) {
		calls++
		return sidecar.WorkspaceInfo{GitRoot: root, Remote: &sidecar.RemoteInfo{Host: "github.com", Owner: "acme", Repo: "widgets"}, Branch: "main", CommitSHA: strings.Repeat("a", 40)}, nil
	}
	t.Cleanup(func() { discoverWorkspace = previous })
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = packetHandler(t, new(int))
	boot := federationBootstrap(t, fx, validLocalBundle(now), nil)

	// When
	result, err := handleContextForTask(context.Background(), boot, callToolRequest(t, map[string]any{"goal": "inspect", "repository": map[string]any{"slug": "acme/widgets"}, "scope": map[string]any{"branch": "main", "commit_sha": strings.Repeat("a", 40)}}))

	// Then
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, calls)
}

func TestFederation_HandlerCollisionExhaustionDoesNotCacheOrRetryHosted(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	fx, calls := newFixtureServer(t), 0
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		packet := validContextPacketFixture(request.RequestID)
		packet.ContextPacketID = localEvidencePrefix + strings.Repeat("0", 64)
		writeJSONFixture(t, w, http.StatusOK, packet)
	}
	boot := federationBootstrap(t, fx, validLocalBundle(now), nil)
	boot.local.hash = func([]byte) [32]byte { return [32]byte{} }
	initTempGitRepo(t, "acme/widgets")

	// When
	result, err := handleContextForTask(context.Background(), boot, callToolRequest(t, map[string]any{"goal": "inspect"}))

	// Then
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, 1, calls)
	require.Zero(t, boot.local.cache.lru.Len())
}
