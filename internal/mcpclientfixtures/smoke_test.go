package mcpclientfixtures

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// smokeInjectedVersion is the release-shaped version this test's build
// injects via -ldflags, so the smoke test proves the exact fixture-
// specified command and args work against a real, versioned binary --
// not just a "dev" development build.
const smokeInjectedVersion = "1.4.2"

// buildVersionedACRMCPBinary compiles the real cmd/acr-mcp entrypoint with
// an injected release-shaped version string, mirroring how a real release
// build sets internal/version.Version/Commit/Date via linker flags,
// without this package importing or modifying internal/version itself.
func buildVersionedACRMCPBinary(t *testing.T) string {
	t.Helper()
	root := findRepoRoot(t)
	binPath := filepath.Join(t.TempDir(), "acr-mcp")
	versionPkg := "github.com/full-chaos/dev-health-acr/internal/version"
	ldflags := fmt.Sprintf("-X %s.Version=%s -X %s.Commit=%s -X %s.Date=%s",
		versionPkg, smokeInjectedVersion,
		versionPkg, "0123456789abcdef0123456789abcdef01234567",
		versionPkg, "2026-01-01T00:00:00Z")
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", binPath, "./cmd/acr-mcp")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build versioned acr-mcp: %v\n%s", err, out)
	}
	return binPath
}

// capabilitiesFixtureJSON renders a minimal, valid capabilities response
// this package's own smoke test server returns, sharing the canonical
// schema-version list with the real contract rather than hand-copying it.
func capabilitiesFixtureJSON(t *testing.T) []byte {
	t.Helper()
	schemaVersions, err := json.Marshal(contractsv1.AllSchemaVersions)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{
		"schema_version": "capabilities.v1",
		"service": "dev-health-acr",
		"service_version": "dev",
		"minimum_sidecar_version": "1.0.0",
		"supported_schema_versions": ` + string(schemaVersions) + `,
		"enabled_tools": ["context_for_task", "source_evidence"],
		"entitlements": {"agent_context_runtime": true},
		"permissions": {"context_read": true, "evidence_read": true, "episode_write": false},
		"limits": {"max_items": 30, "max_output_tokens": 4000, "max_serialized_bytes": 262144, "requests_per_minute": 60},
		"generated_at": "` + time.Now().UTC().Format(time.RFC3339) + `"
	}`)
}

// TestVersionedBinaryReportsInjectedVersion proves the ldflags injection
// this smoke test relies on actually took effect, before trusting the
// STDIO handshake below to be exercising a genuinely "versioned" binary.
func TestVersionedBinaryReportsInjectedVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-spawning smoke test in -short mode")
	}
	binPath := buildVersionedACRMCPBinary(t)
	out, err := exec.Command(binPath, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("acr-mcp version: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), smokeInjectedVersion) {
		t.Fatalf("expected version output to contain %q, got: %s", smokeInjectedVersion, out)
	}
}

// TestFixtureCommandAndArgsStartRealVersionedSTDIOServer is the STDIO
// startup smoke test the diagnostic/client lane owns: it launches the
// exact command and args this package's canonical model (and therefore
// every generated/checked-in client fixture) specifies -- not a
// hand-tuned test-only invocation -- against a real, ldflags-versioned
// acr-mcp binary and a live TLS capabilities fixture, and proves a real
// MCP client can complete initialize and tools/list over that exact
// invocation.
func TestFixtureCommandAndArgsStartRealVersionedSTDIOServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-spawning smoke test in -short mode")
	}
	binPath := buildVersionedACRMCPBinary(t)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(capabilitiesFixtureJSON(t))
	}))
	t.Cleanup(server.Close)

	caPath := filepath.Join(t.TempDir(), "smoke-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	entry, err := ParseDocumentationStdioJSON([]byte(RenderClaudeCodeJSON()))
	if err != nil {
		t.Fatalf("parse the canonical model's own rendered JSON: %v", err)
	}
	if len(entry.Args) != 1 {
		t.Fatalf("expected exactly one fixture arg, got %#v", entry.Args)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// binPath stands in for the fixture's "/path/to/acr-mcp" placeholder;
	// entry.Args[0] ("serve") is the real, unmodified argument the
	// checked-in fixture specifies.
	cmd := exec.CommandContext(ctx, binPath, entry.Args...)
	cmd.Env = append(os.Environ(),
		"ACR_API_URL="+server.URL,
		"ACR_API_CA_BUNDLE="+caPath,
		"ACR_API_TOKEN="+fixtureToken(),
		"ACR_SIDECAR_VERSION=1.0.0",
	)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcpclientfixtures-smoke", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect over CommandTransport failed: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("expected exactly 2 tools over the real versioned subprocess, got %d", len(tools.Tools))
	}
}

// fixtureToken is a fixed, shape-valid ACR API token for this package's
// own smoke test fixture server; it is never a real credential and is
// only ever sent to a local httptest server this same test process
// controls.
func fixtureToken() string {
	value := make([]byte, 32)
	for i := range value {
		value[i] = 0xF3
	}
	return "fcacr_" + base64.RawURLEncoding.EncodeToString(value)
}
