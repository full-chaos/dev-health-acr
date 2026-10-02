package mcp_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// The OAuth login runs end to end here: a real acr-api composition (the
// OAuth routes, the device approval route with web-assertion
// authentication, the authenticator and the context-packet route) behind
// TLS, the real hosted acr-mcp endpoint built exactly as `acr-mcp serve`
// builds it, and the go-sdk's own MCP OAuth client (AuthorizationCodeHandler),
// which knows nothing about this server: it follows the 401 challenge to the
// protected resource metadata, discovers the authorization server, registers
// dynamically or presents a metadata-document client ID, sends PKCE and the resource
// indicator, and exchanges the code. Only the browser and the web consent
// page are scripted (a stub consent approver): the fetcher follows /authorize
// to the consent page URL, reads the request and approves or denies it on the
// consent route the way the web consent page does (a signed web assertion for
// the signed-in user), and follows the redirect_url it gets back.

const (
	oauthLoginRedirect   = "http://127.0.0.1:47111/callback"
	oauthLoginConsentURL = "https://web.example.test/acr/authorize"
)

// oauthClock is the acr-api clock; tests move it to cross an expiry.
type oauthClock struct{ offset atomic.Int64 }

func (c *oauthClock) now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *oauthClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

type oauthStack struct {
	t          *testing.T
	clock      *oauthClock
	api        *httptest.Server
	caPath     string
	private    ed25519.PrivateKey
	store      *storage.CredentialLifecycle
	apiLogs    *syncBuffer
	mcp        *httptest.Server
	mcpURL     string
	mcpHandler *acrmcp.HTTPHandler
	mcpLogs    *syncBuffer
	approve    atomic.Bool
	deny       atomic.Bool
}

type swapHandler struct {
	mu sync.RWMutex
	h  http.Handler
}

func (s *swapHandler) set(h http.Handler) { s.mu.Lock(); s.h = h; s.mu.Unlock() }
func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	h := s.h
	s.mu.RUnlock()
	h.ServeHTTP(w, r)
}

// newOAuthStack builds acr-api (issuing for extraResources as well as the
// endpoint's own URL) and the endpoint.
func newOAuthStack(t *testing.T, extraResources ...string) *oauthStack {
	t.Helper()
	s := &oauthStack{t: t, clock: &oauthClock{}, apiLogs: &syncBuffer{}, mcpLogs: &syncBuffer{}}
	s.approve.Store(true)

	apiSwap := &swapHandler{h: http.NotFoundHandler()}
	s.api = httptest.NewTLSServer(apiSwap)
	t.Cleanup(s.api.Close)
	s.caPath = filepath.Join(t.TempDir(), "oauth-ca.pem")
	if err := os.WriteFile(s.caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.api.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	mcpSwap := &swapHandler{h: http.NotFoundHandler()}
	s.mcp = httptest.NewServer(mcpSwap)
	t.Cleanup(s.mcp.Close)
	s.mcpURL = s.mcp.URL + "/mcp"

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s.private = private
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "kid": "current", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(public)}}})
	if err != nil {
		t.Fatal(err)
	}
	jwksPath := filepath.Join(t.TempDir(), "web.jwks.json")
	if err := os.WriteFile(jwksPath, jwks, 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: jwksPath, Now: s.clock.now})
	if err != nil {
		t.Fatal(err)
	}

	audit := memory.NewAuditStore()
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: s.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	s.store = credentials
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: s.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := evalfixture.VerifyCorpus(repopath.Path(t, "testdata", "evaluation", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	evaluation, err := contextpacket.NewEvaluationStore(corpus, orgOne)
	if err != nil {
		t.Fatal(err)
	}
	assembler := contextpacket.NewAssembler(evaluation, contextpacket.Options{Now: time.Now, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0"})
	manager, err := limits.NewManager(limits.Options{Now: time.Now, PerOrgConcurrency: 64, Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100_000},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100_000, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 100_000},
	}})
	if err != nil {
		t.Fatal(err)
	}

	metadataClient := s.api.Client()
	app, err := api.NewApp(api.AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: 30 * time.Second}, api.Dependencies{
		Capabilities: api.StaticCapabilitiesProvider{Now: time.Now, Value: contractsv1.Capabilities{
			SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: "1.2.3", MinimumSidecarVersion: "1.0.0",
			SupportedSchemaVersions: contractsv1.AllSchemaVersions,
			Limits:                  contractsv1.CapabilityLimits{MaxItems: 30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60},
		}},
		Limits: manager, Now: s.clock.now, WebAssertions: verifier,
		Runtime: &api.RuntimeDependencies{
			Credentials: credentials, Audit: audit,
			Entitlements: api.EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
			Assembler:    assembler, Evidence: evaluation,
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://web.example.test/acr/device",
			DeviceAuthorizationLimiter: api.NewDeviceAuthorizationLimiter(api.ClockFunc(time.Now)),
			ReadinessChecks:            []api.ReadinessCheck{api.CheckFunc{CheckName: "postgres"}, api.CheckFunc{CheckName: "entitlement"}},
			DataStoreChecks:            []api.ReadinessCheck{api.CheckFunc{CheckName: "clickhouse"}},
			OAuth: &api.OAuthRuntime{
				Store: memory.NewOAuthStore(s.clock.now), Issuer: s.api.URL,
				Resources:      append([]string{s.mcpURL}, extraResources...),
				ClientMetadata: auth.NewClientMetadataFetcher(metadataClient),
				ConsentURL:     oauthLoginConsentURL,
			},
		},
	}, slog.New(slog.NewJSONHandler(s.apiLogs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	front := http.NewServeMux()
	front.HandleFunc("GET /cimd/client.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id": s.api.URL + "/cimd/client.json", "client_name": "Metadata Client", "redirect_uris": []string{oauthLoginRedirect},
			"grant_types": []string{"authorization_code"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none",
		})
	})
	front.Handle("/", app.Handler())
	apiSwap.set(front)

	s.mcpHandler = s.newEndpoint(s.mcpURL)
	mcpSwap.set(s.mcpHandler)
	return s
}

func (s *oauthStack) sidecarConfig() sidecar.Config {
	base, err := url.Parse(s.api.URL)
	if err != nil {
		s.t.Fatal(err)
	}
	return sidecar.Config{
		APIBaseURL: base, Timeout: 30 * time.Second, MaxResponseBytes: 1 << 20, MaxRequestBodyBytes: 256 << 10,
		ClientName: "test-sidecar", ClientVersion: "1.0.0", SidecarVersion: "1.0.0",
		CACertPath: s.caPath, AllowInsecureLoopback: true,
	}
}

// newEndpoint builds the endpoint through the serve path for a resource URL.
func (s *oauthStack) newEndpoint(resourceURL string) *acrmcp.HTTPHandler {
	s.t.Helper()
	opts := acrmcp.DefaultServeOptions()
	opts.Transport = acrmcp.TransportHTTP
	opts.ResourceURL = resourceURL
	opts.AuthorizationServer = s.api.URL
	handler, err := acrmcp.NewServeHTTPHandler(s.sidecarConfig(), testIdentity, s.mcpLogs, opts)
	if err != nil {
		s.t.Fatal(err)
	}
	return handler
}

// fetcher is the scripted browser plus a stub of the web consent page:
// /authorize must send the browser straight to the consent page with a
// handle; the stub reads the request and approves (or denies) it for the
// signed-in user, and the browser follows the redirect_url it returns.
func (s *oauthStack) fetcher(repositories []string) sdkauth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
		client := s.api.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		page, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode == http.StatusSeeOther {
			return resultFromRedirect(response.Header.Get("Location"))
		}
		if response.StatusCode != http.StatusFound {
			return nil, errors.New("authorize answered " + response.Status + ": " + string(page))
		}
		location, err := url.Parse(response.Header.Get("Location"))
		if err != nil {
			return nil, err
		}
		if location.Scheme+"://"+location.Host+location.Path != oauthLoginConsentURL || location.Query().Get("handle") == "" {
			return nil, errors.New("authorize did not send the browser to the consent page: " + location.String())
		}
		handle := location.Query().Get("handle")
		status, preview := s.consent(ctx, map[string]any{"action": "preview", "handle": handle}, []string{"*"})
		if status != http.StatusOK || preview.RedirectURL != "" {
			return nil, errors.New("consent preview answered " + http.StatusText(status) + " " + preview.Error)
		}
		var decision consentReply
		switch {
		case s.deny.Load():
			status, decision = s.consent(ctx, map[string]any{"action": "deny", "handle": handle}, []string{"*"})
		case s.approve.Load():
			status, decision = s.consent(ctx, map[string]any{"action": "approve", "handle": handle, "repository_scopes": repositories}, repositories)
		default:
			return nil, errors.New("the consent was left undecided")
		}
		if status != http.StatusOK || decision.RedirectURL == "" {
			return nil, errors.New("consent decision answered " + http.StatusText(status) + " " + decision.Error)
		}
		return resultFromRedirect(decision.RedirectURL)
	}
}

type consentReply struct {
	RedirectURL string `json:"redirect_url"`
	ClientName  string `json:"client_name"`
	Error       string `json:"error"`
}

// consent posts to the consent route exactly as the web consent page does:
// a JSON body and a web assertion for the signed-in user of orgOne granting
// these repositories.
func (s *oauthStack) consent(ctx context.Context, body map[string]any, grant []string) (int, consentReply) {
	s.t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		s.t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.api.URL+api.OAuthConsentPath, bytes.NewReader(encoded))
	if err != nil {
		s.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WebAssertionHeader, s.webAssertion(api.OAuthConsentPath, encoded, grant))
	response, err := s.api.Client().Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	var reply consentReply
	_ = json.NewDecoder(response.Body).Decode(&reply)
	return response.StatusCode, reply
}

func resultFromRedirect(location string) (*sdkauth.AuthorizationResult, error) {
	parsed, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	if query.Get("error") != "" {
		return nil, errors.New("authorization refused: " + query.Get("error"))
	}
	return &sdkauth.AuthorizationResult{Code: query.Get("code"), State: query.Get("state"), Iss: query.Get("iss")}, nil
}

// webAssertion signs the assertion the web sends for its signed-in user.
func (s *oauthStack) webAssertion(path string, body []byte, grant []string) string {
	s.t.Helper()
	digest := sha256.Sum256(body)
	now := s.clock.now()
	var jti [8]byte
	_, _ = rand.Read(jti[:])
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_oauth", "org_id": orgOne,
		"repository_scopes": grant, "permissions": []string{auth.WebAssertionPermissionCredentialIssue},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": base64.RawURLEncoding.EncodeToString(jti[:]),
		"method": http.MethodPost, "path": path, "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
	payload, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.private, []byte(input)))
}

// oauthHandler is the go-sdk MCP OAuth client, registering dynamically.
func (s *oauthStack) oauthHandler(repositories []string) *sdkauth.AuthorizationCodeHandler {
	s.t.Helper()
	handler, err := sdkauth.NewAuthorizationCodeHandler(&sdkauth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &sdkauth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName: "Claude Code (acr-test)", RedirectURIs: []string{oauthLoginRedirect},
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, TokenEndpointAuthMethod: "none",
		}},
		RedirectURL:              oauthLoginRedirect,
		AuthorizationCodeFetcher: s.fetcher(repositories),
		Client:                   s.api.Client(),
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return handler
}

func (s *oauthStack) connect(handler sdkauth.OAuthHandler) (*mcpsdk.ClientSession, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "oauth-login-test", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: s.mcpURL, OAuthHandler: handler, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Timeout: time.Minute},
	}, &mcpsdk.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
}

// tokenOf returns the access token the handler holds.
func tokenOf(t *testing.T, handler *sdkauth.AuthorizationCodeHandler) string {
	t.Helper()
	source, err := handler.TokenSource(context.Background())
	if err != nil || source == nil {
		t.Fatalf("handler holds no token source: %v", err)
	}
	token, err := source.Token()
	if err != nil {
		t.Fatal(err)
	}
	return token.AccessToken
}

// rawMCP posts one JSON-RPC request with a bearer and returns status and
// challenge.
func (s *oauthStack) rawMCP(bearer string) (int, string) {
	s.t.Helper()
	request, err := http.NewRequest(http.MethodPost, s.mcpURL, bytes.NewReader(rawToolsList()))
	if err != nil {
		s.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "tools/list")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode, response.Header.Get("WWW-Authenticate")
}

func (s *oauthStack) credentialFor(t *testing.T, token string) contractsv1.ClientCredential {
	t.Helper()
	credential, err := s.store.FindByTokenHash(context.Background(), auth.HashToken(token))
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func (s *oauthStack) oauthLines(t *testing.T) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range bytes.Split(s.apiLogs.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			continue
		}
		if line["msg"] == "acr-api oauth step" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestOAuthLoginEndToEnd(t *testing.T) {
	s := newOAuthStack(t)

	// 1. No credential: 401 with the resource_metadata challenge.
	status, challenge := s.rawMCP("")
	wantMetadata := s.mcp.URL + acrmcp.ProtectedResourceMetadataPath + "/mcp"
	if status != http.StatusUnauthorized || !strings.Contains(challenge, `resource_metadata="`+wantMetadata+`"`) {
		t.Fatalf("unauthenticated: status %d challenge %q, want 401 with resource_metadata %s", status, challenge, wantMetadata)
	}

	// 2. The go-sdk client logs in and uses the tools.
	handler := s.oauthHandler([]string{repoWidget})
	session, err := s.connect(handler)
	if err != nil {
		t.Fatalf("OAuth login + connect: %v", err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("tools/list returned no tools after login")
	}
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "context_for_task", Arguments: map[string]any{
		"goal": "fix the checkout flow", "repository": map[string]any{"slug": repoWidget}, "scope": map[string]any{"branch": corpusBranch, "commit_sha": corpusCommit},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("context_for_task after login answered a tool error: %s", toolText(result))
	}

	// 3. The credential is the approved grant, bound to this resource, 30 days.
	token := tokenOf(t, handler)
	if !auth.IsTokenShapeValid(token) {
		t.Fatal("the login did not issue an ACR credential")
	}
	credential := s.credentialFor(t, token)
	if credential.OrgID != orgOne || len(credential.RepositoryScopes) != 1 || credential.RepositoryScopes[0] != repoWidget {
		t.Fatalf("credential grant = org %s repos %v, want %s [%s]", credential.OrgID, credential.RepositoryScopes, orgOne, repoWidget)
	}
	if credential.Resource != s.mcpURL {
		t.Fatalf("credential resource = %q, want %q", credential.Resource, s.mcpURL)
	}
	if credential.ExpiresAt == nil || credential.ExpiresAt.Sub(s.clock.now()) < 29*24*time.Hour || credential.ExpiresAt.Sub(s.clock.now()) > 30*24*time.Hour {
		t.Fatalf("credential expiry = %v, want 30 days", credential.ExpiresAt)
	}

	// 4. A repository outside the grant is refused with the same credential.
	outside, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "context_for_task", Arguments: map[string]any{
		"goal": "fix the checkout flow", "repository": map[string]any{"slug": repoOther}, "scope": map[string]any{"branch": corpusBranch, "commit_sha": corpusCommit},
	}})
	if err == nil && !outside.IsError {
		t.Fatal("a repository outside the approved grant was served")
	}

	// 5. Revocation is live: the next request is 401.
	if status, _ := s.rawMCP(token); status != http.StatusOK {
		t.Fatalf("bearer before revocation: status %d, want 200", status)
	}
	if _, err := s.store.RevokeCredential(context.Background(), storage.CredentialRevocationInput{OrgID: orgOne, CredentialID: credential.CredentialID, ActorID: "admin"}); err != nil {
		t.Fatal(err)
	}
	if status, _ := s.rawMCP(token); status != http.StatusUnauthorized {
		t.Fatalf("revoked bearer: status %d, want 401", status)
	}

	// 6. Telemetry rebuilds the login: register, authorize (the redirect to
	// the consent page), the consent page's read, the approval, token ok,
	// all from a dynamic client.
	var steps []string
	for _, line := range s.oauthLines(t) {
		steps = append(steps, line["step"].(string)+":"+line["outcome"].(string))
	}
	want := []string{"register:ok", "authorize:ok", "consent_preview:ok", "consent:ok", "token:ok"}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("oauth telemetry steps = %v, want %v", steps, want)
	}
	for _, line := range s.oauthLines(t) {
		scopes, _ := json.Marshal(line["scopes"])
		wantScopes := `[]`
		if line["outcome"] == "ok" && (line["step"] == "authorize" || line["step"] == "token") {
			wantScopes = `["context:read","evidence:read","data:read"]`
		}
		if string(scopes) != wantScopes {
			t.Fatalf("%s:%s scopes = %s, want %s", line["step"], line["outcome"], scopes, wantScopes)
		}
	}
	logs := string(s.apiLogs.Bytes())
	if strings.Contains(logs, token) {
		t.Fatal("acr-api logs carry the issued access token")
	}
}

// A denial on the consent page sends the browser back with access_denied:
// the client never gets a code and never connects.
func TestOAuthLoginDeniedNeverConnects(t *testing.T) {
	s := newOAuthStack(t)
	s.deny.Store(true)
	if session, err := s.connect(s.oauthHandler([]string{repoWidget})); err == nil {
		session.Close()
		t.Fatal("a denied login connected")
	} else if !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("denied login error = %v, want access_denied", err)
	}
	assertLastOutcomes(t, s, "consent", []string{"access_denied"})
	for _, line := range s.oauthLines(t) {
		if line["step"] == "token" {
			t.Fatalf("a denied login reached the token endpoint: %v", line)
		}
	}
}

func TestOAuthLoginExpiredCredentialIsRefused(t *testing.T) {
	s := newOAuthStack(t)
	handler := s.oauthHandler([]string{repoWidget})
	session, err := s.connect(handler)
	if err != nil {
		t.Fatal(err)
	}
	session.Close()
	token := tokenOf(t, handler)
	if status, _ := s.rawMCP(token); status != http.StatusOK {
		t.Fatalf("fresh credential: status %d, want 200", status)
	}
	s.clock.advance(30*24*time.Hour + time.Minute)
	if status, _ := s.rawMCP(token); status != http.StatusUnauthorized {
		t.Fatalf("expired credential: status %d, want 401", status)
	}
}

func TestOAuthLoginCredentialForAnotherResourceIsRefused(t *testing.T) {
	other := "https://other-mcp.example.test/mcp"
	s := newOAuthStack(t, other)
	// A login for the other resource: the same authorization server, the
	// same user, a credential bound to the other endpoint.
	token := s.manualLogin(t, other, true)
	credential := s.credentialFor(t, token)
	if credential.Resource != other {
		t.Fatalf("credential resource = %q, want %q", credential.Resource, other)
	}
	if status, challenge := s.rawMCP(token); status != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) {
		t.Fatalf("credential for another resource at this endpoint: status %d challenge %q, want 401 invalid_token", status, challenge)
	}
	// Control: the same flow bound to this endpoint is admitted.
	own := s.manualLogin(t, s.mcpURL, true)
	if status, _ := s.rawMCP(own); status != http.StatusOK {
		t.Fatalf("credential for this endpoint: status %d, want 200", status)
	}
}

// manualLogin runs the flow by hand (DCR, authorize, approve, token) for a
// resource and returns the access token. validVerifier false sends a verifier
// that does not match the challenge.
func (s *oauthStack) manualLogin(t *testing.T, resource string, validVerifier bool) string {
	t.Helper()
	token, status, body := s.manualLoginRaw(t, resource, resource, validVerifier)
	if status != http.StatusOK {
		t.Fatalf("token endpoint answered %d: %s", status, body)
	}
	return token
}

func (s *oauthStack) register(t *testing.T) string {
	t.Helper()
	registration, _ := json.Marshal(map[string]any{"client_name": "manual", "redirect_uris": []string{oauthLoginRedirect}, "token_endpoint_auth_method": "none"})
	response, err := s.api.Client().Post(s.api.URL+api.OAuthRegisterPath, "application/json", bytes.NewReader(registration))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var registered struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&registered); err != nil || response.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %v", response.StatusCode, err)
	}
	return registered.ClientID
}

func pkcePair() (verifier, challenge string) {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	verifier = base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:])
}

// authorizeAndApprove runs /authorize for a client and resource, approves the
// consent and returns the code.
func (s *oauthStack) authorizeAndApprove(t *testing.T, clientID, resource, challenge string) string {
	t.Helper()
	query := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {oauthLoginRedirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {resource}, "state": {"state-1"},
	}
	result, err := s.fetcher([]string{repoWidget})(context.Background(), &sdkauth.AuthorizationArgs{URL: s.api.URL + api.OAuthAuthorizePath + "?" + query.Encode()})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "state-1" || result.Iss != s.api.URL {
		t.Fatalf("redirect state %q iss %q, want state-1 and %s", result.State, result.Iss, s.api.URL)
	}
	return result.Code
}

func (s *oauthStack) exchange(t *testing.T, values url.Values) (string, int, string) {
	t.Helper()
	response, err := s.api.Client().PostForm(s.api.URL+api.OAuthTokenPath, values)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var body struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(raw, &body)
	return body.AccessToken, response.StatusCode, string(raw)
}

func (s *oauthStack) manualLoginRaw(t *testing.T, authorizeResource, tokenResource string, validVerifier bool) (string, int, string) {
	t.Helper()
	clientID := s.register(t)
	verifier, challenge := pkcePair()
	code := s.authorizeAndApprove(t, clientID, authorizeResource, challenge)
	if !validVerifier {
		verifier, _ = pkcePair()
	}
	return s.exchange(t, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthLoginRedirect},
		"client_id": {clientID}, "code_verifier": {verifier}, "resource": {tokenResource},
	})
}

func TestOAuthTokenRefusesPKCEMismatchAndSpendsTheCode(t *testing.T) {
	s := newOAuthStack(t)
	clientID := s.register(t)
	verifier, challenge := pkcePair()
	code := s.authorizeAndApprove(t, clientID, s.mcpURL, challenge)
	wrong, _ := pkcePair()
	base := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthLoginRedirect}, "client_id": {clientID}, "resource": {s.mcpURL}}
	withVerifier := func(v string) url.Values {
		values := url.Values{}
		for k, vs := range base {
			values[k] = vs
		}
		values.Set("code_verifier", v)
		return values
	}
	if _, status, body := s.exchange(t, withVerifier(wrong)); status != http.StatusBadRequest || !strings.Contains(body, `"invalid_grant"`) {
		t.Fatalf("wrong verifier: %d %s, want 400 invalid_grant", status, body)
	}
	// The refused exchange spent the code: the right verifier is refused too.
	if _, status, body := s.exchange(t, withVerifier(verifier)); status != http.StatusBadRequest || !strings.Contains(body, `"invalid_grant"`) {
		t.Fatalf("right verifier after a refused exchange: %d %s, want 400 invalid_grant", status, body)
	}
	assertLastOutcomes(t, s, "token", []string{"pkce_mismatch", "invalid_grant"})
}

func TestOAuthTokenRefusesResourceMismatch(t *testing.T) {
	other := "https://other-mcp.example.test/mcp"
	s := newOAuthStack(t, other)
	_, status, body := s.manualLoginRaw(t, s.mcpURL, other, true)
	if status != http.StatusBadRequest || !strings.Contains(body, `"invalid_target"`) {
		t.Fatalf("token resource differing from the authorized one: %d %s, want 400 invalid_target", status, body)
	}
	assertLastOutcomes(t, s, "token", []string{"resource_mismatch"})
}

func TestOAuthTokenIsSingleUse(t *testing.T) {
	s := newOAuthStack(t)
	clientID := s.register(t)
	verifier, challenge := pkcePair()
	code := s.authorizeAndApprove(t, clientID, s.mcpURL, challenge)
	values := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthLoginRedirect}, "client_id": {clientID}, "code_verifier": {verifier}, "resource": {s.mcpURL}}
	if _, status, body := s.exchange(t, values); status != http.StatusOK {
		t.Fatalf("first exchange: %d %s", status, body)
	}
	if _, status, body := s.exchange(t, values); status != http.StatusBadRequest || !strings.Contains(body, `"invalid_grant"`) {
		t.Fatalf("second exchange of the same code: %d %s, want 400 invalid_grant", status, body)
	}
}

func TestOAuthAuthorizeRequiresPKCE(t *testing.T) {
	s := newOAuthStack(t)
	clientID := s.register(t)
	for name, query := range map[string]url.Values{
		"no challenge":     {"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {oauthLoginRedirect}, "state": {"x"}},
		"plain method":     {"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {oauthLoginRedirect}, "code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"plain"}, "state": {"x"}},
		"unknown resource": {"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {oauthLoginRedirect}, "code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"}, "resource": {"https://evil.example.test/mcp"}, "state": {"x"}},
	} {
		client := s.api.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Get(s.api.URL + api.OAuthAuthorizePath + "?" + query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		location, _ := url.Parse(response.Header.Get("Location"))
		if response.StatusCode != http.StatusSeeOther || location == nil || location.Query().Get("error") == "" || location.Query().Get("state") != "x" {
			t.Fatalf("%s: status %d location %q, want a 303 error redirect carrying state", name, response.StatusCode, response.Header.Get("Location"))
		}
	}
	// An unregistered redirect URI is never redirected to.
	client := s.api.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	_, challenge := pkcePair()
	response, err := client.Get(s.api.URL + api.OAuthAuthorizePath + "?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://evil.example.test/cb"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || response.Header.Get("Location") != "" {
		t.Fatalf("unregistered redirect URI: status %d location %q, want 400 and no redirect", response.StatusCode, response.Header.Get("Location"))
	}
}

func TestOAuthLoginWithClientMetadataDocument(t *testing.T) {
	s := newOAuthStack(t)
	documentURL := s.api.URL + "/cimd/client.json"
	handler, err := sdkauth.NewAuthorizationCodeHandler(&sdkauth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &sdkauth.ClientIDMetadataDocumentConfig{URL: documentURL},
		RedirectURL:                    oauthLoginRedirect,
		AuthorizationCodeFetcher:       s.fetcher([]string{repoWidget}),
		Client:                         s.api.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.connect(handler)
	if err != nil {
		t.Fatalf("login with a client metadata document: %v (telemetry %v)", err, s.oauthLines(t))
	}
	defer session.Close()
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, line := range s.oauthLines(t) {
		kinds = append(kinds, line["step"].(string)+":"+line["client_kind"].(string))
	}
	if strings.Join(kinds, ",") != "authorize:metadata_document,consent_preview:metadata_document,consent:metadata_document,token:metadata_document" {
		t.Fatalf("metadata-document login telemetry = %v", kinds)
	}
}

func toolText(result *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcpsdk.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func assertLastOutcomes(t *testing.T, s *oauthStack, step string, want []string) {
	t.Helper()
	var got []string
	for _, line := range s.oauthLines(t) {
		if line["step"] == step {
			got = append(got, line["outcome"].(string))
		}
	}
	if len(got) < len(want) || strings.Join(got[len(got)-len(want):], ",") != strings.Join(want, ",") {
		t.Fatalf("%s outcomes = %v, want to end with %v", step, got, want)
	}
}
