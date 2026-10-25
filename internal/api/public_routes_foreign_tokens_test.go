package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// publicRoute is one registration of (*App).registerRoutes. pattern is the
// mux pattern; the pin compares it with the patterns the mux really received.
type publicRoute struct {
	pattern   string
	method    string
	path      string
	gated     bool
	oauthOnly bool
}

var publicRoutes = []publicRoute{
	{pattern: "GET /healthz", method: "GET", path: "/healthz"},
	{pattern: "GET /readyz", method: "GET", path: "/readyz"},
	{pattern: "GET /api/v1/agent-context/capabilities", method: "GET", path: "/api/v1/agent-context/capabilities", gated: true},
	{pattern: "POST /api/v1/agent-context/context-packets", method: "POST", path: "/api/v1/agent-context/context-packets", gated: true},
	{pattern: "GET /api/v1/agent-context/evidence/{evidence_ref_id}", method: "GET", path: "/api/v1/agent-context/evidence/ev_1", gated: true},
	{pattern: "POST /api/v1/agent-context/episodes", method: "POST", path: "/api/v1/agent-context/episodes", gated: true},
	{pattern: "POST " + ContextFabricInvestigationsPath, method: "POST", path: ContextFabricInvestigationsPath, gated: true},
	{pattern: "GET " + ContextFabricInvestigationResultPath, method: "GET", path: "/api/v1/context-fabric/investigations/res_1", gated: true},
	{pattern: "GET " + ContextFabricDataCatalogPath, method: "GET", path: ContextFabricDataCatalogPath, gated: true},
	{pattern: "POST " + ContextFabricDataSubjectsPath, method: "POST", path: ContextFabricDataSubjectsPath, gated: true},
	{pattern: "POST " + ContextFabricDataFactsPath, method: "POST", path: ContextFabricDataFactsPath, gated: true},
	{pattern: "POST " + ContextFabricDataOperationsPath, method: "POST", path: ContextFabricDataOperationsPath, gated: true},
	{pattern: "POST " + ContextFabricDataGraphQLPath, method: "POST", path: ContextFabricDataGraphQLPath, gated: true},
	{pattern: "POST " + ContextFabricDataRelationshipsPath, method: "POST", path: ContextFabricDataRelationshipsPath, gated: true},
	{pattern: "POST /api/v1/oauth/device_authorization", method: "POST", path: "/api/v1/oauth/device_authorization"},
	{pattern: "POST /api/v1/oauth/token", method: "POST", path: "/api/v1/oauth/token"},
	{pattern: "POST /api/v1/oauth/device_approval", method: "POST", path: "/api/v1/oauth/device_approval", gated: true},
	{pattern: "POST /api/v1/auth/credentials/self/rotate", method: "POST", path: "/api/v1/auth/credentials/self/rotate", gated: true},
	{pattern: "POST /api/v1/auth/credentials/self/revoke", method: "POST", path: "/api/v1/auth/credentials/self/revoke", gated: true},
	{pattern: "GET " + OAuthAuthorizationServerMetadataPath, method: "GET", path: OAuthAuthorizationServerMetadataPath, oauthOnly: true},
	{pattern: "GET " + OAuthAuthorizePath, method: "GET", path: OAuthAuthorizePath, oauthOnly: true},
	{pattern: "POST " + OAuthConsentPath, method: "POST", path: OAuthConsentPath, gated: true, oauthOnly: true},
	{pattern: "POST " + OAuthTokenPath, method: "POST", path: OAuthTokenPath, oauthOnly: true},
	{pattern: "POST " + OAuthRegisterPath, method: "POST", path: OAuthRegisterPath, oauthOnly: true},
	{pattern: "POST " + OAuthDeviceAuthorizationPath, method: "POST", path: OAuthDeviceAuthorizationPath, oauthOnly: true},
	{pattern: "POST /api/v1/auth/credentials/self/ack", method: "POST", path: "/api/v1/auth/credentials/self/ack", gated: true},
}

type foreignToken struct {
	name  string
	build func(t *testing.T) string
}

func randomSegment(t *testing.T, n int) string {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func jwtLookalike(t *testing.T, typ string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": typ, "kid": "k1"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := json.Marshal(map[string]any{"iss": "https://issuer.example.test", "sub": "subject_1", "aud": "svc", "iat": 1, "exp": 2})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims) + "." + randomSegment(t, 64)
}

// External Push prefix: dev-health-ops internal/api/externalingest/auth.go:19 (tokenPrefix = "fcpush_").
var foreignTokens = []foreignToken{
	{"workload_jwt", func(t *testing.T) string { return jwtLookalike(t, "workload-token+jwt") }},
	{"user_eddsa_jwt", func(t *testing.T) string { return jwtLookalike(t, "JWT") }},
	{"external_push_fcpush", func(t *testing.T) string { return "fcpush_" + randomSegment(t, 32) }},
	{"svc_acr", func(t *testing.T) string { return "svc_acr_" + randomSegment(t, 32) }},
	{"svc_worker", func(t *testing.T) string { return "svc_worker_" + randomSegment(t, 32) }},
	{"svc_authsess", func(t *testing.T) string { return "svc_authsess_" + randomSegment(t, 32) }},
	{"generic_bearer", func(t *testing.T) string { return "tok_" + randomSegment(t, 32) }},
}

type countingAudit struct {
	storage.AuditStore
	calls atomic.Int64
}

func (c *countingAudit) Record(ctx context.Context, event storage.AuditEvent) error {
	c.calls.Add(1)
	return c.AuditStore.Record(ctx, event)
}

type countingReplays struct{ calls atomic.Int64 }

func (c *countingReplays) Observe(context.Context, string, string, time.Time, time.Time) (bool, error) {
	c.calls.Add(1)
	return false, nil
}

type publicRouteRig struct {
	app         *App
	handler     http.Handler
	credentials *atomic.Int64
	audit       *countingAudit
	replays     *countingReplays
}

func (r publicRouteRig) lookups() int64 {
	return r.credentials.Load() + r.audit.calls.Load() + r.replays.calls.Load()
}

// newPublicRouteRig builds the real route table with the OAuth routes on. The
// credential store counts every call at the store itself, and the audit and
// replay stores sit behind counters in the runtime dependencies, so a handler
// that reaches app.runtime directly is counted like the authenticator is.
func newPublicRouteRig(t *testing.T, oauthOn bool) publicRouteRig {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	rig := publicRouteRig{credentials: &atomic.Int64{}, replays: &countingReplays{}}
	memoryAudit := memory.NewAuditStore()
	rig.audit = &countingAudit{AuditStore: memoryAudit}
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: memoryAudit, Now: clock, Observe: func(string) { rig.credentials.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oauthTestWebKey = private
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: clock, Replays: rig.replays})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := limits.NewManager(limits.Options{Now: clock, PerOrgConcurrency: 4, Policies: limits.PolicySet{Auth: limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	var oauthRuntime *OAuthRuntime
	if oauthOn {
		oauthRuntime = &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, Store: memory.NewOAuthStore(clock), ConsentURL: oauthTestConsentURL}
	}
	rig.app, err = NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Now: clock, Value: hostedCapabilities()}, Limits: manager, Now: clock, WebAssertions: verifier,
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: rig.audit, Entitlements: EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://web.example.test/acr/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(clock)),
			ReadinessChecks:            exactRuntimeChecks(),
			DataStoreChecks:            exactDataStoreChecks(),
			OAuth:                      oauthRuntime,
		},
	}, slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err != nil {
		t.Fatal(err)
	}
	rig.handler = rig.app.Handler()
	return rig
}

type routeAnswer struct {
	status int
	body   string
	header http.Header
}

var (
	requestIDPattern = regexp.MustCompile(`"request_id":"[^"]*"`)
	noncePattern     = regexp.MustCompile(`nonce-[^';]*|nonce="[^"]*"`)
)

func serveRoute(rig publicRouteRig, route publicRoute, headers map[string]string) routeAnswer {
	request := httptest.NewRequest(route.method, route.path, nil)
	request.RemoteAddr = "192.0.2.10:4000"
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	rig.handler.ServeHTTP(response, request)
	header := response.Header().Clone()
	header.Del("X-Request-Id")
	header.Del("Date")
	for name, values := range header {
		for i, value := range values {
			header[name][i] = noncePattern.ReplaceAllString(value, "nonce")
		}
	}
	return routeAnswer{status: response.Code, body: noncePattern.ReplaceAllString(requestIDPattern.ReplaceAllString(response.Body.String(), `"request_id":""`), "nonce"), header: header}
}

func sameAnswer(a, b routeAnswer) bool {
	if a.status != b.status || a.body != b.body || len(a.header) != len(b.header) {
		return false
	}
	for name, values := range a.header {
		if strings.Join(values, "\x00") != strings.Join(b.header.Values(name), "\x00") {
			return false
		}
	}
	return true
}

func foreignCarriers(token string) map[string]map[string]string {
	return map[string]map[string]string{
		"authorization_bearer": {"Authorization": "Bearer " + token},
		"web_assertion_header": {auth.WebAssertionHeader: token},
	}
}

func TestPublicRoutesRefuseForeignTokenClassesBeforeAnyStoreLookup(t *testing.T) {
	for _, route := range publicRoutes {
		if !route.gated {
			continue
		}
		reference := serveRoute(newPublicRouteRig(t, true), route, map[string]string{"Authorization": "Bearer " + auth.TokenPrefix + strings.Repeat("A", 43)})
		if reference.status != http.StatusUnauthorized {
			t.Fatalf("%s %s: unknown-token reference = %d, want 401", route.method, route.path, reference.status)
		}
		for _, class := range foreignTokens {
			for carrier := range foreignCarriers("x") {
				t.Run(route.method+" "+route.path+"/"+class.name+"/"+carrier, func(t *testing.T) {
					rig := newPublicRouteRig(t, true)
					answer := serveRoute(rig, route, foreignCarriers(class.build(t))[carrier])
					if answer.status != http.StatusUnauthorized {
						t.Fatalf("status = %d, want 401", answer.status)
					}
					if !sameAnswer(answer, reference) {
						t.Fatalf("answer %d %v %q differs from the unknown-token refusal %d %v %q", answer.status, answer.header, answer.body, reference.status, reference.header, reference.body)
					}
					if n := rig.lookups(); n != 0 {
						t.Fatalf("store lookups = %d (credential %d, audit %d, replay %d), want 0", n, rig.credentials.Load(), rig.audit.calls.Load(), rig.replays.calls.Load())
					}
				})
			}
		}
	}
}

func TestPublicRoutesIgnoreForeignTokensOnUnauthenticatedRoutes(t *testing.T) {
	for _, route := range publicRoutes {
		if route.gated {
			continue
		}
		reference := serveRoute(newPublicRouteRig(t, true), route, nil)
		for _, class := range foreignTokens {
			for carrier := range foreignCarriers("x") {
				t.Run(route.method+" "+route.path+"/"+class.name+"/"+carrier, func(t *testing.T) {
					rig := newPublicRouteRig(t, true)
					answer := serveRoute(rig, route, foreignCarriers(class.build(t))[carrier])
					if !sameAnswer(answer, reference) {
						t.Fatalf("answer %d %v %q differs from the no-header answer %d %v %q", answer.status, answer.header, answer.body, reference.status, reference.header, reference.body)
					}
					if n := rig.lookups(); n != 0 {
						t.Fatalf("store lookups = %d, want 0", n)
					}
				})
			}
		}
	}
}

// The controls: a zero above is the counters measuring, not a dead wire. A
// well-formed fcacr_ bearer on a gated route reaches the credential store, and
// a direct call on app.runtime is counted although no authenticator is involved.
func TestPublicRouteCounterSeesAWellFormedCredentialLookup(t *testing.T) {
	rig := newPublicRouteRig(t, true)
	route := publicRoutes[2]
	serveRoute(rig, route, map[string]string{"Authorization": "Bearer " + auth.TokenPrefix + strings.Repeat("A", 43)})
	if rig.credentials.Load() != 1 {
		t.Fatalf("credential lookups for a well-formed unknown bearer = %d, want 1", rig.credentials.Load())
	}
}

func TestPublicRouteCountersSeeDirectRuntimeStoreCalls(t *testing.T) {
	rig := newPublicRouteRig(t, true)
	ctx := context.Background()
	_, _ = rig.app.runtime.Credentials.FindByTokenHash(ctx, strings.Repeat("a", 64))
	_, _ = rig.app.runtime.Credentials.List(ctx, "org_1")
	if rig.credentials.Load() != 2 {
		t.Fatalf("direct runtime credential calls counted = %d, want 2", rig.credentials.Load())
	}
	if err := rig.app.runtime.Audit.Record(ctx, storage.AuditEvent{OrgID: "org_1", Action: "probe", Status: "ok", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if rig.audit.calls.Load() != 1 {
		t.Fatalf("direct runtime audit calls counted = %d, want 1", rig.audit.calls.Load())
	}
}

type recordingMux struct {
	mux      *http.ServeMux
	patterns []string
}

func (m *recordingMux) Handle(pattern string, handler http.Handler) {
	m.patterns = append(m.patterns, pattern)
	m.mux.Handle(pattern, handler)
}

func (m *recordingMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.patterns = append(m.patterns, pattern)
	m.mux.HandleFunc(pattern, handler)
}

func registeredPatterns(t *testing.T, oauthOn bool) []string {
	t.Helper()
	recorder := &recordingMux{mux: http.NewServeMux()}
	newPublicRouteRig(t, oauthOn).app.registerRoutes(recorder)
	sort.Strings(recorder.patterns)
	return recorder.patterns
}

func tablePatterns(oauthOn bool) []string {
	var patterns []string
	for _, route := range publicRoutes {
		if oauthOn || !route.oauthOnly {
			patterns = append(patterns, route.pattern)
		}
	}
	sort.Strings(patterns)
	return patterns
}

func TestPublicRouteTableIsFullyCovered(t *testing.T) {
	for _, oauthOn := range []bool{true, false} {
		registered := registeredPatterns(t, oauthOn)
		table := tablePatterns(oauthOn)
		var missing, stale []string
		for _, pattern := range registered {
			if !slices.Contains(table, pattern) {
				missing = append(missing, pattern)
			}
		}
		for _, pattern := range table {
			if !slices.Contains(registered, pattern) {
				stale = append(stale, pattern)
			}
		}
		if len(missing) != 0 || len(stale) != 0 {
			t.Fatalf("oauth=%v: registered but not in the matrix %v; in the matrix but not registered %v", oauthOn, missing, stale)
		}
	}
}

func TestPublicRouteHandlerRegistersThroughRegisterRoutesOnly(t *testing.T) {
	rig := newPublicRouteRig(t, true)
	for _, route := range publicRoutes {
		request := httptest.NewRequest(route.method, route.path, nil)
		response := httptest.NewRecorder()
		rig.handler.ServeHTTP(response, request)
		if response.Code == http.StatusNotFound || response.Code == http.StatusMethodNotAllowed {
			t.Fatalf("%s %s is in the table but Handler() does not serve it (status %d)", route.method, route.path, response.Code)
		}
	}
}
