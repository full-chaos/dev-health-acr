package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/full-chaos/dev-health-acr/internal/version"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Repository scopes the hosted fixture reads to shape one credential's
// capability answer. They stand in for the organization/entitlement facts
// the real hosted API derives per principal.
const (
	repoAnswers      = "acme/answers"
	repoIncompatible = "acme/incompatible"
	repoPlain        = "acme/plain"
	repoUpgrade      = "acme/upgrade"
	evidenceMissing  = "evidence_missing"
)

// testIdentity is the build identity the hosted endpoint reports.
var testIdentity = version.Info{Version: "9.8.7", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-09-21T00:00:00Z"}

// hostedAPI is an in-process hosted API whose credential decisions are made
// by the REAL internal/auth Authenticator over a real (memory) credential
// store: unknown, expired, revoked and insufficient-scope credentials take
// exactly the production code path. Every payload it returns names the
// credential it was produced for, and it records which credential asked for
// which path, so identity is observed on the wire, not argued.
type hostedAPI struct {
	t       *testing.T
	server  *httptest.Server
	store   *storage.CredentialLifecycle
	service *auth.Service
	caPath  string

	blocked      atomic.Bool
	liveStatus   atomic.Int32
	evidenceWait atomic.Pointer[barrier]

	mu        sync.Mutex
	seen      map[string][]string
	forwarded []string
}

// recordForwarded notes the X-Forwarded-For each capabilities request arrived
// with, so a test observes what acr-mcp put on the wire.
func (h *hostedAPI) recordForwarded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.forwarded = append(h.forwarded, r.Header.Get("X-Forwarded-For"))
		h.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (h *hostedAPI) forwardedFor() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.forwarded)
}

// barrier holds every evidence request until n have arrived at once.
type barrier struct {
	mu      sync.Mutex
	n       int
	arrived int
	release chan struct{}
	maxSeen int
}

func newBarrier(n int) *barrier { return &barrier{n: n, release: make(chan struct{})} }

func (b *barrier) wait() {
	b.mu.Lock()
	b.arrived++
	if b.arrived > b.maxSeen {
		b.maxSeen = b.arrived
	}
	if b.arrived == b.n {
		close(b.release)
	}
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-time.After(10 * time.Second):
	}
}

type switchLimiter struct{ blocked *atomic.Bool }

func (l switchLimiter) AllowAttempt(string, time.Time) bool   { return true }
func (l switchLimiter) FailureBlocked(string, time.Time) bool { return l.blocked.Load() }
func (l switchLimiter) BeginAttempt(string, time.Time) (func(), bool) {
	return func() {}, !l.blocked.Load()
}
func (l switchLimiter) RecordFailure(string, time.Time) {}
func (l switchLimiter) RetryAfter(string, time.Time) time.Duration {
	return 7 * time.Second
}

// hostedOptions gives the fixture acr-api a real per-address gate. The zero
// value keeps the switchLimiter every other test uses.
type hostedOptions struct {
	Limiter           auth.AttemptLimiter
	TrustedProxyCIDRs []string
}

func newHostedAPI(t *testing.T) *hostedAPI { return newHostedAPIWith(t, hostedOptions{}) }

func newHostedAPIWith(t *testing.T, opts hostedOptions) *hostedAPI {
	t.Helper()
	issuedAt := time.Now().Add(-2 * time.Hour)
	store, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: memory.NewAuditStore(), Now: func() time.Time { return issuedAt }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(store, auth.ServiceOptions{Now: func() time.Time { return issuedAt }})
	if err != nil {
		t.Fatal(err)
	}
	h := &hostedAPI{t: t, store: store, service: service, seen: map[string][]string{}}
	var limiter auth.AttemptLimiter = switchLimiter{blocked: &h.blocked}
	if opts.Limiter != nil {
		limiter = opts.Limiter
	}
	authOptions := auth.AuthenticatorOptions{Limiter: limiter, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	if opts.TrustedProxyCIDRs != nil {
		resolver, err := auth.NewTrustedProxyClientIPResolver(opts.TrustedProxyCIDRs)
		if err != nil {
			t.Fatal(err)
		}
		authOptions.ClientIP = resolver
	}
	authenticator, err := auth.NewAuthenticator(store, memory.NewAuditStore(), authOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		status := int(h.liveStatus.Load())
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	})
	mux.Handle("GET /api/v1/agent-context/capabilities", h.recordForwarded(authenticator.Middleware(authenticator.RequireScope(auth.ScopeContextRead, http.HandlerFunc(h.capabilities)))))
	mux.Handle("GET /api/v1/agent-context/evidence/{id}", authenticator.Middleware(authenticator.RequireScope(auth.ScopeEvidenceRead, http.HandlerFunc(h.evidence))))
	h.server = httptest.NewTLSServer(mux)
	t.Cleanup(h.server.Close)

	h.caPath = filepath.Join(t.TempDir(), "hosted-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.server.Certificate().Raw})
	if err := os.WriteFile(h.caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *hostedAPI) record(principal storage.Principal, path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen[principal.CredentialID] = append(h.seen[principal.CredentialID], path)
}

func (h *hostedAPI) pathsSeenBy(credentialID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.seen[credentialID])
}

func (h *hostedAPI) capabilities(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.PrincipalFromContext(r.Context())
	h.record(principal, r.URL.Path)
	if slices.Contains(principal.RepositoryScopes, repoUpgrade) {
		writeHostedJSON(w, http.StatusUpgradeRequired, contractsv1.ErrorEnvelope{
			SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_fixture",
			Error: contractsv1.ErrorDetail{Code: "version_mismatch", Message: "upgrade", HTTPStatus: http.StatusUpgradeRequired, Details: map[string]any{"minimum_client_version": "99.0.0"}},
		})
		return
	}
	caps := contractsv1.Capabilities{
		SchemaVersion:           contractsv1.CapabilitiesSchema,
		Service:                 "dev-health-acr",
		ServiceVersion:          "1.2.3",
		MinimumSidecarVersion:   "0.1.0",
		SupportedSchemaVersions: acrmcp.OurSchemaVersionsForTest,
		EnabledTools:            []string{acrmcp.ToolContextForTaskForTest, acrmcp.ToolSourceEvidenceForTest},
		Entitlements:            contractsv1.CapabilityEntitlements{AgentContextRuntime: true},
		Permissions: contractsv1.CapabilityPermissions{
			ContextRead:  auth.HasScope(principal.Permissions, auth.ScopeContextRead),
			EvidenceRead: auth.HasScope(principal.Permissions, auth.ScopeEvidenceRead),
		},
		Limits:      contractsv1.CapabilityLimits{MaxItems: 30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60},
		GeneratedAt: time.Now().UTC(),
	}
	if slices.Contains(principal.RepositoryScopes, repoAnswers) {
		caps.EnabledTools = append(caps.EnabledTools, acrmcp.ToolInvestigateQuestionForTest, acrmcp.ToolInvestigationResultForTest)
	}
	if slices.Contains(principal.RepositoryScopes, repoIncompatible) {
		caps.MinimumSidecarVersion = "99.0.0"
	}
	writeHostedJSON(w, http.StatusOK, caps)
}

func (h *hostedAPI) evidence(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.PrincipalFromContext(r.Context())
	h.record(principal, r.URL.Path)
	if b := h.evidenceWait.Load(); b != nil {
		b.wait()
	}
	id := r.PathValue("id")
	if id == evidenceMissing {
		writeHostedJSON(w, http.StatusNotFound, contractsv1.ErrorEnvelope{
			SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_fixture",
			Error: contractsv1.ErrorDetail{Code: "not_found", Message: "not found", HTTPStatus: http.StatusNotFound},
		})
		return
	}
	now := time.Now().UTC()
	writeHostedJSON(w, http.StatusOK, contractsv1.ExpandedEvidence{
		SchemaVersion: contractsv1.ExpandedEvidenceSchema,
		Evidence: contractsv1.EvidenceRef{
			SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: id,
			Source:     contractsv1.EvidenceSource{System: "github_actions", EntityType: "workflow_run", EntityID: "12345", DisplayLabel: "CI run #12345"},
			Provenance: "heuristic", Confidence: 0.9, Citation: "log line 42", ObservedAt: now, Availability: contractsv1.EvidenceAvailable,
		},
		ResolvedAt:   now,
		Excerpt:      "excerpt-for-" + principal.CredentialID,
		Availability: contractsv1.EvidenceAvailable,
		Structured:   map[string]any{"attempt": 3},
	})
}

func writeHostedJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// issued is one credential the hosted fixture knows.
type issued struct {
	token        string
	credentialID string
}

func (h *hostedAPI) issue(scopes, repositories []string, expires *time.Time) issued {
	h.t.Helper()
	credential, err := h.service.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: "org_1", Name: "mcp-http-test", RepositoryScopes: repositories, Scopes: scopes, CreatedBy: "test_actor", ExpiresAt: expires,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return issued{token: credential.Token, credentialID: credential.Credential.CredentialID}
}

func (h *hostedAPI) revoke(c issued) {
	h.t.Helper()
	if _, err := h.store.RevokeCredential(context.Background(), storage.CredentialRevocationInput{OrgID: "org_1", CredentialID: c.credentialID, ActorID: "admin"}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *hostedAPI) sidecarConfig() sidecar.Config {
	h.t.Helper()
	base, err := url.Parse(h.server.URL)
	if err != nil {
		h.t.Fatal(err)
	}
	return sidecar.Config{
		APIBaseURL:            base,
		Timeout:               5 * time.Second,
		MaxResponseBytes:      1 << 20,
		MaxRequestBodyBytes:   256 << 10,
		ClientName:            "test-sidecar",
		ClientVersion:         "1.0.0",
		SidecarVersion:        "1.0.0",
		CACertPath:            h.caPath,
		AllowInsecureLoopback: true,
	}
}

// syncBuffer is a goroutine-safe log sink: the endpoint writes its lines
// from request goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.buf.Bytes())
}

// endpoint is one hosted MCP endpoint under test, served by httptest.
type endpoint struct {
	handler *acrmcp.HTTPHandler
	server  *httptest.Server
	logs    *syncBuffer
	sdkHits atomic.Int64
}

func (e *endpoint) url() string { return e.server.URL + "/mcp" }

// newEndpoint builds the endpoint exactly as ServeHTTPTransport does, with
// the SDK handler wrapped by a counter so a test can prove a refused request
// never reached it.
func newEndpoint(t *testing.T, hosted *hostedAPI) *endpoint {
	return newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{})
}

func newEndpointWithGate(t *testing.T, hosted *hostedAPI, gate acrmcp.EdgeGateOptions) *endpoint {
	t.Helper()
	logs := &syncBuffer{}
	cfg, err := acrmcp.NewHTTPProcessConfig(hosted.sidecarConfig(), testIdentity, logs)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := acrmcp.NewHTTPHandler(cfg, acrmcp.HTTPHandlerOptions{
		BasePath: "/mcp", Identity: testIdentity, MaxRequestBodyBytes: 1 << 20, ResolveTimeout: 5 * time.Second, EdgeGate: gate,
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{handler: handler, logs: logs}
	acrmcp.WrapHTTPHandlerSDKForTest(handler, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.sdkHits.Add(1)
			next.ServeHTTP(w, r)
		})
	})
	e.server = httptest.NewServer(handler)
	t.Cleanup(e.server.Close)
	return e
}

// headerTransport adds the caller's bearer (and any fixed headers) to every
// request an MCP client makes, and records every response's session header.
type headerTransport struct {
	bearer     string
	headers    http.Header
	mu         sync.Mutex
	sessionIDs []string
	responses  int
}

func (h *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if h.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+h.bearer)
	}
	for name, values := range h.headers {
		for _, value := range values {
			r.Header.Add(name, value)
		}
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		h.mu.Lock()
		h.responses++
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			h.sessionIDs = append(h.sessionIDs, id)
		}
		h.mu.Unlock()
	}
	return resp, err
}

func connectClient(t *testing.T, e *endpoint, rt *headerTransport) *mcpsdk.ClientSession {
	t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
		Endpoint: e.url(), HTTPClient: &http.Client{Transport: rt}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// rawToolsList is a 2026-07-28 tools/list request as a client puts it on the
// wire, for tests that must drive the endpoint below the SDK client.
func rawToolsList() []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{%q:"2026-07-28",%q:{}}}}`,
		mcpsdk.MetaKeyProtocolVersion, mcpsdk.MetaKeyClientCapabilities))
}

func rawToolsCall(name string, args map[string]any) []byte {
	encoded, _ := json.Marshal(args)
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s,"_meta":{%q:"2026-07-28",%q:{}}}}`,
		name, encoded, mcpsdk.MetaKeyProtocolVersion, mcpsdk.MetaKeyClientCapabilities))
}

func postMCP(t *testing.T, e *endpoint, method string, body []byte, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.url(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	var envelope struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
			URI  string `json:"uri"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Method != "" {
		req.Header.Set("Mcp-Method", envelope.Method)
		switch {
		case envelope.Params.Name != "":
			req.Header.Set("Mcp-Name", envelope.Params.Name)
		case envelope.Params.URI != "":
			req.Header.Set("Mcp-Name", envelope.Params.URI)
		}
	}
	for name, values := range header {
		req.Header.Del(name)
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func bearerHeader(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}}
}

func withRequestID(h http.Header, id string) http.Header {
	out := h.Clone()
	if out == nil {
		out = http.Header{}
	}
	out.Set("X-Request-ID", id)
	return out
}

func expandedEvidenceExcerpt(t *testing.T, result *mcpsdk.CallToolResult) string {
	t.Helper()
	if result.IsError {
		var text string
		for _, c := range result.Content {
			if tc, ok := c.(*mcpsdk.TextContent); ok {
				text += tc.Text
			}
		}
		t.Errorf("tool error: %s", text)
		return ""
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Error(err)
		return ""
	}
	var response contractsv1.MCPSourceEvidenceResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Error(err)
		return ""
	}
	return response.Structured.Excerpt
}

func toolNames(t *testing.T, session *mcpsdk.ClientSession) []string {
	t.Helper()
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%q does not contain %q", haystack, needle)
	}
}
