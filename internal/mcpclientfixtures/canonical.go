// Package mcpclientfixtures is the canonical, single-source-of-truth model
// for the machine-readable MCP client config templates under
// docs/examples/mcp-clients/: claude-code-mcp.json, cursor-mcp-config.json,
// and codex-config.toml. Each Render* function deterministically renders
// one client's config from the same shared example values (command, API
// URL, timeout), so the three fixtures cannot silently drift apart from
// each other or from the "serve"-only invocation the real STDIO transport
// (internal/mcp) actually implements. See fixtures_test.go for the
// deterministic checker that fails if a checked-in fixture ever diverges
// from what this package generates.
package mcpclientfixtures

import "fmt"

// Canonical example values shared by every generated client config below.
// Changing one of these here is the single place that needs to change to
// keep every generated fixture, and the checker that locks them to disk,
// in sync.
const (
	ExampleCommand = "/path/to/acr-mcp"
	ExampleAPIURL  = "https://api.dev-health.example.com"
	ExampleTimeout = "30s"
	ServeArg       = "serve"
)

// RenderClaudeCodeJSON renders the Claude Code project-scoped `.mcp.json`
// STDIO server entry template.
func RenderClaudeCodeJSON() string {
	return renderStdioJSON()
}

// RenderCursorJSON renders the Cursor `.cursor/mcp.json` STDIO server
// entry template.
func RenderCursorJSON() string {
	return renderStdioJSON()
}

// renderStdioJSON is the shared template for both JSON-shaped client
// configs above.
func renderStdioJSON() string {
	return fmt.Sprintf(`{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": %q,
      "args": [%q],
      "env": {
        "ACR_API_URL": %q,
        "ACR_API_TIMEOUT": %q
      }
    }
  }
}
`, ExampleCommand, ServeArg, ExampleAPIURL, ExampleTimeout)
}

// codexHeaderComment is the fixed explanatory header codex-config.toml
// carries above its [mcp_servers.acr] table: TOML has no `${VAR}`
// expansion, so the guidance about writing paths out in full lives here
// rather than in a shared narrative doc.
const codexHeaderComment = `# Example ACR MCP server entry for Codex CLI.
# Codex uses TOML, not JSON: place this table in ~/.codex/config.toml (user
# scope) or .codex/config.toml (project scope, requires trusting the
# project on first use). See codex.md in this directory for full setup
# steps and CLI-based alternatives (` + "`codex mcp add`" + `).
`

// RenderCodexTOML renders the Codex CLI `config.toml` STDIO server entry
// template.
func RenderCodexTOML() string {
	return fmt.Sprintf(`%s
[mcp_servers.acr]
command = %q
args = [%q]
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = %q
ACR_API_TIMEOUT = %q
`, codexHeaderComment, ExampleCommand, ServeArg, ExampleAPIURL, ExampleTimeout)
}
