package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// realHostedCapabilities mirrors cmd/acr-api/main.go's actual capabilities
// wiring (schema version list included), not a hand-crafted "just enough
// to pass" fixture: it is what a real hosted deployment advertises once
// SupportedSchemaVersions is sourced from the single canonical
// contractsv1.AllSchemaVersions list.
func realHostedCapabilities() contractsv1.Capabilities {
	return contractsv1.Capabilities{
		SchemaVersion:           contractsv1.CapabilitiesSchema,
		Service:                 "dev-health-acr",
		ServiceVersion:          "1.2.3",
		MinimumSidecarVersion:   "1.0.0",
		SupportedSchemaVersions: contractsv1.AllSchemaVersions,
		Limits: contractsv1.CapabilityLimits{
			MaxItems: 30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60,
		},
	}
}

// TestMCPBootstrapAgainstRealHostedCapabilitiesProviderWithNoVersionEnvOverride
// is the end-to-end regression lock for the CHAOS-2908 review finding: the
// default production bootstrap path (no ACR_SIDECAR_CLIENT_VERSION, no
// ACR_SIDECAR_VERSION configured -- exactly what a freshly installed
// release binary sees) must succeed against the *real* hosted API's
// capabilities handler (internal/api.handleCapabilities via a real
// net/http server, not internal/mcp's own hand-rolled test fixture, which
// never replicates the server-side X-ACR-Client-Version rejection and so
// cannot catch this class of bug). Given/When/Then:
//
// Given a real hosted App wired exactly as cmd/acr-api/main.go wires it
// (StaticCapabilitiesProvider sourced from contractsv1.AllSchemaVersions,
// a real auth-issued bearer credential with context:read+evidence:read
// scope) served over a real httptest TLS listener, and no
// ACR_SIDECAR_CLIENT_VERSION/ACR_SIDECAR_VERSION override configured,
//
// When acrmcp.NewBootstrap runs with a realistic compiled serverVersion,
//
// Then bootstrap succeeds: the compiled version was resolved and sent as
// X-ACR-Client-Version before the real hosted handler's
// clientVersionCompatible check ever ran, and the real hosted
// SupportedSchemaVersions list satisfied every schema this sidecar
// requires.
func TestMCPBootstrapAgainstRealHostedCapabilitiesProviderWithNoVersionEnvOverride(t *testing.T) {
	provider := StaticCapabilitiesProvider{Value: realHostedCapabilities()}
	app, token := newHostedTestApp(t, provider, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil, nil)
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)

	caPath := filepath.Join(t.TempDir(), "mcp-bootstrap-integration-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv(sidecar.TokenEnvironment, token)
	// Deliberately not setting ACR_SIDECAR_CLIENT_VERSION or
	// ACR_SIDECAR_VERSION: the scenario under test is a freshly installed
	// release binary where an operator configured neither.

	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("expected default bootstrap (no version env override) to succeed against the real hosted capabilities provider, got: %v", err)
	}
	if boot.Capabilities.Service != "dev-health-acr" {
		t.Fatalf("unexpected capabilities: %#v", boot.Capabilities)
	}
}

// TestMCPBootstrapAgainstRealHostedCapabilitiesProviderRejectsStaleBinary
// is the fail-closed counterpart run against the same real hosted handler:
// a genuinely stale compiled version, with no env override, must still be
// rejected -- proving the fix in
// TestMCPBootstrapAgainstRealHostedCapabilitiesProviderWithNoVersionEnvOverride
// is a real compatibility gate, not an accidental bypass of it.
func TestMCPBootstrapAgainstRealHostedCapabilitiesProviderRejectsStaleBinary(t *testing.T) {
	provider := StaticCapabilitiesProvider{Value: realHostedCapabilities()}
	app, token := newHostedTestApp(t, provider, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil, nil)
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)

	caPath := filepath.Join(t.TempDir(), "mcp-bootstrap-integration-stale-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv(sidecar.TokenEnvironment, token)

	_, err := acrmcp.NewBootstrap(context.Background(), "0.5.0")
	if err == nil {
		t.Fatal("expected bootstrap to fail closed against a real hosted API when the compiled version is older than its minimum")
	}
}

// TestMCPServerUsesDefaultDeviceCredentialForExplicitRepositoryOutsideGit
// closes the device-login-to-tool-call seam. It deliberately obtains the
// bearer token through the real device authorization, signed web approval,
// and token redemption routes instead of provisioning a separate fixture
// credential. The resulting organization-wide grant must authorize an
// explicit same-organization repository even when the MCP process has no Git
// workspace from which it could discover repository identity.
func TestMCPServerUsesDefaultDeviceCredentialForExplicitRepositoryOutsideGit(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := StaticCapabilitiesProvider{Value: realHostedCapabilities()}
	app, _ := newHostedTestAppWithWebAssertions(t, provider, nil, nil, nil, nil, verifier)

	createdResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(createdResponse, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{
		SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema,
	}))
	if createdResponse.Code != http.StatusOK {
		t.Fatalf("device authorization status = %d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var authorization contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(createdResponse.Body).Decode(&authorization); err != nil {
		t.Fatal(err)
	}

	approvalResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(approvalResponse, deviceApprovalRequest(t, now, private, contractsv1.DeviceApprovalRequest{
		SchemaVersion:    contractsv1.DeviceApprovalRequestSchema,
		UserCode:         authorization.UserCode,
		RepositoryScopes: []string{"*"},
	}, "approval_mcp_org_wide"))
	if approvalResponse.Code != http.StatusOK {
		t.Fatalf("device approval status = %d body=%s", approvalResponse.Code, approvalResponse.Body.String())
	}

	tokenResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(tokenResponse, deviceTokenRequest(t, authorization.DeviceCode))
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("device token status = %d body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var issued contractsv1.DeviceTokenResponse
	if err := json.NewDecoder(tokenResponse.Body).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if len(issued.Credential.RepositoryScopes) != 1 || issued.Credential.RepositoryScopes[0] != "*" {
		t.Fatalf("device credential repository scopes = %v, want organization-wide grant", issued.Credential.RepositoryScopes)
	}

	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "mcp-device-integration-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv(sidecar.TokenEnvironment, issued.AccessToken)

	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("bootstrap with device-issued credential: %v", err)
	}
	serverMCP := acrmcp.NewServer(boot, "1.2.5")
	clientMCP := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "device-login-integration", Version: "1.0.0"}, nil)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := serverMCP.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := clientMCP.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	// t.TempDir is intentionally not initialized as Git. An explicit repository
	// must be sufficient and must not be narrowed by local inventory/discovery.
	t.Chdir(t.TempDir())
	result, err := clientSession.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "context_for_task",
		Arguments: map[string]any{
			"goal":       "investigate flaky checkout tests",
			"repository": map[string]any{"slug": hostedTestRepository},
		},
	})
	if err != nil {
		t.Fatalf("context_for_task protocol error: %v", err)
	}
	if result.IsError {
		t.Fatalf("context_for_task rejected the device-issued organization-wide credential: %#v", result.Content)
	}
}
