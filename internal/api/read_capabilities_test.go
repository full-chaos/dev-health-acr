package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestCapabilitiesRouteUsesServerDerivedAccess(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var capabilities contractsv1.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if !capabilities.Entitlements.AgentContextRuntime || !capabilities.Permissions.ContextRead || !capabilities.Permissions.EvidenceRead || capabilities.Permissions.EpisodeWrite || len(capabilities.EnabledTools) != 2 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestCapabilitiesRouteEnforcesAuthScopeAndVersion(t *testing.T) {
	tests := []struct {
		name       string
		scopes     []string
		withToken  bool
		version    string
		wantStatus int
		wantCode   string
	}{
		{name: "missing token", scopes: []string{auth.ScopeContextRead}, wantStatus: http.StatusUnauthorized, wantCode: "invalid_token"},
		{name: "missing context scope", scopes: []string{auth.ScopeEvidenceRead}, withToken: true, wantStatus: http.StatusForbidden, wantCode: "insufficient_scope"},
		{name: "missing client version", scopes: []string{auth.ScopeContextRead}, withToken: true, wantStatus: http.StatusUpgradeRequired, wantCode: "version_mismatch"},
		{name: "old client", scopes: []string{auth.ScopeContextRead}, withToken: true, version: "0.0.9", wantStatus: http.StatusUpgradeRequired, wantCode: "version_mismatch"},
		{name: "development client", scopes: []string{auth.ScopeContextRead}, withToken: true, version: "dev", wantStatus: http.StatusUpgradeRequired, wantCode: "version_mismatch"},
		{name: "malformed client", scopes: []string{auth.ScopeContextRead}, withToken: true, version: "latest", wantStatus: http.StatusUpgradeRequired, wantCode: "version_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, token := newHostedTestApp(t, nil, nil, test.scopes, nil, nil)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
			if test.withToken {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			request.Header.Set("X-ACR-Client-Version", test.version)
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestCapabilitiesRouteReportsAbsentEntitlementWithoutGating(t *testing.T) {
	provider := EntitlementFunc(func(context.Context, string, string) (bool, error) { return false, nil })
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, provider, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var capabilities contractsv1.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.Entitlements.AgentContextRuntime || len(capabilities.EnabledTools) != 0 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestCapabilitiesRouteRejectsExactRevokedClientVersion(t *testing.T) {
	// Given
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	app.config.RevokedClientVersions = []string{"1.2.3+build.7"}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.2.3+build.7")

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	assertErrorResponse(t, response, http.StatusUpgradeRequired, "version_mismatch")
}

func TestHostedReadRoutesFailClosedWithoutRuntime(t *testing.T) {
	response := httptest.NewRecorder()

	testApp(t).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil))

	assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
}

func TestCapabilitiesProviderCannotObserveBearerCredential(t *testing.T) {
	provider := &capturingCapabilitiesProvider{value: hostedCapabilities()}
	app, token := newHostedTestApp(t, provider, nil, []string{auth.ScopeContextRead}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)

	app.Handler().ServeHTTP(httptest.NewRecorder(), request)

	if provider.authorization != "" {
		t.Fatal("capabilities provider received bearer credential")
	}
}

func TestRuntimeReadinessChecksAreRequiredAndExecuted(t *testing.T) {
	app, _ := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	ready := httptest.NewRecorder()
	app.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || len(app.readinessChecks) != 3 {
		t.Fatalf("ready status = %d checks=%d", ready.Code, len(app.readinessChecks))
	}
	app.readinessChecks[1] = CheckFunc{CheckName: "entitlement", Fn: func(context.Context) error { return errors.New("unavailable") }}
	notReady := httptest.NewRecorder()
	app.Handler().ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d", notReady.Code)
	}
}

type capturingCapabilitiesProvider struct {
	value         contractsv1.Capabilities
	authorization string
}

func (p *capturingCapabilitiesProvider) Capabilities(_ context.Context, request *http.Request) (contractsv1.Capabilities, error) {
	p.authorization = request.Header.Get("Authorization")
	return p.value, nil
}
