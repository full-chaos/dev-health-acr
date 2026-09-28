package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// CHAOS-7071 (S0): the direct data routes are registered with their full
// protection chain and a stub answer. These tests drive the REAL App handler
// with the REAL authenticator over the memory credential store, so the
// states asserted are the ones a caller sees.

type chaos7071Harness struct {
	app      *App
	service  *auth.Service
	now      *time.Time
	entitled *bool
}

func newChaos7071Harness(t *testing.T, dataRequestsPerWindow int) *chaos7071Harness {
	t.Helper()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	entitled := true
	h := &chaos7071Harness{now: &now, entitled: &entitled}
	clock := func() time.Time { return *h.now }
	audit := memory.NewAuditStore()
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	h.service, err = auth.NewService(credentials, auth.ServiceOptions{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	hooks := observability.NewHooks(nil, nil)
	store := seededEvaluationStore(t, "org_1", observability.NewEvidenceExpansionObserver(hooks))
	manager, err := limits.NewManager(limits.Options{Now: clock, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 1, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Data:     limits.DataPolicy{Window: time.Minute, PerCredentialLimit: dataRequestsPerWindow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assembler := contextpacket.NewAssembler(store, contextpacket.Options{
		Now: clock, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0",
		Observer: observability.NewAssemblyObserver(hooks), StoreBackend: contextpacket.StoreBackendMemory,
	})
	h.app, err = NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Now: clock, Value: hostedCapabilities()}, Observability: &hooks, Limits: manager, Now: clock,
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Assembler: assembler, Evidence: store,
			Entitlements:         EntitlementFunc(func(context.Context, string, string) (bool, error) { return *h.entitled, nil }),
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device", DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(clock)),
			ReadinessChecks: exactRuntimeChecks(),
		},
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *chaos7071Harness) issue(t *testing.T, scopes []string, expiresAt *time.Time) auth.IssuedCredential {
	t.Helper()
	issued, err := h.service.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: "org_1", Name: "chaos7071", RepositoryScopes: []string{hostedTestRepository}, Scopes: scopes, CreatedBy: "test_actor", ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func (h *chaos7071Harness) call(method, path, token string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{}`)
	} else {
		body = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, body)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(response, request)
	return response
}

var chaos7071Routes = []struct {
	method, path, scope string
}{
	{http.MethodGet, ContextFabricDataCatalogPath, auth.ScopeContextRead},
	{http.MethodPost, ContextFabricDataSubjectsPath, auth.ScopeContextRead},
	{http.MethodPost, ContextFabricDataFactsPath, auth.ScopeContextRead},
	{http.MethodPost, ContextFabricDataOperationsPath, auth.ScopeDataRead},
}

func assertChaos7071Stub(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assertErrorResponse(t, response, http.StatusNotImplemented, "feature_not_enabled")
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Details["reason"] != contextFabricDataNotImplementedReason || envelope.Error.Retryable {
		t.Fatalf("stub envelope %#v", envelope.Error)
	}
}

// Scope: a pre-CHAOS-7071 token (context:read + evidence:read) reaches the
// context:read stubs and gets 403 insufficient_scope on operations; a
// data:read-only token is the mirror image. This is the state decision K4
// exists to reach: an existing token never gains the data route.
func TestChaos7071DataRoutesEnforceTheirScope(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	legacy := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil).Token
	dataOnly := h.issue(t, []string{auth.ScopeDataRead}, nil).Token
	both := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeDataRead}, nil).Token
	for _, route := range chaos7071Routes {
		legacyResponse := h.call(route.method, route.path, legacy)
		dataResponse := h.call(route.method, route.path, dataOnly)
		if route.scope == auth.ScopeDataRead {
			assertErrorResponse(t, legacyResponse, http.StatusForbidden, "insufficient_scope")
			assertChaos7071Stub(t, dataResponse)
		} else {
			assertChaos7071Stub(t, legacyResponse)
			assertErrorResponse(t, dataResponse, http.StatusForbidden, "insufficient_scope")
		}
		assertChaos7071Stub(t, h.call(route.method, route.path, both))
	}
}

// Authentication is live on every request: no bearer, a revoked credential
// and an expired credential are all 401, never the stub.
func TestChaos7071DataRoutesRefuseMissingRevokedAndExpiredCredentials(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	all := []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeDataRead}
	revoked := h.issue(t, all, nil)
	expiry := h.now.Add(time.Hour)
	expiring := h.issue(t, all, &expiry)
	for _, route := range chaos7071Routes {
		assertErrorResponse(t, h.call(route.method, route.path, ""), http.StatusUnauthorized, "invalid_token")
		assertChaos7071Stub(t, h.call(route.method, route.path, revoked.Token))
		assertChaos7071Stub(t, h.call(route.method, route.path, expiring.Token))
	}
	if _, err := h.service.Revoke(context.Background(), "org_1", revoked.Credential.CredentialID, "test_actor"); err != nil {
		t.Fatal(err)
	}
	*h.now = h.now.Add(2 * time.Hour)
	for _, route := range chaos7071Routes {
		assertErrorResponse(t, h.call(route.method, route.path, revoked.Token), http.StatusUnauthorized, "invalid_token")
		assertErrorResponse(t, h.call(route.method, route.path, expiring.Token), http.StatusUnauthorized, "invalid_token")
	}
}

// Entitlement: an unentitled organization gets 403 feature_not_enabled
// (not the 501 stub) on every data route.
func TestChaos7071DataRoutesRequireTheEntitlement(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeDataRead}, nil).Token
	*h.entitled = false
	for _, route := range chaos7071Routes {
		assertErrorResponse(t, h.call(route.method, route.path, token), http.StatusForbidden, "feature_not_enabled")
	}
}

// Rate class Data is its own budget: spending it answers 429 on operations
// and leaves the Context-class data routes untouched.
func TestChaos7071OperationsRouteHasItsOwnRateClass(t *testing.T) {
	h := newChaos7071Harness(t, 1)
	token := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeDataRead}, nil).Token
	assertChaos7071Stub(t, h.call(http.MethodPost, ContextFabricDataOperationsPath, token))
	assertErrorResponse(t, h.call(http.MethodPost, ContextFabricDataOperationsPath, token), http.StatusTooManyRequests, "rate_limited")
	assertChaos7071Stub(t, h.call(http.MethodPost, ContextFabricDataFactsPath, token))
	assertChaos7071Stub(t, h.call(http.MethodGet, ContextFabricDataCatalogPath, token))
}

// Existing behaviour is unchanged: a token that also holds data:read gets a
// byte-identical capabilities answer (no new tool, no new permission field)
// to a pre-CHAOS-7071 token, and the pre-existing routes answer as before.
func TestChaos7071ExistingSurfacesUnchanged(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	legacy := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil).Token
	withData := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeDataRead}, nil).Token
	legacyCaps := h.call(http.MethodGet, "/api/v1/agent-context/capabilities", legacy)
	dataCaps := h.call(http.MethodGet, "/api/v1/agent-context/capabilities", withData)
	if legacyCaps.Code != http.StatusOK || dataCaps.Code != http.StatusOK {
		t.Fatalf("capabilities status %d / %d", legacyCaps.Code, dataCaps.Code)
	}
	var legacyValue, dataValue contractsv1.Capabilities
	if err := json.Unmarshal(legacyCaps.Body.Bytes(), &legacyValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dataCaps.Body.Bytes(), &dataValue); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyCaps.Body.Bytes(), dataCaps.Body.Bytes()) {
		t.Fatalf("capabilities differ with data:read:\n%s\n%s", legacyCaps.Body.String(), dataCaps.Body.String())
	}
	if strings.Join(legacyValue.EnabledTools, ",") != "context_for_task,source_evidence" {
		t.Fatalf("enabled tools %v", legacyValue.EnabledTools)
	}
	// A data:read-only token still cannot use a pre-existing route.
	dataOnly := h.issue(t, []string{auth.ScopeDataRead}, nil).Token
	assertErrorResponse(t, h.call(http.MethodGet, "/api/v1/agent-context/evidence/opaque-reference", dataOnly), http.StatusForbidden, "insufficient_scope")
	assertErrorResponse(t, h.call(http.MethodGet, "/api/v1/agent-context/capabilities", dataOnly), http.StatusForbidden, "insufficient_scope")
}
