package mcpclientfixtures

import "fmt"

// This file holds the canonical model for the narrative-doc snippet
// *variants* that intentionally differ from the primary
// claude-code-mcp.json/cursor-mcp-config.json/codex-config.toml templates
// in canonical.go: a bare on-PATH command instead of an explicit path, an
// overridden timeout, or an extra Codex-only field. Each variant still has
// exactly one generator here, so a stale hand-edited copy in a .md guide
// is caught the same way canonical.go's primary templates are.

// stdioJSONOptions parameterizes renderStdioJSONWith below: every JSON-
// shaped doc snippet variant (on-PATH command, overridden timeout, no
// timeout at all) is one call with a different set of fields, rather than
// a hand-duplicated literal per variant.
type stdioJSONOptions struct {
	Command        string
	TokenFileValue string
	Timeout        string // empty omits the ACR_API_TIMEOUT line entirely
}

func renderStdioJSONWith(opts stdioJSONOptions) string {
	if opts.Timeout == "" {
		return fmt.Sprintf(`{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": %q,
      "args": [%q],
      "env": {
        "ACR_API_URL": %q,
        "ACR_API_TOKEN_FILE": %q
      }
    }
  }
}
`, opts.Command, ServeArg, ExampleAPIURL, opts.TokenFileValue)
	}
	return fmt.Sprintf(`{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": %q,
      "args": [%q],
      "env": {
        "ACR_API_URL": %q,
        "ACR_API_TOKEN_FILE": %q,
        "ACR_API_TIMEOUT": %q
      }
    }
  }
}
`, opts.Command, ServeArg, ExampleAPIURL, opts.TokenFileValue, opts.Timeout)
}

// RenderClaudeCodeUserScopeJSON renders claude-code.md's user-scope
// (~/.claude.json) variant: a bare "acr-mcp" command relying on PATH,
// no ACR_API_TIMEOUT line.
func RenderClaudeCodeUserScopeJSON() string {
	return renderStdioJSONWith(stdioJSONOptions{Command: "acr-mcp", TokenFileValue: "${HOME}/.acr/token"})
}

// RenderClaudeCodeFullExampleJSON renders claude-code.md's "Example: Full
// Configuration (binary on PATH)" variant: a bare "acr-mcp" command and an
// explicit 60s timeout override.
func RenderClaudeCodeFullExampleJSON() string {
	return renderStdioJSONWith(stdioJSONOptions{Command: "acr-mcp", TokenFileValue: "${HOME}/.acr/token", Timeout: "60s"})
}

// RenderCursorSetupStepJSON renders cursor.md's "Create the config
// directory and file" heredoc variant: identical fields to
// RenderCursorJSON's primary template, minus ACR_API_TIMEOUT.
func RenderCursorSetupStepJSON() string {
	return renderStdioJSONWith(stdioJSONOptions{Command: ExampleCommand, TokenFileValue: "${env:HOME}/.acr/token"})
}

// RenderCursorFullExampleJSON renders cursor.md's "Example: Full
// Configuration (binary on PATH)" variant: a bare "acr-mcp" command and an
// explicit 60s timeout override.
func RenderCursorFullExampleJSON() string {
	return renderStdioJSONWith(stdioJSONOptions{Command: "acr-mcp", TokenFileValue: "${env:HOME}/.acr/token", Timeout: "60s"})
}

// RenderCodexTOMLDocSnippet renders codex.md's top "Configuration File"
// TOML variant: no header comment (that lives only in the standalone
// codex-config.toml file) and no ACR_API_TIMEOUT line.
func RenderCodexTOMLDocSnippet() string {
	return fmt.Sprintf(`[mcp_servers.acr]
command = %q
args = [%q]
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = %q
ACR_API_TOKEN_FILE = %q
`, ExampleCommand, ServeArg, ExampleAPIURL, "/home/you/.acr/token")
}

// RenderCodexTOMLFullExample renders codex.md's "Example: Full
// Configuration (binary on PATH)" TOML variant: a bare "acr-mcp" command,
// the Codex-only startup_timeout_sec field, and an explicit 60s
// ACR_API_TIMEOUT override.
func RenderCodexTOMLFullExample() string {
	return fmt.Sprintf(`[mcp_servers.acr]
command = %q
args = [%q]
enabled = true
startup_timeout_sec = 10.0

[mcp_servers.acr.env]
ACR_API_URL = %q
ACR_API_TOKEN_FILE = %q
ACR_API_TIMEOUT = %q
`, "acr-mcp", ServeArg, ExampleAPIURL, "/home/you/.acr/token", "60s")
}
