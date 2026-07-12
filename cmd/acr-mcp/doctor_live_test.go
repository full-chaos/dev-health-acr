package main

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// TestDoctorLiveSkipsNetworkWhenLocalConfigInvalid: Given no ACR_API_URL
// configured, When runDoctorLive runs, Then it reports an unreachable live
// check with a fixed, network-free detail -- neither plain `acr-mcp doctor`
// (live is its default mode) nor the explicit `--live` alias ever touches
// the network while static local configuration remains invalid.
func TestDoctorLiveSkipsNetworkWhenLocalConfigInvalid(t *testing.T) {
	t.Setenv("ACR_API_URL", "")
	t.Setenv("ACR_API_TOKEN", "")

	report := runDoctorLive()

	if report.LiveCheck == nil || report.LiveCheck.Reachable {
		t.Fatalf("expected an unreachable live check without a valid local config, got: %#v", report.LiveCheck)
	}
}

// TestDoctorLiveReportsRealEntitlementScopeToolAvailability: Given a
// valid local configuration pointed at a real hosted capabilities
// endpoint, When runDoctorLive runs, Then it reports the actual
// entitlement/scope/tool availability the real handshake returned --
// live data, not only static local configuration.
func TestDoctorLiveReportsRealEntitlementScopeToolAvailability(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"schema_version": "capabilities.v1",
			"service": "dev-health-acr",
			"service_version": "dev",
			"minimum_sidecar_version": "1.0.0",
			"supported_schema_versions": ` + schemaVersionsJSON() + `,
			"enabled_tools": ["context_for_task", "source_evidence"],
			"entitlements": {"agent_context_runtime": true},
			"permissions": {"context_read": true, "evidence_read": true, "episode_write": false},
			"limits": {"max_items": 30, "max_output_tokens": 4000, "max_serialized_bytes": 262144, "requests_per_minute": 60},
			"generated_at": "` + time.Now().UTC().Format(time.RFC3339) + `"
		}`))
	}))
	t.Cleanup(server.Close)

	caPath := filepath.Join(t.TempDir(), "doctor-live-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv("ACR_API_TOKEN", validDoctorToken(42))
	t.Setenv(sidecar.SidecarVersionEnvironment, "1.0.0")

	report := runDoctorLive()

	if report.LiveCheck == nil || !report.LiveCheck.Reachable {
		t.Fatalf("expected a reachable live check, got: %#v", report.LiveCheck)
	}
	if !report.LiveCheck.AgentContextRuntime || !report.LiveCheck.ContextReadScope || !report.LiveCheck.EvidenceReadScope {
		t.Fatalf("expected live entitlement/scope availability to be true, got: %#v", report.LiveCheck)
	}
	if report.LiveCheck.EpisodeWriteScope || report.LiveCheck.RecordEpisodeActive {
		t.Fatalf("expected writeback to remain inactive without episode:write and local opt-in, got: %#v", report.LiveCheck)
	}
	if len(report.LiveCheck.EnabledTools) != 2 {
		t.Fatalf("expected 2 live enabled tools, got: %#v", report.LiveCheck.EnabledTools)
	}
}

// schemaVersionsJSON renders contractsv1.AllSchemaVersions as a JSON array
// literal, so the fixture capabilities response above advertises the same
// canonical schema-version list a real hosted deployment does.
func schemaVersionsJSON() string {
	encoded, err := json.Marshal(contractsv1.AllSchemaVersions)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestDoctorLiveReportsUnreachableTopLevelStatusOnConnectionFailure is the
// CHAOS-2908 rereview regression lock: when the static local checks are
// valid (a well-formed ACR_API_URL, a shape-valid credential) but the
// hosted API is actually unreachable (here, a closed listener), the
// top-level report.Status must reflect that failure -- it must never stay
// "ok" while report.LiveCheck.Reachable is false, which would let an
// operator or agent scanning only the top-level status miss a real
// connectivity outage. The live-check detail must also never leak the
// configured host.
func TestDoctorLiveReportsUnreachableTopLevelStatusOnConnectionFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	caPath := filepath.Join(t.TempDir(), "doctor-live-unreachable-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	host := server.URL
	// Close the listener before any request is sent, so the otherwise-valid
	// configuration below fails purely on connectivity, not shape.
	server.Close()

	t.Setenv(sidecar.APIURLEnvironment, host)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv("ACR_API_TOKEN", validDoctorToken(43))

	report := runDoctorLive()

	if !report.APIURLValid || !report.CredentialShapeValid {
		t.Fatalf("expected static local checks to be valid, got: %#v", report)
	}
	if report.LiveCheck == nil || report.LiveCheck.Reachable {
		t.Fatalf("expected an unreachable live check against a closed listener, got: %#v", report.LiveCheck)
	}
	if report.Status == "ok" {
		t.Fatal("expected a non-ok top-level status when the live check is unreachable")
	}
	if strings.Contains(report.LiveCheck.Detail, host) {
		t.Fatalf("live check detail leaked the configured host: %q", report.LiveCheck.Detail)
	}
}
