package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// providerSwitch is an entitlement provider whose outage can be flipped
// mid-test. While down it answers like the real client does: an error.
type providerSwitch struct {
	down  atomic.Bool
	calls atomic.Int64
}

func (p *providerSwitch) HasEntitlement(context.Context, string, string) (bool, error) {
	p.calls.Add(1)
	if p.down.Load() {
		return false, errors.New("entitlement provider unavailable")
	}
	return true, nil
}

var allOutageScopes = []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite, auth.ScopeDataRead}

// gatedRoutePatterns lists every route registered through
// protectedRuntimeHandler with entitlement=true. A new gated route must be
// added here; TestEntitlementGatedRoutesRefuseFailClosed fails when the
// served set and this list differ.
var gatedRoutePatterns = []string{
	"DELETE /api/v1/context-fabric/threads/{thread_id}",
	"GET /api/v1/agent-context/capabilities",
	"GET /api/v1/agent-context/evidence/{evidence_ref_id}",
	"GET /api/v1/context-fabric/data/catalog",
	"GET /api/v1/context-fabric/investigations/feedback/{result_id}",
	"GET /api/v1/context-fabric/investigations/submissions/{submission_id}",
	"GET /api/v1/context-fabric/investigations/{result_id}",
	"GET /api/v1/context-fabric/threads",
	"GET /api/v1/context-fabric/threads/{thread_id}",
	"POST /api/v1/agent-context/episodes",
	"POST /api/v1/context-fabric/data/facts",
	"POST /api/v1/context-fabric/data/graphql",
	"POST /api/v1/context-fabric/data/operations",
	"POST /api/v1/context-fabric/data/relationships",
	"POST /api/v1/context-fabric/data/subjects",
	"POST /api/v1/context-fabric/investigations",
	"POST /api/v1/context-fabric/investigations/feedback/{result_id}",
}

type outagePatternMux struct{ patterns []string }

func (m *outagePatternMux) Handle(pattern string, _ http.Handler) {
	m.patterns = append(m.patterns, pattern)
}
func (m *outagePatternMux) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	m.patterns = append(m.patterns, pattern)
}

func outageRequest(pattern, token string) *http.Request {
	method, path, _ := strings.Cut(pattern, " ")
	for strings.Contains(path, "{") {
		start := strings.Index(path, "{")
		end := strings.Index(path, "}")
		path = path[:start] + "x" + path[end+1:]
	}
	request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func serveOutageRequest(app *App, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func isEntitlementRefusal(response *httptest.ResponseRecorder) bool {
	return response.Code == http.StatusServiceUnavailable && strings.Contains(response.Body.String(), "Entitlement service is temporarily unavailable")
}

func newOutageApp(t *testing.T, provider *providerSwitch) (*App, string) {
	t.Helper()
	return newHostedTestApp(t, nil, nil, allOutageScopes, provider, nil)
}

// CHAOS-12730 cell (a): the entitlement provider being down does not change
// /readyz and does not touch the surfaces that need no entitlement decision.
func TestEntitlementOutage_ReadyzStaysReadyAndPostgresOnly(t *testing.T) {
	provider := &providerSwitch{}
	provider.down.Store(true)
	app, _ := newOutageApp(t, provider)

	response := serveOutageRequest(app, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "entitlement") {
		t.Fatalf("/readyz = %d %s, want 200 without an entitlement check", response.Code, response.Body.String())
	}
	if len(app.readinessChecks) != 1 || app.readinessChecks[0].Name() != "postgres" {
		t.Fatalf("readiness checks = %#v, want exactly postgres", app.readinessChecks)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("/readyz called the entitlement provider %d times, want 0", provider.calls.Load())
	}
	if health := serveOutageRequest(app, httptest.NewRequest(http.MethodGet, "/healthz", nil)); health.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", health.Code)
	}
}

func TestEntitlementOutage_OAuthSurfaceStillServes(t *testing.T) {
	provider := &providerSwitch{}
	provider.down.Store(true)
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	app.runtime.Entitlements = provider
	metadata := serveOutageRequest(app, httptest.NewRequest(http.MethodGet, OAuthAuthorizationServerMetadataPath, nil))
	if metadata.Code != http.StatusOK {
		t.Fatalf("AS metadata = %d, want 200 while the provider is down", metadata.Code)
	}
	authorize := serveOutageRequest(app, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath, nil))
	if authorize.Code >= http.StatusInternalServerError {
		t.Fatalf("authorize = %d, want a non-5xx answer while the provider is down", authorize.Code)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("OAuth routes called the entitlement provider %d times, want 0", provider.calls.Load())
	}
}

// Authenticated capabilities reports an entitlement decision, so it is
// entitlement-gated: it refuses (503 upstream_unavailable) rather than
// answer entitlement=false (lead ruling 2026-11-03).
func TestEntitlementOutage_AuthenticatedCapabilitiesFailsClosed(t *testing.T) {
	provider := &providerSwitch{}
	app, token := newOutageApp(t, provider)
	request := func() *http.Request { return outageRequest("GET /api/v1/agent-context/capabilities", token) }
	if up := serveOutageRequest(app, request()); up.Code != http.StatusOK {
		t.Fatalf("capabilities with the provider up = %d, want 200", up.Code)
	}
	provider.down.Store(true)
	down := serveOutageRequest(app, request())
	assertErrorResponse(t, down, http.StatusServiceUnavailable, "upstream_unavailable")
	if strings.Contains(down.Body.String(), `"agent_context_runtime":false`) {
		t.Fatalf("capabilities served an entitlement=false answer: %s", down.Body.String())
	}
}

// CHAOS-12730 cell (b) + pin: every entitlement-gated route refuses
// fail-closed while the provider is down, and the set of gated routes is
// pinned.
func TestEntitlementGatedRoutesRefuseFailClosed(t *testing.T) {
	provider := &providerSwitch{}
	provider.down.Store(true)
	app, token := newOutageApp(t, provider)
	recorder := &outagePatternMux{}
	app.registerRoutes(recorder)
	var served []string
	for _, pattern := range recorder.patterns {
		if isEntitlementRefusal(serveOutageRequest(app, outageRequest(pattern, token))) {
			served = append(served, pattern)
		}
	}
	sort.Strings(served)
	want := append([]string(nil), gatedRoutePatterns...)
	sort.Strings(want)
	if strings.Join(served, "\n") != strings.Join(want, "\n") {
		t.Fatalf("routes refusing with the entitlement 503:\n%s\nwant:\n%s", strings.Join(served, "\n"), strings.Join(want, "\n"))
	}
}

// CHAOS-12730 cell (c): recovery needs no restart.
func TestEntitlementOutage_GatedRouteRecoversWithoutRestart(t *testing.T) {
	provider := &providerSwitch{}
	app, token := newOutageApp(t, provider)
	pattern := "GET /api/v1/context-fabric/data/catalog"
	provider.down.Store(true)
	if response := serveOutageRequest(app, outageRequest(pattern, token)); !isEntitlementRefusal(response) {
		t.Fatalf("while down: %d %s, want the entitlement refusal", response.Code, response.Body.String())
	}
	if ready := serveOutageRequest(app, httptest.NewRequest(http.MethodGet, "/readyz", nil)); ready.Code != http.StatusOK {
		t.Fatalf("/readyz while down = %d, want 200", ready.Code)
	}
	provider.down.Store(false)
	if response := serveOutageRequest(app, outageRequest(pattern, token)); isEntitlementRefusal(response) || response.Code == http.StatusForbidden {
		t.Fatalf("after recovery: %d %s, want the request past the entitlement gate", response.Code, response.Body.String())
	}
	provider.down.Store(true)
	if response := serveOutageRequest(app, outageRequest(pattern, token)); !isEntitlementRefusal(response) {
		t.Fatalf("down again: %d %s, want the entitlement refusal", response.Code, response.Body.String())
	}
}

func TestRuntimeDependencies_rejects_entitlement_in_readiness_checks(t *testing.T) {
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	runtime := *app.runtime
	runtime.ReadinessChecks = []ReadinessCheck{CheckFunc{CheckName: "postgres"}, CheckFunc{CheckName: "entitlement"}}
	if err := runtime.validate(); err == nil {
		t.Fatal("runtime accepted an entitlement check inside ReadinessChecks (/readyz)")
	}
	runtime.ReadinessChecks = []ReadinessCheck{CheckFunc{CheckName: "postgres"}}
	if err := runtime.validate(); err != nil {
		t.Fatalf("runtime rejected a postgres-only /readyz list: %v", err)
	}
	runtime.ReadinessChecks = nil
	if err := runtime.validate(); err == nil {
		t.Fatal("runtime accepted an empty /readyz list")
	}
}

// Postgres down still flips /readyz (unchanged).
func TestReadyz_PostgresDownIsNotReady(t *testing.T) {
	app, _ := newOutageApp(t, &providerSwitch{})
	app.readinessChecks[0] = CheckFunc{CheckName: "postgres", Fn: func(context.Context) error { return errors.New("down") }}
	if response := serveOutageRequest(app, httptest.NewRequest(http.MethodGet, "/readyz", nil)); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with postgres down = %d, want 503", response.Code)
	}
}
