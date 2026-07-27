package mcp

import (
	"context"
	"encoding/json"
	"encoding/pem"
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

// buildACRMCPBinary compiles the real cmd/acr-mcp entrypoint into a temp
// file, for an end-to-end test that exercises the exact `acr-mcp serve`
// process a client would launch, including main.go's flag/command
// dispatch and signal wiring, not just the internal/mcp package in
// isolation.
func buildACRMCPBinary(t *testing.T) string {
	t.Helper()
	root := findRepoRoot(t)
	binPath := filepath.Join(t.TempDir(), "acr-mcp")
	cmd := exec.Command("go", "build", "-tags", "acr_compiled_lifecycle_lock_fixture", "-o", binPath, "./cmd/acr-mcp")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build acr-mcp: %v\n%s", err, out)
	}
	return binPath
}

// TestCommandTransportE2EBothToolsAgainstLiveTLSFixture launches the real
// acr-mcp binary as a subprocess over mcp.CommandTransport (exactly how an
// IDE/agent client would), connects it to a live httptest.NewTLSServer
// fixture standing in for the hosted ACR API, and exercises tools/list plus
// a successful tools/call for both context_for_task and source_evidence
// end to end through real process stdio.
func TestCommandTransportE2EBothToolsAgainstLiveTLSFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-spawning E2E test in -short mode")
	}
	binPath := buildACRMCPBinary(t)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent-context/capabilities":
			writeJSONFixture(t, w, http.StatusOK, validCapabilitiesFixture())
		case r.URL.Path == "/api/v1/agent-context/context-packets":
			var received contractsv1.ContextPacketRequest
			_ = json.NewDecoder(r.Body).Decode(&received)
			writeJSONFixture(t, w, http.StatusOK, validContextPacketFixture(received.RequestID))
		default:
			writeJSONFixture(t, w, http.StatusOK, validExpandedEvidenceFixture("ev_e2e_fixture"))
		}
	}))
	t.Cleanup(server.Close)

	caPath := filepath.Join(t.TempDir(), "e2e-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "serve")
	cmd.Env = append(os.Environ(),
		"ACR_API_URL="+server.URL,
		"ACR_API_CA_BUNDLE="+caPath,
		"ACR_API_TOKEN="+fixtureToken(0xE2),
		"ACR_SIDECAR_VERSION=1.0.0",
	)
	var stderr fixtureStderrBuffer
	cmd.Stderr = &stderr

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "e2e-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect over CommandTransport failed: %v\nstderr: %s", err, stderr.String())
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("expected exactly 2 tools over the real subprocess, got %d", len(tools.Tools))
	}

	ctxResult, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: toolContextForTask,
		Arguments: map[string]any{
			"goal":       "e2e: investigate flaky checkout tests",
			"repository": map[string]any{"slug": "acme/widgets"},
			"scope":      map[string]any{"branch": "main"},
		},
	})
	if err != nil {
		t.Fatalf("context_for_task tools/call failed: %v\nstderr: %s", err, stderr.String())
	}
	if ctxResult.IsError {
		t.Fatalf("expected context_for_task success over the real subprocess, got: %#v text=%s", ctxResult.Content, ctxResult.Content[0].(*mcpsdk.TextContent).Text)
	}

	evResult, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: toolSourceEvidence,
		Arguments: map[string]any{
			"evidence_ref_id": "ev_e2e_fixture",
		},
	})
	if err != nil {
		t.Fatalf("source_evidence tools/call failed: %v", err)
	}
	if evResult.IsError {
		t.Fatalf("expected source_evidence success over the real subprocess, got: %#v", evResult.Content)
	}

	// TestCommandTransportE2EBothToolsAgainstLiveTLSFixture also proves the
	// caller-input contract removal end to end over the real subprocess
	// wire (not just the in-process handler unit tests): a goal-only
	// payload -- goal is the only required field, with repository resolved
	// from this test process's own discoverable Git workspace -- must
	// succeed, and a payload that still sends the removed schema_version
	// field must be rejected as an unrecognized field, not silently
	// accepted or ignored.
	goalOnlyResult, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: toolContextForTask,
		Arguments: map[string]any{
			"goal": "e2e: goal-only, no repository or scope override",
		},
	})
	if err != nil {
		t.Fatalf("goal-only context_for_task tools/call failed: %v\nstderr: %s", err, stderr.String())
	}
	if goalOnlyResult.IsError {
		t.Fatalf("expected goal-only context_for_task success over the real subprocess, got: %#v text=%s", goalOnlyResult.Content, goalOnlyResult.Content[0].(*mcpsdk.TextContent).Text)
	}

	schemaVersionResult, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: toolContextForTask,
		Arguments: map[string]any{
			"goal":           "e2e: rejected schema_version payload",
			"schema_version": contractsv1.MCPContextForTaskRequestSchema,
		},
	})
	if err != nil {
		t.Fatalf("schema_version context_for_task tools/call failed at the protocol level: %v\nstderr: %s", err, stderr.String())
	}
	if !schemaVersionResult.IsError {
		t.Fatal("expected a payload carrying the removed schema_version field to be rejected as an unrecognized field")
	}
	schemaVersionText, ok := schemaVersionResult.Content[0].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(schemaVersionText.Text, "validation") {
		t.Fatalf("expected category %q in result, got: %#v", "validation", schemaVersionResult.Content)
	}
}

// fixtureStderrBuffer is a minimal io.Writer for capturing subprocess
// capturing subprocess stderr in test failure messages.
type fixtureStderrBuffer struct {
	data []byte
}

func (b *fixtureStderrBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *fixtureStderrBuffer) String() string { return string(b.data) }
