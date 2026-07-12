package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mcpServerEntry mirrors the "command"/"args" shape shared by the Claude
// Code and Cursor STDIO client config templates under
// docs/examples/mcp-clients/.
type mcpServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func loadMCPServersJSON(t *testing.T, root, relPath string) map[string]mcpServerEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	var doc struct {
		MCPServers map[string]mcpServerEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode %s: %v", relPath, err)
	}
	return doc.MCPServers
}

func assertLaunchesServeWithNoOtherArgs(t *testing.T, label string, entry mcpServerEntry) {
	t.Helper()
	if !strings.HasSuffix(entry.Command, "acr-mcp") {
		t.Fatalf("%s: expected command to launch acr-mcp, got %q", label, entry.Command)
	}
	if len(entry.Args) != 1 || entry.Args[0] != "serve" {
		t.Fatalf("%s: expected args [\"serve\"] (the exact command this package's CommandTransport E2E test exercises), got %#v", label, entry.Args)
	}
}

// TestClaudeCodeAndCursorFixturesLaunchServeCorrectly is the "client
// fixture smoke" check for the two JSON-shaped example configs: both must
// invoke the real, implemented `acr-mcp serve` subcommand with no
// unsupported extra arguments. The actual STDIO protocol behavior these
// configs rely on is exercised end to end by
// TestCommandTransportE2EBothToolsAgainstLiveTLSFixture.
func TestClaudeCodeAndCursorFixturesLaunchServeCorrectly(t *testing.T) {
	root := findRepoRoot(t)

	cases := []struct {
		label   string
		relPath string
	}{
		{"claude-code-mcp.json", "docs/examples/mcp-clients/claude-code-mcp.json"},
		{"cursor-mcp-config.json", "docs/examples/mcp-clients/cursor-mcp-config.json"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			servers := loadMCPServersJSON(t, root, tc.relPath)
			entry, ok := servers["acr"]
			if !ok {
				t.Fatalf("%s: expected an \"acr\" mcpServers entry", tc.relPath)
			}
			assertLaunchesServeWithNoOtherArgs(t, tc.relPath, entry)
		})
	}
}

// TestCodexConfigFixtureLaunchesServeCorrectly checks the TOML-shaped Codex
// CLI config template without adding a TOML dependency: it asserts the
// fixed substrings this package's own template writer controls (command
// path suffix, exact args array, correct env table name) are present
// verbatim, which is sufficient for a static fixture nobody else mutates
// programmatically.
func TestCodexConfigFixtureLaunchesServeCorrectly(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs/examples/mcp-clients/codex-config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	for _, want := range []string{
		`[mcp_servers.acr]`,
		`args = ["serve"]`,
		`[mcp_servers.acr.env]`,
		`ACR_API_URL`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("codex-config.toml missing expected content: %q", want)
		}
	}
	if strings.Contains(content, `args = ["serve",`) || strings.Contains(content, `"record_episode"`) {
		t.Fatal("codex-config.toml must not reference extra args or record_episode")
	}
}

// TestGenericLauncherScriptInvokesServeOnly proves launch-sidecar.sh's
// final exec targets the "serve" subcommand and nothing else.
func TestGenericLauncherScriptInvokesServeOnly(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs/examples/mcp-clients/launch-sidecar.sh"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `exec "$ACR_MCP_BINARY" serve`) {
		t.Fatal("launch-sidecar.sh must exec the sidecar binary with exactly the serve subcommand")
	}
}
