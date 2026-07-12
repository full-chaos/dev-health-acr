package api

import (
	"context"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
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
