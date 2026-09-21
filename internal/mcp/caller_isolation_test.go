package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// multiCallerFixture is a hosted API that answers strictly according to the
// bearer it actually received on the wire, and records which bearer asked
// for what.
//
// It is what makes identity isolation OBSERVABLE rather than argued: every
// payload it returns names the caller it was produced for, so a request
// that travelled on the wrong credential comes back carrying the wrong
// caller's label and the assertion fails on the value, not on a pointer
// identity that a refactor could preserve while leaking anyway.
type multiCallerFixture struct {
	t            *testing.T
	Server       *httptest.Server
	mu           sync.Mutex
	labels       map[string]string
	capabilities map[string]contractsv1.Capabilities
	seen         map[string][]string
	unauthorized int
}

func newMultiCallerFixture(t *testing.T) *multiCallerFixture {
	t.Helper()
	fx := &multiCallerFixture{
		t:            t,
		labels:       map[string]string{},
		capabilities: map[string]contractsv1.Capabilities{},
		seen:         map[string][]string{},
	}
	fx.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		label, capabilities, known := fx.lookup(bearer, r.URL.Path)
		if !known {
			writeErrorFixture(t, w, http.StatusUnauthorized, "invalid_token", false)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/agent-context/capabilities":
			writeJSONFixture(t, w, http.StatusOK, capabilities)
		case r.URL.Path == "/api/v1/agent-context/context-packets":
			var received contractsv1.ContextPacketRequest
			_ = json.NewDecoder(r.Body).Decode(&received)
			packet := validContextPacketFixture(received.RequestID)
			packet.Summary = label + " summary"
			writeJSONFixture(t, w, http.StatusOK, packet)
		case strings.HasPrefix(r.URL.Path, "/api/v1/agent-context/evidence/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/agent-context/evidence/")
			evidence := validExpandedEvidenceFixture(id)
			evidence.Excerpt = label + " excerpt"
			writeJSONFixture(t, w, http.StatusOK, evidence)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fx.Server.Close)
	return fx
}

func (f *multiCallerFixture) lookup(bearer, path string) (string, contractsv1.Capabilities, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	label, known := f.labels[bearer]
	if !known {
		f.unauthorized++
		return "", contractsv1.Capabilities{}, false
	}
	f.seen[label] = append(f.seen[label], path)
	return label, f.capabilities[bearer], true
}

func (f *multiCallerFixture) register(bearer, label string, capabilities contractsv1.Capabilities) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels[bearer] = label
	f.capabilities[bearer] = capabilities
}

func (f *multiCallerFixture) pathsSeenFor(label string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen[label]...)
}

func (f *multiCallerFixture) unauthorizedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unauthorized
}

// answerCapabilitiesFixture is validCapabilitiesFixture plus the two
// Context Fabric answer tools, which the hosted API advertises per
// credential.
func answerCapabilitiesFixture() contractsv1.Capabilities {
	capabilities := validCapabilitiesFixture()
	capabilities.EnabledTools = append(append([]string(nil), capabilities.EnabledTools...), toolInvestigateQuestion, toolInvestigationResult)
	return capabilities
}

// twoCallerProcess boots one process configuration and resolves two
// distinct callers against it, each with its OWN bearer, exactly as a
// hosted transport does per request.
func twoCallerProcess(t *testing.T) (*multiCallerFixture, *ProcessConfig, *CallerContext, *CallerContext, string, string) {
	t.Helper()
	fx := newMultiCallerFixture(t)
	bearerA, bearerB := fixtureToken(0x11), fixtureToken(0x22)
	fx.register(bearerA, "caller-a", answerCapabilitiesFixture())
	fx.register(bearerB, "caller-b", validCapabilitiesFixture())

	cfg := NewProcessConfig(fixtureConfig(t, fx.Server), testReleaseIdentity(), io.Discard)
	callerA, err := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: bearerA})
	require.NoError(t, err)
	callerB, err := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: bearerB})
	require.NoError(t, err)
	return fx, cfg, callerA, callerB, bearerA, bearerB
}

func sourceEvidenceExcerpt(t *testing.T, ctx context.Context, cfg *ProcessConfig, caller *CallerContext, id string) string {
	t.Helper()
	result, err := handleSourceEvidence(ContextWithCaller(ctx, caller), cfg, callToolRequest(t, map[string]any{"evidence_ref_id": id}))
	require.NoError(t, err)
	require.False(t, result.IsError, "source_evidence reported an error: %s", toolResultText(result))
	var response contractsv1.MCPSourceEvidenceResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &response))
	return response.Structured.Excerpt
}

// TestTwoCallersInOneProcessShareNoClientCapabilitiesOrCache is the
// isolation pin for A1: two credentials served by ONE process must reach
// the hosted API as themselves and must never read each other's answers.
//
// It fails on the defect it exists to catch -- a single shared hosted
// client, which is what the process had before caller contexts existed --
// in three independent ways: the two clients are the same pointer, every
// hosted call carries one bearer so the fixture attributes both callers'
// requests to one label, and the returned excerpt names the wrong caller.
func TestTwoCallersInOneProcessShareNoClientCapabilitiesOrCache(t *testing.T) {
	// Given
	fx, cfg, callerA, callerB, _, _ := twoCallerProcess(t)
	ctx := context.Background()

	// Then: the caller-derived state is distinct, not shared.
	require.NotSame(t, callerA.Client(), callerB.Client(), "two callers must not share one hosted API client")
	require.NotSame(t, callerA, callerB)
	require.NotSame(t, callerA.hostedRoutes, callerB.hostedRoutes, "two callers must not share the hosted routing cache")
	require.NotSame(t, callerA.localCache, callerB.localCache, "two callers must not share the local evidence cache")
	require.Contains(t, callerA.Capabilities().EnabledTools, toolInvestigateQuestion)
	require.NotContains(t, callerB.Capabilities().EnabledTools, toolInvestigateQuestion)

	// When: the two callers interleave hosted reads in one process.
	for range 3 {
		require.Equal(t, "caller-a excerpt", sourceEvidenceExcerpt(t, ctx, cfg, callerA, "evidence_shared_id"))
		require.Equal(t, "caller-b excerpt", sourceEvidenceExcerpt(t, ctx, cfg, callerB, "evidence_shared_id"))
	}

	// Then: the hosted API attributed every call to the credential that
	// actually made it, and neither caller's traffic went out unattributed.
	pathsA, pathsB := fx.pathsSeenFor("caller-a"), fx.pathsSeenFor("caller-b")
	require.NotEmpty(t, pathsA)
	require.NotEmpty(t, pathsB)
	require.Len(t, pathsA, 4, "one capabilities call plus three evidence reads")
	require.Len(t, pathsB, 4, "one capabilities call plus three evidence reads")
	require.Zero(t, fx.unauthorizedCount())
}

// TestConcurrentCallersNeverReceiveAnotherCallersAnswer runs the same
// isolation under -race with both callers in flight at once, because a
// shared client is a data race as well as an authorization defect and the
// interleaved test above cannot distinguish the two.
func TestConcurrentCallersNeverReceiveAnotherCallersAnswer(t *testing.T) {
	// Given
	fx, cfg, callerA, callerB, _, _ := twoCallerProcess(t)
	const rounds = 8

	// When
	var wg sync.WaitGroup
	excerpts := make([]string, 2*rounds)
	for round := range rounds {
		for index, pair := range []struct {
			caller *CallerContext
			slot   int
		}{{callerA, 2 * round}, {callerB, 2*round + 1}} {
			wg.Add(1)
			go func(caller *CallerContext, slot, index int) {
				defer wg.Done()
				excerpts[slot] = sourceEvidenceExcerpt(t, context.Background(), cfg, caller, "evidence_shared_id")
			}(pair.caller, pair.slot, index)
		}
	}
	wg.Wait()

	// Then
	require.Len(t, excerpts, 2*rounds)
	for slot, excerpt := range excerpts {
		want := "caller-a excerpt"
		if slot%2 == 1 {
			want = "caller-b excerpt"
		}
		require.Equal(t, want, excerpt, "slot %d received another caller's evidence", slot)
	}
	require.Len(t, fx.pathsSeenFor("caller-a"), rounds+1)
	require.Len(t, fx.pathsSeenFor("caller-b"), rounds+1)
	require.Zero(t, fx.unauthorizedCount())
}

// TestLocalEvidenceCachedForOneCallerIsUnreachableFromAnother pins the
// cache half of A1. A federated answer puts a local excerpt in the cache of
// the caller whose question produced it; a second caller asking for the
// same reference id must not be served from it.
func TestLocalEvidenceCachedForOneCallerIsUnreachableFromAnother(t *testing.T) {
	// Given
	fx, cfg, callerA, callerB, _, _ := twoCallerProcess(t)
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	local := newLocalFederationRuntime(sidecar.LocalIndexConfig{Provider: sidecar.LocalIndexProviderCodeGraph, Timeout: time.Second, MaxItems: 5, MaxOutputTokens: 1000, MaxSerializedBytes: 65536}, func() time.Time { return now }, sha256.Sum256)
	local.providerFactory = func(sidecar.LocalIndexConfig, sidecar.LocalWorkspaceSnapshot) sidecar.LocalIndexProvider {
		return federationProvider{bundle: validLocalBundle(now)}
	}
	cfg.local = local
	// Local evidence exists only where the process serves its own workspace;
	// a hosted process resolves scope from the request and never federates.
	cfg.Transport = TransportStdio
	initTempGitRepo(t, "acme/widgets")

	// When: caller A asks a question that federates local evidence.
	result, err := handleContextForTask(ContextWithCaller(context.Background(), callerA), cfg, callToolRequest(t, map[string]any{"goal": "inspect widget"}))
	require.NoError(t, err)
	require.False(t, result.IsError, "context_for_task reported an error: %s", toolResultText(result))
	var packet contractsv1.MCPContextForTaskResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &packet))
	require.NotNil(t, packet.LocalContext)
	require.NotEmpty(t, packet.LocalContext.EvidenceRefs)
	localID := packet.LocalContext.EvidenceRefs[0].EvidenceRefID
	require.True(t, strings.HasPrefix(localID, localEvidencePrefix))

	// Then: A reads its own cached excerpt.
	require.Equal(t, "safe local evidence", sourceEvidenceExcerpt(t, context.Background(), cfg, callerA, localID))
	require.NotNil(t, callerA.localCache)
	require.NotZero(t, callerA.localCache.lru.Len())

	// And: B, asking for the identical reference id, is refused rather than
	// served A's excerpt, and nothing reached its cache either.
	refused, err := handleSourceEvidence(ContextWithCaller(context.Background(), callerB), cfg, callToolRequest(t, map[string]any{"evidence_ref_id": localID}))
	require.NoError(t, err)
	require.True(t, refused.IsError, "caller B read caller A's cached local evidence")
	require.NotContains(t, toolResultText(refused), "safe local evidence")
	require.NotNil(t, callerB.localCache)
	require.Zero(t, callerB.localCache.lru.Len())
	require.Zero(t, fx.unauthorizedCount())
}

// TestEveryToolRefusesARequestCarryingNoCallerIdentity pins the fail-closed
// rule: a tool handler reached without an identity refuses rather than
// borrowing whatever credential the process happens to hold.
//
// A live, fully served caller exists in the same process first, and its
// calls have already run. That is the state a borrowed identity needs: a
// handler that reached for "the caller this process last served" instead of
// the request's own would find one here and answer, so the refusal below is
// a refusal in the presence of an available identity rather than in its
// absence.
func TestEveryToolRefusesARequestCarryingNoCallerIdentity(t *testing.T) {
	// Given
	fx, liveCfg, liveCaller, _, _, _ := twoCallerProcess(t)
	require.Equal(t, "caller-a excerpt", sourceEvidenceExcerpt(t, context.Background(), liveCfg, liveCaller, "evidence_live_id"))
	liveSession := connectedClientForCaller(t, liveCfg, liveCaller)
	require.NotEmpty(t, listedToolNames(t, liveSession))

	cfg := liveCfg
	handlers := map[string]struct {
		handle func(context.Context, *ProcessConfig, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error)
		args   map[string]any
	}{
		toolContextForTask:      {handleContextForTask, map[string]any{"goal": "inspect widget", "repository": map[string]any{"slug": "acme/widgets"}}},
		toolSourceEvidence:      {handleSourceEvidence, map[string]any{"evidence_ref_id": "evidence_0001"}},
		toolInvestigateQuestion: {handleInvestigateQuestion, map[string]any{"question": "why is checkout slow"}},
		toolInvestigationResult: {handleInvestigationResult, map[string]any{"result_id": "cfr_00000000000000000000000001"}},
		toolRecordEpisode:       {handleRecordEpisode, map[string]any{"client_episode_id": "episode-1"}},
	}
	require.Len(t, handlers, 5, "every registered tool is covered")

	refused := 0
	for name, entry := range handlers {
		// When: the request context carries no caller.
		result, err := entry.handle(context.Background(), cfg, callToolRequest(t, entry.args))

		// Then
		require.NoError(t, err, "%s must refuse as a tool error, never a protocol error", name)
		require.True(t, result.IsError, "%s served a request with no caller identity", name)
		require.Contains(t, toolResultText(result), "no authenticated caller identity", "%s refused for the wrong reason", name)
		require.NotContains(t, toolResultText(result), "caller-a", "%s answered as the caller the process last served", name)
		refused++
	}
	require.Equal(t, len(handlers), refused)
	require.Positive(t, refused)
	require.Zero(t, fx.unauthorizedCount(), "no hosted call may be attempted without a caller")

	// And the live caller is still served correctly afterwards, so the
	// refusals above are the identity rule biting rather than a process
	// that stopped working.
	require.Equal(t, "caller-a excerpt", sourceEvidenceExcerpt(t, context.Background(), liveCfg, liveCaller, "evidence_live_id"))
}

// TestCallerFromContextFailsClosed covers the accessor itself over its
// whole input domain: nothing bound, an explicit nil caller, and a caller
// carrying no hosted client are each refusals, never a usable identity.
func TestCallerFromContextFailsClosed(t *testing.T) {
	cases := map[string]context.Context{
		"nothing bound":    context.Background(),
		"nil caller bound": ContextWithCaller(context.Background(), nil),
		"caller with no client": ContextWithCaller(context.Background(), &CallerContext{
			capabilities: validCapabilitiesFixture(),
		}),
	}
	require.Len(t, cases, 3)
	// A server for a real caller exists in this process, so an accessor
	// that reached for a process-wide "current caller" would find one.
	_, liveCfg, liveCaller, _, _, _ := twoCallerProcess(t)
	require.NotEmpty(t, listedToolNames(t, connectedClientForCaller(t, liveCfg, liveCaller)))
	for name, ctx := range cases {
		caller, err := CallerFromContext(ctx)
		require.ErrorIs(t, err, ErrCallerNotInContext, "%s", name)
		require.Nil(t, caller, "%s", name)
	}
}

// TestResolveCallerFailsClosedOnAnUnusableCredential enumerates the
// credential domain ResolveCaller must refuse before any hosted call is
// attempted, plus the live refusal a hosted API returns for a token it does
// not know.
func TestResolveCallerFailsClosedOnAnUnusableCredential(t *testing.T) {
	// Given
	fx := newMultiCallerFixture(t)
	known := fixtureToken(0x33)
	fx.register(known, "caller-known", validCapabilitiesFixture())
	cfg := NewProcessConfig(fixtureConfig(t, fx.Server), testReleaseIdentity(), io.Discard)

	shapeRefused := map[string]string{
		"empty":            "",
		"whitespace":       "   ",
		"wrong prefix":     "bearer_" + strings.Repeat("a", 43),
		"truncated secret": "fcacr_abc",
		"license key":      "dhl_" + strings.Repeat("b", 43),
	}
	require.Len(t, shapeRefused, 5)
	for name, bearer := range shapeRefused {
		// When / Then
		caller, err := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: bearer})
		require.ErrorIs(t, err, ErrCallerCredentialInvalid, "%s", name)
		require.Nil(t, caller, "%s", name)
	}
	require.Zero(t, fx.unauthorizedCount(), "a malformed credential must never reach the hosted API")

	// A shape-valid credential the hosted API rejects is refused too, and
	// no caller context exists for it.
	caller, err := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: fixtureToken(0x44)})
	require.Error(t, err)
	require.Nil(t, caller)
	require.Equal(t, 1, fx.unauthorizedCount())

	// A missing process configuration is refused before anything else.
	caller, err = ResolveCaller(context.Background(), nil, CallerCredential{Bearer: known})
	require.ErrorIs(t, err, ErrProcessConfigMissing)
	require.Nil(t, caller)
}

// TestResolveCallerRunsTheCompatibilityGateAgainstEachCallersOwnSnapshot
// pins that the handshake is decided per caller: one credential the hosted
// API serves an incompatible snapshot for is refused with no caller
// context, while another credential in the same process is unaffected.
//
// Before caller contexts the gate ran once, at boot, against the operator's
// snapshot, so a second credential inherited a verdict that was never about
// it.
func TestResolveCallerRunsTheCompatibilityGateAgainstEachCallersOwnSnapshot(t *testing.T) {
	// Given
	fx := newMultiCallerFixture(t)
	compatible, incompatible := fixtureToken(0x66), fixtureToken(0x77)
	fx.register(compatible, "caller-compatible", validCapabilitiesFixture())
	unentitled := validCapabilitiesFixture()
	unentitled.Entitlements.AgentContextRuntime = false
	fx.register(incompatible, "caller-unentitled", unentitled)
	cfg := NewProcessConfig(fixtureConfig(t, fx.Server), testReleaseIdentity(), io.Discard)

	// When
	good, goodErr := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: compatible})
	bad, badErr := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: incompatible})

	// Then
	require.NoError(t, goodErr)
	require.NotNil(t, good)
	require.NotEmpty(t, good.Capabilities().EnabledTools)
	require.Error(t, badErr)
	require.Contains(t, badErr.Error(), "entitlement")
	require.Nil(t, bad)

	// And the order does not decide it: resolving the unentitled credential
	// first still refuses it and still admits the other.
	bad, badErr = ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: incompatible})
	require.Error(t, badErr)
	require.Nil(t, bad)
	good, goodErr = ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: compatible})
	require.NoError(t, goodErr)
	require.NotEmpty(t, good.Capabilities().EnabledTools)
	require.Zero(t, fx.unauthorizedCount())
}

// TestResolveCallerCarriesAnAuthenticatedPrincipalByValue covers the A5
// reuse path: when the hosting process already authenticated the bearer
// through internal/auth, the caller carries that storage.Principal, and it
// carries a COPY so a later mutation of the transport's own value cannot
// change what the tool layer sees.
func TestResolveCallerCarriesAnAuthenticatedPrincipalByValue(t *testing.T) {
	// Given
	fx := newMultiCallerFixture(t)
	bearer := fixtureToken(0x55)
	fx.register(bearer, "caller-principal", validCapabilitiesFixture())
	cfg := NewProcessConfig(fixtureConfig(t, fx.Server), testReleaseIdentity(), io.Discard)
	principal := storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential,
		Subject:              "cred_1",
		OrgID:                "org_1",
		CredentialID:         "cred_1",
		RepositoryScopes:     []string{"acme/widgets"},
		Permissions:          []string{"context:read", "evidence:read"},
	}

	// When
	caller, err := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: bearer, Principal: &principal})
	require.NoError(t, err)
	principal.OrgID = "org_mutated"
	principal.RepositoryScopes[0] = "other/repo"

	// Then
	carried, ok := caller.Principal()
	require.True(t, ok)
	require.Equal(t, "org_1", carried.OrgID)
	require.Equal(t, []string{"acme/widgets"}, carried.RepositoryScopes)

	// And a caller resolved without one reports absence rather than a zero
	// principal that would read as an authenticated identity.
	anonymous, err := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: bearer})
	require.NoError(t, err)
	_, ok = anonymous.Principal()
	require.False(t, ok)
}

// connectedClientForCaller connects an in-memory MCP client to a server
// built for one caller, so tools/list is read over the real wire.
func connectedClientForCaller(t *testing.T, cfg *ProcessConfig, caller *CallerContext) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := NewServerForCaller(cfg, caller, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	return clientSession
}

func listedToolNames(t *testing.T, session *mcpsdk.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	require.NotEmpty(t, names, "a caller's tool catalogue must never be empty")
	return names
}

// TestToolCatalogueFollowsTheCallersOwnCapabilities is the A2 pin: in one
// process, tools/list describes what THIS credential can do, not what the
// process operator could do.
func TestToolCatalogueFollowsTheCallersOwnCapabilities(t *testing.T) {
	// Given
	_, cfg, callerA, callerB, _, _ := twoCallerProcess(t)

	// When
	namesA := listedToolNames(t, connectedClientForCaller(t, cfg, callerA))
	namesB := listedToolNames(t, connectedClientForCaller(t, cfg, callerB))

	// Then
	require.ElementsMatch(t, []string{toolContextForTask, toolSourceEvidence, toolInvestigateQuestion, toolInvestigationResult}, namesA)
	require.ElementsMatch(t, []string{toolContextForTask, toolSourceEvidence}, namesB)
	require.NotEqual(t, len(namesA), len(namesB), "two capability sets produced the same catalogue")
}

// TestAServerBuiltForOneCallerBindsThatCallerToEveryToolCall pins the
// binding itself end to end over the wire: the tool answer a session
// returns is the answer that caller's own credential earns, which is what
// the getServer(*http.Request) hook depends on in the stateless model.
func TestAServerBuiltForOneCallerBindsThatCallerToEveryToolCall(t *testing.T) {
	// Given
	fx, cfg, callerA, callerB, _, _ := twoCallerProcess(t)
	sessionA := connectedClientForCaller(t, cfg, callerA)
	sessionB := connectedClientForCaller(t, cfg, callerB)

	// When
	excerpts := map[string]string{}
	for label, session := range map[string]*mcpsdk.ClientSession{"caller-a": sessionA, "caller-b": sessionB} {
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
			Name:      toolSourceEvidence,
			Arguments: map[string]any{"evidence_ref_id": "evidence_wire_id"},
		})
		require.NoError(t, err)
		require.False(t, result.IsError, "%s: %s", label, toolResultText(result))
		var response contractsv1.MCPSourceEvidenceResponse
		encoded, err := json.Marshal(result.StructuredContent)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(encoded, &response))
		excerpts[label] = response.Structured.Excerpt
	}

	// Then
	require.Len(t, excerpts, 2)
	require.Equal(t, "caller-a excerpt", excerpts["caller-a"])
	require.Equal(t, "caller-b excerpt", excerpts["caller-b"])
	require.Zero(t, fx.unauthorizedCount())
}
