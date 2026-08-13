package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memorymodelconfig"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// newContextFabricModelConfigTestApp mirrors newContextFabricTestApp
// (context_fabric_routes_test.go) but wires an OrgModelConfigStore and
// issues a context:admin-scoped credential instead of context:read.
func newContextFabricModelConfigTestApp(t *testing.T) (*App, *memorymodelconfig.Store, string, func(scopes []string) string) {
	t.Helper()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialLifecycle(t, audit, now)
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	token := issueScopedCredential(t, credentials, audit, now, []string{auth.ScopeContextAdmin}, []string{hostedTestRepository})
	configs := memorymodelconfig.NewStore(func() time.Time { return now })
	entitlements := EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil })
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:    limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context: limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider := StaticCapabilitiesProvider{Now: func() time.Time { return now }, Value: hostedCapabilities()}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: provider, Limits: manager, Now: func() time.Time { return now },
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: entitlements,
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(func() time.Time { return now })),
			ReadinessChecks:            exactRuntimeChecks(),
			OrgModelConfigs:            configs,
		},
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	issueToken := func(scopes []string) string {
		return issueScopedCredential(t, credentials, audit, now, scopes, []string{hostedTestRepository})
	}
	return app, configs, token, issueToken
}

func modelConfigWriteBody() contractsv1.ContextFabricOrgModelConfigWriteRequest {
	return contractsv1.ContextFabricOrgModelConfigWriteRequest{
		SchemaVersion: contractsv1.ContextFabricOrgModelConfigWriteRequestSchema,
		Provider:      "acme-gateway",
		BaseURL:       "https://llm.acme-gateway.example/v1/",
		Model:         "acme-large",
		FallbackModel: "acme-large-fallback",
		Credential:    "sk-acme-live-a1b2c3d4e5f6wxyz",
	}
}

func modelConfigRequest(t *testing.T, method, token string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, ContextFabricOrgModelConfigPath, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	return request
}

// TestContextFabricOrgModelConfig_getReturnsNotFound_beforeAnyWrite covers
// AC-3775-3's observable half: reading an unconfigured organization's
// configuration is a clean 404, not an error.
func TestContextFabricOrgModelConfig_getReturnsNotFound_beforeAnyWrite(t *testing.T) {
	app, _, token, _ := newContextFabricModelConfigTestApp(t)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, modelConfigRequest(t, http.MethodGet, token, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", response.Code, response.Body.String())
	}
}

// TestContextFabricOrgModelConfig_putThenGet_roundTripsMaskedCredential is
// the end-to-end AC-3775-4 proof through the real route: the credential
// sent on PUT never reappears anywhere in either response body.
func TestContextFabricOrgModelConfig_putThenGet_roundTripsMaskedCredential(t *testing.T) {
	app, _, token, _ := newContextFabricModelConfigTestApp(t)
	write := modelConfigWriteBody()

	putResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(putResponse, modelConfigRequest(t, http.MethodPut, token, write))
	if putResponse.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", putResponse.Code, putResponse.Body.String())
	}
	if bytes.Contains(putResponse.Body.Bytes(), []byte(write.Credential)) {
		t.Fatal("PUT response leaked the plaintext credential")
	}

	getResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(getResponse, modelConfigRequest(t, http.MethodGet, token, nil))
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", getResponse.Code, getResponse.Body.String())
	}
	if bytes.Contains(getResponse.Body.Bytes(), []byte(write.Credential)) {
		t.Fatal("GET response leaked the plaintext credential")
	}
	var got contractsv1.ContextFabricOrgModelConfig
	if err := json.Unmarshal(getResponse.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if got.Provider != write.Provider || got.Model != write.Model || got.FallbackModel != write.FallbackModel {
		t.Fatalf("got = %+v, want provider/model/fallback_model to match the write", got)
	}
	if got.CredentialMasked == "" || got.CredentialMasked == write.Credential {
		t.Fatalf("CredentialMasked = %q, want a non-empty masked value", got.CredentialMasked)
	}
	if got.OrgID == "" {
		t.Fatal("OrgID was not populated in the response")
	}
}

// TestContextFabricOrgModelConfig_put_rejectsMissingCredential locks
// contract validation at the route: a write with no credential is a 400,
// never silently accepted as "keep whatever was there before" (this route
// has full-replace, not partial-patch, semantics).
func TestContextFabricOrgModelConfig_put_rejectsMissingCredential(t *testing.T) {
	app, _, token, _ := newContextFabricModelConfigTestApp(t)
	invalid := modelConfigWriteBody()
	invalid.Credential = ""
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, modelConfigRequest(t, http.MethodPut, token, invalid))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", response.Code, response.Body.String())
	}
}

// TestContextFabricOrgModelConfig_put_rejectsInsecureBaseURL locks that an
// http:// base URL is rejected for the per-organization surface (unlike the
// deployment-default surface's AllowInsecureBaseURL escape hatch -- a
// customer-entered URL always leaves ACR's trust boundary).
func TestContextFabricOrgModelConfig_put_rejectsInsecureBaseURL(t *testing.T) {
	app, _, token, _ := newContextFabricModelConfigTestApp(t)
	invalid := modelConfigWriteBody()
	invalid.BaseURL = "http://llm.acme-gateway.example/v1/"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, modelConfigRequest(t, http.MethodPut, token, invalid))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", response.Code, response.Body.String())
	}
}

// TestContextFabricOrgModelConfig_requiresAuthentication locks that every
// method 401s with no credential, before ever touching the store.
func TestContextFabricOrgModelConfig_requiresAuthentication(t *testing.T) {
	app, _, _, _ := newContextFabricModelConfigTestApp(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, modelConfigRequest(t, method, "", modelConfigWriteBody()))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body: %s", response.Code, response.Body.String())
			}
		})
	}
}

// TestContextFabricOrgModelConfig_requiresAdminScope locks that a
// context:read-scoped credential (sufficient for the investigations route)
// is NOT sufficient here -- managing a BYO LLM credential is a distinct,
// higher privilege than reading investigation results.
func TestContextFabricOrgModelConfig_requiresAdminScope(t *testing.T) {
	app, _, _, issueToken := newContextFabricModelConfigTestApp(t)
	readOnlyToken := issueToken([]string{auth.ScopeContextRead})
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, modelConfigRequest(t, http.MethodGet, readOnlyToken, nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", response.Code, response.Body.String())
	}
}

// TestContextFabricOrgModelConfig_delete_thenGet_returnsNotFound is
// AC-3775-3's other observable half: deleting a configuration reverts the
// organization to "no configuration", not to some cached prior state.
func TestContextFabricOrgModelConfig_delete_thenGet_returnsNotFound(t *testing.T) {
	app, _, token, _ := newContextFabricModelConfigTestApp(t)
	putResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(putResponse, modelConfigRequest(t, http.MethodPut, token, modelConfigWriteBody()))
	if putResponse.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", putResponse.Code)
	}
	deleteResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(deleteResponse, modelConfigRequest(t, http.MethodDelete, token, nil))
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	getResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(getResponse, modelConfigRequest(t, http.MethodGet, token, nil))
	if getResponse.Code != http.StatusNotFound {
		t.Fatalf("GET status after delete = %d, want 404; body: %s", getResponse.Code, getResponse.Body.String())
	}
}

// TestContextFabricOrgModelConfig_orgIDIsServerDerived_neverFromBody locks
// the structural org-scoping rule (TRD §19.3.6). Two things must both be
// true: an org_id in the request body is rejected outright (the Go type has
// no such field, and decodeJSONBody disallows unknown fields -- stronger
// than merely ignoring it), and when a caller sends a body with no org_id
// at all, the write always lands under the authenticated principal's own
// organization, never anywhere else.
func TestContextFabricOrgModelConfig_orgIDIsServerDerived_neverFromBody(t *testing.T) {
	app, configs, token, _ := newContextFabricModelConfigTestApp(t)
	payloadWithOrgID := map[string]any{
		"schema_version": contractsv1.ContextFabricOrgModelConfigWriteRequestSchema,
		"provider":       "acme-gateway",
		"model":          "acme-large",
		"credential":     "sk-acme-live-a1b2c3d4e5f6wxyz",
		"org_id":         "org_attacker_supplied",
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, modelConfigRequest(t, http.MethodPut, token, payloadWithOrgID))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (org_id is not a field this contract accepts); body: %s", response.Code, response.Body.String())
	}
	if _, ok, err := configs.ResolveOrgModelConfig(context.Background(), "org_attacker_supplied"); err != nil || ok {
		t.Fatalf("a rejected write must never land under any organization; ok=%v err=%v", ok, err)
	}

	okResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(okResponse, modelConfigRequest(t, http.MethodPut, token, modelConfigWriteBody()))
	if okResponse.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", okResponse.Code, okResponse.Body.String())
	}
	if _, ok, err := configs.ResolveOrgModelConfig(context.Background(), "org_1"); err != nil || !ok {
		t.Fatalf("the write must land under the authenticated principal's org (org_1); ok=%v err=%v", ok, err)
	}
}
