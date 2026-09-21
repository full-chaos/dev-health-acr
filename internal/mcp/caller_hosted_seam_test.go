package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/version"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// testReleaseIdentity is a canonical release identity, the shape a real
// release binary carries in its ldflags.
func testReleaseIdentity() version.Info {
	return legacyIdentity("1.2.5")
}

// TestNewProcessConfigSendsTheReleaseVersionNotTheDevSentinel pins that a
// hosted process built through the public seam identifies itself to the
// hosted API as the running release. The default configuration carries the
// "dev" sentinel, which a real hosted API rejects before any caller can be
// resolved.
func TestNewProcessConfigSendsTheReleaseVersionNotTheDevSentinel(t *testing.T) {
	// Given
	var mu sync.Mutex
	var sent []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, r.Header.Get("X-ACR-Client-Version"))
		mu.Unlock()
		writeJSONFixture(t, w, http.StatusOK, validCapabilitiesFixture())
	}))
	t.Cleanup(server.Close)
	cfg := fixtureConfig(t, server)
	cfg.ClientVersion, cfg.SidecarVersion = "dev", "dev"

	// When
	process := NewProcessConfig(cfg, testReleaseIdentity(), io.Discard)
	caller, err := ResolveCaller(context.Background(), process, CallerCredential{Bearer: fixtureToken(0x31)})

	// Then
	require.NoError(t, err)
	require.NotNil(t, caller)
	require.Equal(t, "1.2.5", process.Config.ClientVersion)
	require.Equal(t, "1.2.5", process.Config.SidecarVersion)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"1.2.5"}, sent)
}

// TestWritebackIsDecidedPerCallerNotAsAConditionOfBeingACaller pins that a
// process with writeback enabled still serves a caller who cannot write:
// the reader resolves and gets the read catalogue, the writer resolves and
// additionally gets record_episode.
func TestWritebackIsDecidedPerCallerNotAsAConditionOfBeingACaller(t *testing.T) {
	// Given
	fx := newMultiCallerFixture(t)
	reader, writer := fixtureToken(0x41), fixtureToken(0x42)
	fx.register(reader, "caller-reader", validCapabilitiesFixture())
	writeCaps := validCapabilitiesFixture()
	writeCaps.Permissions.EpisodeWrite = true
	writeCaps.SupportedSchemaVersions = append(append([]string(nil), writeCaps.SupportedSchemaVersions...), writebackSchemaVersions...)
	writeCaps.EnabledTools = append(append([]string(nil), writeCaps.EnabledTools...), toolRecordEpisode)
	fx.register(writer, "caller-writer", writeCaps)
	cfg := fixtureConfig(t, fx.Server)
	cfg.EnableWriteback = true
	process := NewProcessConfig(cfg, testReleaseIdentity(), io.Discard)

	// When
	readerCaller, readerErr := ResolveCaller(context.Background(), process, CallerCredential{Bearer: reader})
	writerCaller, writerErr := ResolveCaller(context.Background(), process, CallerCredential{Bearer: writer})

	// Then
	require.NoError(t, readerErr, "a reader must not be refused because the process enables writeback")
	require.NoError(t, writerErr)
	require.ElementsMatch(t, []string{toolContextForTask, toolSourceEvidence}, listedToolNames(t, connectedClientForCaller(t, process, readerCaller)))
	require.ElementsMatch(t, []string{toolContextForTask, toolSourceEvidence, toolRecordEpisode}, listedToolNames(t, connectedClientForCaller(t, process, writerCaller)))

	// And a caller advertising the tool and the scope, but not every
	// writeback schema, is still not offered it.
	partial := fixtureToken(0x43)
	partialCaps := writeCaps
	partialCaps.SupportedSchemaVersions = append([]string(nil), validCapabilitiesFixture().SupportedSchemaVersions...)
	fx.register(partial, "caller-partial", partialCaps)
	partialCaller, err := ResolveCaller(context.Background(), process, CallerCredential{Bearer: partial})
	require.NoError(t, err)
	require.NotContains(t, listedToolNames(t, connectedClientForCaller(t, process, partialCaller)), toolRecordEpisode)
}

func negotiatedRevision(t *testing.T, server *mcpsdk.Server, requested string) string {
	t.Helper()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "revision-probe", Version: "0.0.1"}, nil).
		Connect(context.Background(), clientTransport, &mcpsdk.ClientSessionOptions{ProtocolVersion: requested})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session.InitializeResult().ProtocolVersion
}

// TestHostedServerNegotiatesTheNewestRevisionWhileStdioStaysCapped pins
// that the roots-driven revision cap belongs to STDIO alone: the same
// request for 2026-07-28 is honoured by a per-caller hosted server and
// negotiated down by the STDIO server.
func TestHostedServerNegotiatesTheNewestRevisionWhileStdioStaysCapped(t *testing.T) {
	// Given
	const newest = "2026-07-28"
	require.Contains(t, mcpsdk.SupportedProtocolVersions(), newest)
	_, process, caller, _, _, _ := twoCallerProcess(t)
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	// When
	hosted := negotiatedRevision(t, NewServerForCaller(process, caller, "test-version"), newest)
	stdio := negotiatedRevision(t, NewServer(boot, "test-version"), newest)

	// Then
	require.Equal(t, newest, hosted)
	require.NotEqual(t, newest, stdio)
	require.Less(t, stdio, rootsRemovedRevision)
}

// TestHostedProcessNeverFederatesProcessLocalEvidence pins that a process
// built for many callers carries no local workspace federation even when
// the environment asks for it, so no caller's answer can contain evidence
// read from the serving process's own workspace.
func TestHostedProcessNeverFederatesProcessLocalEvidence(t *testing.T) {
	// Given: the environment explicitly asks for local federation.
	t.Setenv("ACR_LOCAL_INDEX_PROVIDER", string(sidecar.LocalIndexProviderCodeGraph))
	require.Equal(t, sidecar.LocalIndexProviderCodeGraph, sidecar.LoadLocalIndexConfig().Provider)
	fx := newMultiCallerFixture(t)
	bearer := fixtureToken(0x51)
	fx.register(bearer, "caller-hosted", validCapabilitiesFixture())
	process := NewProcessConfig(fixtureConfig(t, fx.Server), testReleaseIdentity(), io.Discard)
	caller, err := ResolveCaller(context.Background(), process, CallerCredential{Bearer: bearer})
	require.NoError(t, err)
	initTempGitRepo(t, "acme/widgets")

	// When
	result, err := handleContextForTask(ContextWithCaller(context.Background(), caller), process, callToolRequest(t, map[string]any{"goal": "inspect widget"}))

	// Then
	require.Nil(t, process.local)
	require.NoError(t, err)
	require.False(t, result.IsError, "context_for_task reported an error: %s", toolResultText(result))
	var response contractsv1.MCPContextForTaskResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &response))
	require.Nil(t, response.LocalContext, "a hosted caller was served process-local evidence")
	require.Equal(t, "caller-hosted summary", response.Structured.Summary)
	require.NotNil(t, caller.localCache)
	require.Zero(t, caller.localCache.lru.Len())
}
