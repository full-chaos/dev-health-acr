package main

import (
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// With no transport configured, doctor reports STDIO, the STDIO revision
// list (which stops short of 2026-07-28, so roots keep working), the server
// revision, and no listen address -- and the missing credential still makes
// the configuration incomplete, exactly as before.
func TestDoctorReportsTheSTDIOTransportByDefault(t *testing.T) {
	t.Setenv(sidecar.APIURLEnvironment, "https://acr.example.test")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenFileEnvironment, t.TempDir()+"/missing-token")

	report := runDoctor()
	if report.Transport.Mode != acrmcp.TransportSTDIO || !report.Transport.Valid {
		t.Fatalf("transport %#v", report.Transport)
	}
	if len(report.Transport.ProtocolRevisions) == 0 || slices.Contains(report.Transport.ProtocolRevisions, "2026-07-28") {
		t.Fatalf("STDIO revisions %v", report.Transport.ProtocolRevisions)
	}
	if report.Transport.ListenAddress != "" || report.Transport.BasePath != "" {
		t.Fatalf("STDIO reported HTTP settings: %#v", report.Transport)
	}
	if report.ServerRevision == "" || report.ServerRevision != report.Version {
		t.Fatalf("server revision %q, version %q", report.ServerRevision, report.Version)
	}
	if report.Status != "incomplete_configuration" {
		t.Fatalf("status %q", report.Status)
	}
	if check := doctorCheck(t, report, "transport"); check.Status != "ok" || check.Detail != "STDIO is the SVS MCP transport" {
		t.Fatalf("transport check %#v", check)
	}
}

// With the HTTP transport configured, doctor reports it, a revision list that
// includes 2026-07-28, the listen address, base path and probe paths, and does
// not treat the absent process credential as a missing setting.
func TestDoctorReportsTheHTTPTransport(t *testing.T) {
	t.Setenv(sidecar.APIURLEnvironment, "https://acr.example.test")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenFileEnvironment, t.TempDir()+"/missing-token")
	t.Setenv(acrmcp.TransportEnvironment, "http")
	t.Setenv(acrmcp.HTTPListenEnvironment, "0.0.0.0:9443")
	t.Setenv(acrmcp.HTTPBasePathEnvironment, "/agent/mcp")

	report := runDoctor()
	want := doctorTransport{
		Mode: "http", Valid: true, ProtocolRevisions: acrmcp.ProtocolRevisions(acrmcp.TransportHTTP),
		ListenAddress: "0.0.0.0:9443", BasePath: "/agent/mcp", HealthPath: "/healthz", ReadyPath: "/readyz",
	}
	if report.Transport.Mode != want.Mode || report.Transport.ListenAddress != want.ListenAddress || report.Transport.BasePath != want.BasePath ||
		report.Transport.HealthPath != want.HealthPath || report.Transport.ReadyPath != want.ReadyPath || !report.Transport.Valid {
		t.Fatalf("transport %#v, want %#v", report.Transport, want)
	}
	if len(report.Transport.ProtocolRevisions) == 0 || report.Transport.ProtocolRevisions[0] != "2026-07-28" {
		t.Fatalf("HTTP revisions %v, want 2026-07-28 first", report.Transport.ProtocolRevisions)
	}
	if report.Status != "ok" {
		t.Fatalf("status %q: an absent process credential is not missing configuration for the hosted transport", report.Status)
	}
	if check := doctorCheck(t, report, "credential"); check.Status != "ok" {
		t.Fatalf("credential check %#v", check)
	}
}

// Every invalid serve setting makes doctor report invalid_configuration and
// name the setting, never the value.
func TestDoctorNamesAnInvalidServeSetting(t *testing.T) {
	cases := map[string]string{
		acrmcp.TransportEnvironment:             "grpc",
		acrmcp.HTTPListenEnvironment:            "no-port-secret-value",
		acrmcp.HTTPBasePathEnvironment:          "relative-secret-value",
		acrmcp.HTTPReadHeaderTimeoutEnvironment: "soon",
		acrmcp.HTTPReadTimeoutEnvironment:       "0s",
		acrmcp.HTTPWriteTimeoutEnvironment:      "-1s",
		acrmcp.HTTPIdleTimeoutEnvironment:       "2h",
		acrmcp.HTTPShutdownTimeoutEnvironment:   "x",
		acrmcp.HTTPMaxBodyBytesEnvironment:      "999999999999",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(sidecar.APIURLEnvironment, "https://acr.example.test")
			t.Setenv(acrmcp.TransportEnvironment, "http")
			t.Setenv(name, value)
			report := runDoctor()
			check := doctorCheck(t, report, "transport")
			if report.Status != "invalid_configuration" || report.Transport.Valid || check.Status != "error" {
				t.Fatalf("report %#v check %#v", report.Transport, check)
			}
			if !strings.Contains(check.Detail, name) || strings.Contains(check.Detail, value) {
				t.Fatalf("detail %q must name %s and not carry the value", check.Detail, name)
			}
		})
	}
	if len(cases) != 9 {
		t.Fatalf("%d cases, want one per serve setting (9)", len(cases))
	}
}

// The hosted transport's live check is the readiness probe: it succeeds on the
// hosted API's liveness route alone, with no credential configured.
func TestDoctorLiveForTheHTTPTransportProbesLivenessWithoutACredential(t *testing.T) {
	var sawAuthorization bool
	status := http.StatusOK
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuthorization = true
		}
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "doctor-http-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenFileEnvironment, t.TempDir()+"/missing-token")
	t.Setenv(acrmcp.TransportEnvironment, "http")

	report := runDoctorLive()
	if report.LiveCheck == nil || !report.LiveCheck.Reachable || report.Status != "ok" {
		t.Fatalf("live %#v status %q", report.LiveCheck, report.Status)
	}
	status = http.StatusServiceUnavailable
	report = runDoctorLive()
	if report.LiveCheck == nil || report.LiveCheck.Reachable || report.Status != "live_check_unreachable" {
		t.Fatalf("live %#v status %q", report.LiveCheck, report.Status)
	}
	if sawAuthorization {
		t.Fatal("the hosted live check sent a credential")
	}
}

// Flags win over the environment; each flag has an environment twin; a
// positional argument is a usage error; an out-of-range flag names its
// setting.
func TestParseServeArgs(t *testing.T) {
	env := map[string]string{
		acrmcp.TransportEnvironment:  "http",
		acrmcp.HTTPListenEnvironment: ":9000",
	}
	lookup := func(name string) (string, bool) { v, ok := env[name]; return v, ok }

	opts, err := parseServeArgs(nil, lookup)
	if err != nil || opts.Transport != "http" || opts.Listen != ":9000" || opts.BasePath != "/mcp" {
		t.Fatalf("env only: %#v %v", opts, err)
	}
	opts, err = parseServeArgs([]string{
		"--transport=stdio", "--listen=127.0.0.1:7000", "--base-path=/x", "--read-header-timeout=2s", "--read-timeout=3s",
		"--write-timeout=4s", "--idle-timeout=5s", "--shutdown-timeout=6s", "--max-body-bytes=4096",
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := acrmcp.ServeOptions{Transport: "stdio", Listen: "127.0.0.1:7000", BasePath: "/x", ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second,
		WriteTimeout: 4 * time.Second, IdleTimeout: 5 * time.Second, ShutdownTimeout: 6 * time.Second, MaxBodyBytes: 4096}
	if opts != want {
		t.Fatalf("flags: %#v, want %#v", opts, want)
	}
	if _, err := parseServeArgs([]string{"extra"}, lookup); !errors.Is(err, errServeUsage) {
		t.Fatalf("positional: %v", err)
	}
	if _, err := parseServeArgs([]string{"--unknown"}, lookup); !errors.Is(err, errServeUsage) {
		t.Fatalf("unknown flag: %v", err)
	}
	var invalid *acrmcp.ErrServeOptionInvalid
	if _, err := parseServeArgs([]string{"--base-path=/healthz"}, lookup); !errors.As(err, &invalid) || invalid.Setting != acrmcp.HTTPBasePathEnvironment {
		t.Fatalf("probe path as base path: %v", err)
	}
	if opts, err := parseServeArgs(nil, func(string) (string, bool) { return "", false }); err != nil || opts.Transport != "stdio" {
		t.Fatalf("defaults: %#v %v", opts, err)
	}
}
