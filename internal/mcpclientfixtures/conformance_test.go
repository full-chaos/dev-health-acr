package mcpclientfixtures

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientConformance(t *testing.T) {
	// Given
	root := findRepoRoot(t)
	bundle, err := LoadClientBundle(filepath.Join(root, "clients", "conformance", "client-bundle.v1.json"))
	if err != nil {
		t.Fatal(err)
	}

	// When
	err = ValidateClientPackageRoots(root, bundle)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"context", "evidence", "unavailable"} {
		if output, ok := bundle.ConformanceExpectations()[name]; !ok || !output.Visible {
			t.Fatalf("scenario %q is not visibly conformant: %#v", name, output)
		}
	}
	t.Log("CLIENT_CONFORMANCE_OK clients=opencode,claude-code,codex,cursor semantics=shared")
}

func TestClientServeCommand(t *testing.T) {
	// Given
	root := findRepoRoot(t)
	configurations := []struct {
		name  string
		path  string
		parse func([]byte) (StdioServerEntry, error)
	}{
		{"opencode", "clients/opencode/config/opencode.json", ParseCommandArrayMCP},
		{"claude-code", "clients/claude-code/marketplace/plugins/context-fabric/.mcp.json", ParseClaudeCodeJSON},
		{"codex", "clients/codex/marketplace/plugins/context-fabric/.mcp.json", ParseCodexJSON},
		{"cursor", "clients/cursor/mcp.json", ParseStdioJSON},
	}

	for _, configuration := range configurations {
		t.Run(configuration.name, func(t *testing.T) {
			// When
			raw, err := os.ReadFile(filepath.Join(root, configuration.path))
			if err != nil {
				t.Fatal(err)
			}
			entry, err := configuration.parse(raw)

			// Then
			if err != nil {
				t.Fatal(err)
			}
			if entry.Command != "acr-mcp" || len(entry.Args) != 1 || entry.Args[0] != "serve" {
				t.Fatalf("registration = command=%q args=%q; want acr-mcp serve", entry.Command, entry.Args)
			}
		})
	}

	// Given
	policy, err := os.ReadFile(filepath.Join(root, "clients", "codex", "marketplace", "plugins", "context-fabric", "agents", "openai.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if err := ParseCodexPolicyYAML(policy); err != nil {
		t.Fatalf("ParseCodexPolicyYAML: %v", err)
	}
}

func TestParseCodexPolicyYAMLRejectsNonCanonicalPolicy(t *testing.T) {
	for _, policy := range []string{
		"policy:\n  allow_implicit_invocation: true\n",
		"policy:\n  allow_implicit_invocation: false\n  extra: value\n",
		"policy:\n  allow_implicit_invocation: false\n# trailing content\n",
	} {
		if err := ParseCodexPolicyYAML([]byte(policy)); err == nil {
			t.Fatal("noncanonical Codex policy was accepted")
		}
	}
}
