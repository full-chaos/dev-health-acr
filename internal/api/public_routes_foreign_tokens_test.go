package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// publicRoute is one registration of (*App).Handler. expr is the pattern
// argument exactly as app.go spells it, so the table can be pinned to the source.
type publicRoute struct {
	expr   string
	method string
	path   string
	gated  bool
}

var publicRoutes = []publicRoute{
	{expr: `"GET /healthz"`, method: "GET", path: "/healthz"},
	{expr: `"GET /readyz"`, method: "GET", path: "/readyz"},
	{expr: `"GET /api/v1/agent-context/capabilities"`, method: "GET", path: "/api/v1/agent-context/capabilities", gated: true},
	{expr: `"POST /api/v1/agent-context/context-packets"`, method: "POST", path: "/api/v1/agent-context/context-packets", gated: true},
	{expr: `"GET /api/v1/agent-context/evidence/{evidence_ref_id}"`, method: "GET", path: "/api/v1/agent-context/evidence/ev_1", gated: true},
	{expr: `"POST /api/v1/agent-context/episodes"`, method: "POST", path: "/api/v1/agent-context/episodes", gated: true},
	{expr: `"POST "+ContextFabricInvestigationsPath`, method: "POST", path: ContextFabricInvestigationsPath, gated: true},
	{expr: `"GET "+ContextFabricInvestigationResultPath`, method: "GET", path: "/api/v1/context-fabric/investigations/res_1", gated: true},
	{expr: `"GET "+ContextFabricDataCatalogPath`, method: "GET", path: ContextFabricDataCatalogPath, gated: true},
	{expr: `"POST "+ContextFabricDataSubjectsPath`, method: "POST", path: ContextFabricDataSubjectsPath, gated: true},
	{expr: `"POST "+ContextFabricDataFactsPath`, method: "POST", path: ContextFabricDataFactsPath, gated: true},
	{expr: `"POST "+ContextFabricDataOperationsPath`, method: "POST", path: ContextFabricDataOperationsPath, gated: true},
	{expr: `"POST "+ContextFabricDataGraphQLPath`, method: "POST", path: ContextFabricDataGraphQLPath, gated: true},
	{expr: `"POST "+ContextFabricDataRelationshipsPath`, method: "POST", path: ContextFabricDataRelationshipsPath, gated: true},
	{expr: `"POST /api/v1/oauth/device_authorization"`, method: "POST", path: "/api/v1/oauth/device_authorization"},
	{expr: `"POST /api/v1/oauth/token"`, method: "POST", path: "/api/v1/oauth/token"},
	{expr: `"POST /api/v1/oauth/device_approval"`, method: "POST", path: "/api/v1/oauth/device_approval", gated: true},
	{expr: `"POST /api/v1/auth/credentials/self/rotate"`, method: "POST", path: "/api/v1/auth/credentials/self/rotate", gated: true},
	{expr: `"POST /api/v1/auth/credentials/self/revoke"`, method: "POST", path: "/api/v1/auth/credentials/self/revoke", gated: true},
	{expr: `"GET "+OAuthAuthorizationServerMetadataPath`, method: "GET", path: OAuthAuthorizationServerMetadataPath},
	{expr: `"GET "+OAuthAuthorizePath`, method: "GET", path: OAuthAuthorizePath},
	{expr: `"POST "+OAuthConsentPath`, method: "POST", path: OAuthConsentPath, gated: true},
	{expr: `"POST "+OAuthTokenPath`, method: "POST", path: OAuthTokenPath},
	{expr: `"POST "+OAuthRegisterPath`, method: "POST", path: OAuthRegisterPath},
	{expr: `"POST "+OAuthDeviceAuthorizationPath`, method: "POST", path: OAuthDeviceAuthorizationPath},
	{expr: `"POST /api/v1/auth/credentials/self/ack"`, method: "POST", path: "/api/v1/auth/credentials/self/ack", gated: true},
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

type countingCredentials struct {
	storage.CredentialStore
	calls atomic.Int64
}

func (c *countingCredentials) List(ctx context.Context, orgID string) ([]contractsv1.ClientCredential, error) {
	c.calls.Add(1)
	return c.CredentialStore.List(ctx, orgID)
}

func (c *countingCredentials) GetByID(ctx context.Context, orgID, id string) (contractsv1.ClientCredential, error) {
	c.calls.Add(1)
	return c.CredentialStore.GetByID(ctx, orgID, id)
}

func (c *countingCredentials) FindByTokenHash(ctx context.Context, hash string) (contractsv1.ClientCredential, error) {
	c.calls.Add(1)
	return c.CredentialStore.FindByTokenHash(ctx, hash)
}

func (c *countingCredentials) TouchLastUsed(ctx context.Context, orgID, id, ip, ua string, at time.Time) error {
	c.calls.Add(1)
	return c.CredentialStore.TouchLastUsed(ctx, orgID, id, ip, ua, at)
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
	handler     http.Handler
	credentials *countingCredentials
	audit       *countingAudit
	replays     *countingReplays
	reached     *atomic.Int64
}

func (r publicRouteRig) lookups() int64 {
	return r.credentials.calls.Load() + r.audit.calls.Load() + r.replays.calls.Load()
}

// newPublicRouteRig builds the real route table with the OAuth routes on and
// every credential, audit and replay store behind a counter.
func newPublicRouteRig(t *testing.T) publicRouteRig {
	t.Helper()
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rig := publicRouteRig{
		credentials: &countingCredentials{CredentialStore: app.runtime.Credentials},
		audit:       &countingAudit{AuditStore: app.runtime.Audit},
		replays:     &countingReplays{},
		reached:     &atomic.Int64{},
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: app.now, Replays: rig.replays})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewAuthenticator(rig.credentials, rig.audit, auth.AuthenticatorOptions{Now: app.now, Limiter: auth.NoopLimiter{}, ClientIP: app.clientIP, WebAssertions: verifier})
	if err != nil {
		t.Fatal(err)
	}
	app.authenticator = authenticator
	rig.handler = app.Handler()
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
		reference := serveRoute(newPublicRouteRig(t), route, map[string]string{"Authorization": "Bearer " + auth.TokenPrefix + strings.Repeat("A", 43)})
		if reference.status != http.StatusUnauthorized {
			t.Fatalf("%s %s: unknown-token reference = %d, want 401", route.method, route.path, reference.status)
		}
		for _, class := range foreignTokens {
			for carrier := range foreignCarriers("x") {
				t.Run(route.method+" "+route.path+"/"+class.name+"/"+carrier, func(t *testing.T) {
					rig := newPublicRouteRig(t)
					answer := serveRoute(rig, route, foreignCarriers(class.build(t))[carrier])
					if answer.status != http.StatusUnauthorized {
						t.Fatalf("status = %d, want 401", answer.status)
					}
					if !sameAnswer(answer, reference) {
						t.Fatalf("answer %d %v %q differs from the unknown-token refusal %d %v %q", answer.status, answer.header, answer.body, reference.status, reference.header, reference.body)
					}
					if n := rig.lookups(); n != 0 {
						t.Fatalf("store lookups = %d (credential %d, audit %d, replay %d), want 0", n, rig.credentials.calls.Load(), rig.audit.calls.Load(), rig.replays.calls.Load())
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
		reference := serveRoute(newPublicRouteRig(t), route, nil)
		for _, class := range foreignTokens {
			for carrier := range foreignCarriers("x") {
				t.Run(route.method+" "+route.path+"/"+class.name+"/"+carrier, func(t *testing.T) {
					rig := newPublicRouteRig(t)
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

// The control: a well-formed fcacr_ bearer on a gated route does reach the
// credential store, so a zero above is the counter measuring, not a dead wire.
func TestPublicRouteCounterSeesAWellFormedCredentialLookup(t *testing.T) {
	rig := newPublicRouteRig(t)
	route := publicRoutes[2]
	serveRoute(rig, route, map[string]string{"Authorization": "Bearer " + auth.TokenPrefix + strings.Repeat("A", 43)})
	if rig.credentials.calls.Load() != 1 {
		t.Fatalf("credential lookups for a well-formed unknown bearer = %d, want 1", rig.credentials.calls.Load())
	}
}

func TestPublicRouteTableIsFullyCovered(t *testing.T) {
	source, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	registration := regexp.MustCompile(`mux\.Handle(?:Func)?\(([^,]+),`)
	found := map[string]bool{}
	for _, match := range registration.FindAllStringSubmatch(string(source), -1) {
		found[strings.ReplaceAll(strings.TrimSpace(match[1]), " ", "")] = true
	}
	covered := map[string]bool{}
	for _, route := range publicRoutes {
		covered[strings.ReplaceAll(route.expr, " ", "")] = true
	}
	var missing, stale []string
	for expr := range found {
		if !covered[expr] {
			missing = append(missing, expr)
		}
	}
	for expr := range covered {
		if !found[expr] {
			stale = append(stale, expr)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) != 0 || len(stale) != 0 {
		t.Fatalf("route table out of step with app.go: not in the matrix %v; in the matrix but not registered %v", missing, stale)
	}
}
