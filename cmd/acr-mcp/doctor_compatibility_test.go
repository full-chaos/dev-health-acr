package main

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestDoctorReportsReachableIncompatibleCapabilities(t *testing.T) {
	tests := []struct {
		name         string
		entitled     bool
		contextRead  bool
		evidenceRead bool
		tools        []string
		minimum      string
	}{
		{name: "missing entitlement", contextRead: true, evidenceRead: true, tools: []string{"context_for_task", "source_evidence"}, minimum: "1.0.0"},
		{name: "missing context scope", entitled: true, evidenceRead: true, tools: []string{"context_for_task", "source_evidence"}, minimum: "1.0.0"},
		{name: "missing read tool", entitled: true, contextRead: true, evidenceRead: true, tools: []string{"context_for_task"}, minimum: "1.0.0"},
		{name: "minimum version too new", entitled: true, contextRead: true, evidenceRead: true, tools: []string{"context_for_task", "source_evidence"}, minimum: "9.0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			server := doctorCapabilitiesServer(t, contractsv1.Capabilities{
				SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: "1.0.0", MinimumSidecarVersion: test.minimum,
				SupportedSchemaVersions: contractsv1.AllSchemaVersions, EnabledTools: test.tools,
				Entitlements: contractsv1.CapabilityEntitlements{AgentContextRuntime: test.entitled},
				Permissions:  contractsv1.CapabilityPermissions{ContextRead: test.contextRead, EvidenceRead: test.evidenceRead},
				Limits:       contractsv1.CapabilityLimits{MaxItems: 30, MaxOutputTokens: 4_000, MaxSerializedBytes: 262_144, RequestsPerMinute: 60}, GeneratedAt: time.Now().UTC(),
			})
			t.Cleanup(server.Close)
			configureDoctorServer(t, server)

			// When
			report := runDoctorLive()

			// Then
			if report.LiveCheck == nil || !report.LiveCheck.Reachable || report.Status != "live_check_incompatible" {
				t.Fatalf("doctor report = %#v", report)
			}
			if report.LiveCheck.AgentContextRuntime != test.entitled || report.LiveCheck.ContextReadScope != test.contextRead || report.LiveCheck.EvidenceReadScope != test.evidenceRead {
				t.Fatalf("live check = %#v", report.LiveCheck)
			}
			if len(report.LiveCheck.EnabledTools) != len(test.tools) || report.LiveCheck.Detail == "" {
				t.Fatalf("live check = %#v", report.LiveCheck)
			}
		})
	}
}

func doctorCapabilitiesServer(t *testing.T, capabilities contractsv1.Capabilities) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(capabilities); err != nil {
			t.Fatal(err)
		}
	}))
}

func configureDoctorServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doctor-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, path)
	t.Setenv("ACR_API_TOKEN", validDoctorToken(44))
	t.Setenv(sidecar.SidecarVersionEnvironment, "1.0.0")
}
