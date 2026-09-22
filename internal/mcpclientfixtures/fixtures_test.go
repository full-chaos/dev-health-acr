package mcpclientfixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findRepoRoot walks up from the current package directory to the
// repository root (identified by go.mod plus a contracts/ directory),
// mirroring internal/mcp/schemas_parity_test.go's helper of the same name.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	current, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(current, "contracts")); err == nil {
				return current
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatalf("could not find repository root above %s", current)
		}
		current = parent
	}
}

// TestFixturesMatchCanonicalModel is the deterministic checker: it proves
// each checked-in client config under docs/examples/mcp-clients/ is
// byte-for-byte what this package's canonical model renders, so the three
// configs cannot silently drift out of sync with each other or be hand-
// edited into an inconsistent shape without this test catching it.
func TestFixturesMatchCanonicalModel(t *testing.T) {
	root := findRepoRoot(t)

	cases := []struct {
		relPath string
		render  func() string
	}{
		{"docs/examples/mcp-clients/claude-code-mcp.json", RenderClaudeCodeJSON},
		{"docs/examples/mcp-clients/cursor-mcp-config.json", RenderCursorJSON},
		{"docs/examples/mcp-clients/codex-config.toml", RenderCodexTOML},
	}
	for _, tc := range cases {
		t.Run(tc.relPath, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(root, tc.relPath))
			if err != nil {
				t.Fatal(err)
			}
			got := tc.render()
			if string(want) != got {
				t.Fatalf("%s has drifted from the canonical model.\n--- checked-in file ---\n%s\n--- canonical render ---\n%s", tc.relPath, want, got)
			}
		})
	}
}

// TestRenderersAreDeterministic proves calling each Render* function twice
// produces byte-identical output, so a checker running it repeatedly (or
// across processes) never flakes.
func TestRenderersAreDeterministic(t *testing.T) {
	renderers := []func() string{RenderClaudeCodeJSON, RenderCursorJSON, RenderCodexTOML}
	for _, render := range renderers {
		if render() != render() {
			t.Fatal("expected a deterministic render, got differing output across two calls")
		}
	}
}

// TestJSONFixturesInvokeServeWithNoOtherArgs is a lighter-weight, package-
// local restatement of internal/mcp's TestClaudeCodeAndCursorFixturesLaunchServeCorrectly
// canary, scoped to the canonical model itself rather than the file on
// disk: it fails fast, without a repo-root file read, if a future change
// to renderStdioJSON ever stops emitting exactly `"args": ["serve"]`.
func TestJSONFixturesInvokeServeWithNoOtherArgs(t *testing.T) {
	for _, render := range []func() string{RenderClaudeCodeJSON, RenderCursorJSON} {
		content := render()
		if want := `"args": ["serve"]`; !strings.Contains(content, want) {
			t.Fatalf("expected rendered JSON to contain %q, got:\n%s", want, content)
		}
	}
}

// TestClientSkillsCoverTheInvestigationFlow: every packaged STDIO skill names
// the investigate -> result -> evidence tools, the receipt handoff, and the
// untrusted-content rule. The STDIO package contract (mcp_commands, enabled
// tools) is unchanged and asserted by the package-tree tests. Remote (hosted)
// server client configuration and skill text now live in the public
// context-fabric-agents repo (CHAOS-6209), so this no longer asserts
// remote-only content such as repository.slug.
func TestClientSkillsCoverTheInvestigationFlow(t *testing.T) {
	root := findRepoRoot(t)
	skills := []string{
		"clients/claude-code/marketplace/plugins/context-fabric/skills/context-fabric/SKILL.md",
		"clients/codex/marketplace/plugins/context-fabric/skills/context-fabric/SKILL.md",
		"clients/opencode/config/skills/context-fabric/SKILL.md",
		"clients/cursor/skills/context-fabric/SKILL.md",
	}
	for _, rel := range skills {
		t.Run(rel, func(t *testing.T) {
			text := string(readDoc(t, root, rel))
			for _, needle := range []string{
				"context_for_task", "source_evidence", "investigate_question", "investigation_result",
				"parent_result_id", "prior_*_receipts", "single `evidence_ref_id` argument", "acr://guide/", "untrusted",
				"context-fabric-agents",
			} {
				if !strings.Contains(text, needle) {
					t.Errorf("%s lacks %q", rel, needle)
				}
			}
		})
	}
}
