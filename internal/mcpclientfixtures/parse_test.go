package mcpclientfixtures

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseStdioJSONMatchesCanonicalModel structurally parses the checked-
// in Claude Code and Cursor JSON fixtures (not just a substring match) and
// asserts the parsed fields match this package's canonical model exactly.
func TestParseStdioJSONMatchesCanonicalModel(t *testing.T) {
	root := findRepoRoot(t)

	cases := []struct {
		relPath       string
		tokenFileWant string
	}{
		{"docs/examples/mcp-clients/claude-code-mcp.json", "${HOME}/.acr/token"},
		{"docs/examples/mcp-clients/cursor-mcp-config.json", "${env:HOME}/.acr/token"},
	}
	for _, tc := range cases {
		t.Run(tc.relPath, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, tc.relPath))
			if err != nil {
				t.Fatal(err)
			}
			entry, err := ParseDocumentationStdioJSON(data)
			if err != nil {
				t.Fatal(err)
			}
			if entry.Type != "stdio" {
				t.Fatalf("expected type stdio, got %q", entry.Type)
			}
			if entry.Command != ExampleCommand {
				t.Fatalf("expected command %q, got %q", ExampleCommand, entry.Command)
			}
			if len(entry.Args) != 1 || entry.Args[0] != ServeArg {
				t.Fatalf("expected args [%q], got %#v", ServeArg, entry.Args)
			}
			if entry.Env["ACR_API_URL"] != ExampleAPIURL {
				t.Fatalf("expected ACR_API_URL %q, got %q", ExampleAPIURL, entry.Env["ACR_API_URL"])
			}
			if entry.Env["ACR_API_TOKEN_FILE"] != tc.tokenFileWant {
				t.Fatalf("expected ACR_API_TOKEN_FILE %q, got %q", tc.tokenFileWant, entry.Env["ACR_API_TOKEN_FILE"])
			}
			if entry.Env["ACR_API_TIMEOUT"] != ExampleTimeout {
				t.Fatalf("expected ACR_API_TIMEOUT %q, got %q", ExampleTimeout, entry.Env["ACR_API_TIMEOUT"])
			}
		})
	}
}

func TestParseStdioJSONRejectsAmbiguousOrUnsafeRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"extra server", `{"mcpServers":{"acr":{"type":"stdio","command":"acr-mcp","args":["serve"]},"other":{"type":"stdio","command":"other","args":[]}}}`},
		{"environment injection", `{"mcpServers":{"acr":{"type":"stdio","command":"acr-mcp","args":["serve"],"env":{"HOME":"/tmp"}}}}`},
		{"wrong type", `{"mcpServers":{"acr":{"type":"http","command":"acr-mcp","args":["serve"]}}}`},
		{"extra field", `{"mcpServers":{"acr":{"type":"stdio","command":"acr-mcp","args":["serve"],"cwd":"/tmp"}}}`},
		{"empty command", `{"mcpServers":{"acr":{"type":"stdio","command":"","args":["serve"]}}}`},
		{"extra argument", `{"mcpServers":{"acr":{"type":"stdio","command":"acr-mcp","args":["serve","--debug"]}}}`},
		{"trailing document", `{"mcpServers":{"acr":{"type":"stdio","command":"acr-mcp","args":["serve"]}}}{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an untrusted registration variant.
			// When: it is parsed as a client configuration.
			_, err := ParseStdioJSON([]byte(tc.doc))
			// Then: only the canonical server shape is accepted.
			if err == nil {
				t.Fatal("unsafe registration was accepted")
			}
		})
	}
}

// TestParseCodexTOMLMatchesCanonicalModel structurally parses the checked-
// in codex-config.toml fixture and asserts every field matches the
// canonical model.
func TestParseCodexTOMLMatchesCanonicalModel(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs/examples/mcp-clients/codex-config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := ParseCodexTOML(data)
	if err != nil {
		t.Fatalf("ParseCodexTOML: %v", err)
	}
	if entry.Command != ExampleCommand {
		t.Fatalf("expected command %q, got %q", ExampleCommand, entry.Command)
	}
	if len(entry.Args) != 1 || entry.Args[0] != ServeArg {
		t.Fatalf("expected args [%q], got %#v", ServeArg, entry.Args)
	}
	if !entry.Enabled {
		t.Fatal("expected enabled = true")
	}
	if entry.Env["ACR_API_URL"] != ExampleAPIURL {
		t.Fatalf("expected ACR_API_URL %q, got %q", ExampleAPIURL, entry.Env["ACR_API_URL"])
	}
	if entry.Env["ACR_API_TOKEN_FILE"] != "/home/you/.acr/token" {
		t.Fatalf("expected an absolute ACR_API_TOKEN_FILE, got %q", entry.Env["ACR_API_TOKEN_FILE"])
	}
	if entry.Env["ACR_API_TIMEOUT"] != ExampleTimeout {
		t.Fatalf("expected ACR_API_TIMEOUT %q, got %q", ExampleTimeout, entry.Env["ACR_API_TIMEOUT"])
	}
}

// TestParseCodexTOMLRejectsUnrecognizedContent proves the narrow parser
// fails closed on content outside its supported subset, rather than
// silently ignoring or misparsing it.
func TestParseCodexTOMLRejectsUnrecognizedContent(t *testing.T) {
	if _, err := ParseCodexTOML([]byte("not an assignment or a table header\n")); err == nil {
		t.Fatal("expected an error for unrecognized content")
	}
	if _, err := ParseCodexTOML([]byte("[unexpected.table]\nfoo = \"bar\"\n")); err == nil {
		t.Fatal("expected an error for an unrecognized table")
	}
}

// TestParseLaunchScriptExecTargetsServeOnly structurally locates
// launch-sidecar.sh's final exec line and proves it targets exactly the
// sidecar binary variable and the "serve" subcommand.
func TestParseLaunchScriptExecTargetsServeOnly(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs/examples/mcp-clients/launch-sidecar.sh"))
	if err != nil {
		t.Fatal(err)
	}
	execLine, err := ParseLaunchScriptExec(data)
	if err != nil {
		t.Fatal(err)
	}
	if want := `exec "$ACR_MCP_BINARY" serve`; execLine != want {
		t.Fatalf("expected exec line %q, got %q", want, execLine)
	}
}
