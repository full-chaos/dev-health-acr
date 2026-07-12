package main

import (
	"archive/tar"
	"bytes"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const (
	diagnosticsCanaryProxySecret   = "diagnostics-proxy-userinfo-secret-42"
	diagnosticsCanaryHeaderSecret  = "diagnostics-secret-header-value-7f3a"
	diagnosticsCanaryBodyMarker    = "diagnostics-secret-body-marker-9c21"
	diagnosticsCanaryDirectoryName = "top-s3cr3t-directory-8b41"
)

// diagnosticsBundleEntries extracts every tar entry's name and raw content
// from a built archive, for the canary scans below.
func diagnosticsBundleEntries(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	entries := make(map[string][]byte)
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar entry: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar content for %s: %v", header.Name, err)
		}
		entries[header.Name] = content
	}
	return entries
}

// assertBundleNeverContains scans every tar entry's filename and full byte
// content for each needle, failing loudly (with the offending entry name,
// but never the secret itself) if any needle appears anywhere.
func assertBundleNeverContains(t *testing.T, archive []byte, needles ...string) {
	t.Helper()
	entries := diagnosticsBundleEntries(t, archive)
	for name, content := range entries {
		for _, needle := range needles {
			if strings.Contains(name, needle) {
				t.Fatalf("bundle entry filename %q contains a canary secret", name)
			}
			if bytes.Contains(content, []byte(needle)) {
				t.Fatalf("bundle entry %q contains a canary secret", name)
			}
		}
	}
}

// TestDiagnosticsBundleHasExactlyTheExpectedFiles locks the archive's file
// list to the fixed, documented set -- a canary against any future code
// path that generates a per-check or per-secret filename.
func TestDiagnosticsBundleHasExactlyTheExpectedFiles(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(60))

	data, err := buildDiagnosticsBundle(false)
	if err != nil {
		t.Fatalf("buildDiagnosticsBundle: %v", err)
	}
	entries := diagnosticsBundleEntries(t, data)
	expected := map[string]bool{
		"manifest.json":      true,
		"doctor-static.json": true,
		"README.md":          true,
	}
	if len(entries) != len(expected) {
		t.Fatalf("expected exactly %d entries, got %d: %v", len(expected), len(entries), entries)
	}
	for name := range entries {
		if !expected[name] {
			t.Fatalf("unexpected bundle entry %q", name)
		}
	}
}

// TestDiagnosticsBundleNeverLeaksStaticConfigSecretsOrPaths is the
// process-level canary for the diagnostic bundle's static (non-live)
// path: it configures the real production sidecar config surface -- a
// token file, a CA bundle, and a proxy URL with embedded userinfo -- each
// carrying a distinct canary value planted in its path and/or content,
// runs the exact pipeline `acr-mcp diagnostics` uses, and then scans every
// resulting archive byte and filename for each canary. None may appear
// anywhere in the bundle, regardless of whether the resulting local
// configuration is valid or invalid.
func TestDiagnosticsBundleNeverLeaksStaticConfigSecretsOrPaths(t *testing.T) {
	dir := t.TempDir()
	secretDir := filepath.Join(dir, diagnosticsCanaryDirectoryName)
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}

	token := validDoctorToken(61)
	tokenPath := filepath.Join(secretDir, "acr-token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	caPath := filepath.Join(secretDir, "ca-bundle.pem")
	caContentMarker := "secretCAcontentMarkerXYZ9911"
	caPEM := "-----BEGIN CERTIFICATE-----\n" + caContentMarker + "\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(caPath, []byte(caPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", "")
	t.Setenv("ACR_API_TOKEN_FILE", tokenPath)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv(sidecar.ProxyURLEnvironment, "http://proxyuser:"+diagnosticsCanaryProxySecret+"@proxy.example.test:3128")

	data, err := buildDiagnosticsBundle(false)
	if err != nil {
		t.Fatalf("buildDiagnosticsBundle: %v", err)
	}

	assertBundleNeverContains(t, data,
		token,
		secretDir,
		tokenPath,
		caPath,
		caContentMarker,
		diagnosticsCanaryProxySecret,
		"proxyuser",
		"proxy.example.test",
	)
}

// TestDiagnosticsBundleLiveModeNeverLeaksHeadersOrResponseBody proves the
// --live path is equally leak-free: a real hosted capabilities response
// carrying a canary response header and an extra, undeclared JSON body
// field must never reach the bundle, only the sanitized booleans and
// enabled-tool names doctor --live already extracts.
func TestDiagnosticsBundleLiveModeNeverLeaksHeadersOrResponseBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Diagnostics-Canary", diagnosticsCanaryHeaderSecret)
		_, _ = w.Write([]byte(`{
			"schema_version": "capabilities.v1",
			"service": "dev-health-acr",
			"service_version": "dev-` + diagnosticsCanaryBodyMarker + `",
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

	caPath := filepath.Join(t.TempDir(), "diagnostics-live-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv("ACR_API_TOKEN", validDoctorToken(62))
	t.Setenv(sidecar.SidecarVersionEnvironment, "1.0.0")

	data, err := buildDiagnosticsBundle(true)
	if err != nil {
		t.Fatalf("buildDiagnosticsBundle: %v", err)
	}

	entries := diagnosticsBundleEntries(t, data)
	liveReport, ok := entries["doctor-live.json"]
	if !ok {
		t.Fatalf("expected a doctor-live.json entry, got: %v", entries)
	}
	if !bytes.Contains(liveReport, []byte(`"reachable": true`)) {
		t.Fatalf("expected the live report to record a reachable server: %s", liveReport)
	}

	assertBundleNeverContains(t, data,
		diagnosticsCanaryHeaderSecret,
		diagnosticsCanaryBodyMarker,
		caPath,
		server.URL,
	)
}

func TestParseDoctorArgsCoversBundleAbsentPresentAndLiveOrder(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		parsed, err := parseDoctorArgs(nil)
		if err != nil {
			t.Fatalf("parseDoctorArgs(nil) error: %v", err)
		}
		if parsed.offline || !parsed.live || parsed.bundle || parsed.bundleOutput != "" || parsed.bundleLive {
			t.Fatalf("unexpected parsed args: %#v", parsed)
		}
	})

	t.Run("present", func(t *testing.T) {
		parsed, err := parseDoctorArgs([]string{"--bundle", "/tmp/x.tar"})
		if err != nil {
			t.Fatalf("parseDoctorArgs(bundle) error: %v", err)
		}
		if parsed.offline || !parsed.live || !parsed.bundle || parsed.bundleOutput != "/tmp/x.tar" || parsed.bundleLive {
			t.Fatalf("unexpected parsed args: %#v", parsed)
		}
	})

	t.Run("live_before_bundle", func(t *testing.T) {
		parsed, err := parseDoctorArgs([]string{"--live", "--bundle", "/tmp/y.tar"})
		if err != nil {
			t.Fatalf("parseDoctorArgs(live, bundle) error: %v", err)
		}
		if parsed.offline || !parsed.live || !parsed.bundle || parsed.bundleOutput != "/tmp/y.tar" || !parsed.bundleLive {
			t.Fatalf("unexpected parsed args: %#v", parsed)
		}
	})

	t.Run("live_after_bundle", func(t *testing.T) {
		parsed, err := parseDoctorArgs([]string{"--bundle", "/tmp/z.tar", "--live"})
		if err != nil {
			t.Fatalf("parseDoctorArgs(bundle, live) error: %v", err)
		}
		if parsed.offline || !parsed.live || !parsed.bundle || parsed.bundleOutput != "/tmp/z.tar" || !parsed.bundleLive {
			t.Fatalf("unexpected parsed args: %#v", parsed)
		}
	})
}

// TestRunDiagnosticsBundleFailsClosedForInvalidDestination proves the CLI
// entry point surfaces WriteBundle's fail-closed destination checks as a
// nonzero exit code rather than panicking or silently succeeding.
func TestRunDiagnosticsBundleFailsClosedForInvalidDestination(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(63))

	if code := runDiagnosticsBundle(filepath.Join(t.TempDir(), "missing-parent", "bundle.tar"), false); code == 0 {
		t.Fatal("expected a nonzero exit code for a missing parent directory")
	}
}

// TestRunDiagnosticsBundleWritesReadableBundle is the happy-path
// end-to-end smoke test: a valid configuration writes a bundle to an
// explicit path with exit code 0.
func TestRunDiagnosticsBundleWritesReadableBundle(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(64))

	path := filepath.Join(physicalTempDir(t), "bundle.tar")
	if code := runDiagnosticsBundle(path, false); code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeGOOSIsWindows() {
		return
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %o", info.Mode().Perm())
	}
}

func runtimeGOOSIsWindows() bool {
	return os.PathSeparator == '\\'
}
