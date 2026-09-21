package mcpclientfixtures

import "fmt"

// This file is the canonical model for the remote (hosted, Streamable HTTP)
// client configs under docs/examples/mcp-clients/. The STDIO templates in
// canonical.go launch a local acr-mcp process; these point a client at an
// already running `acr-mcp serve --transport=http` and send the caller's own
// bearer with every request. The hosted server holds no credential of its own,
// so the only secret a client config ever names is an environment variable that
// the client expands when it connects. A literal token never appears in any
// rendered config, and RemoteTokenEnvVar is the only credential name used.
const (
	// ExampleRemoteURL is the placeholder endpoint: the deployment host plus the
	// server's default base path (`--base-path`, default /mcp).
	ExampleRemoteURL = "https://acr-mcp.dev-health.example.com/mcp"
	// RemoteServerName is the server key every remote fixture registers.
	RemoteServerName = "acr"
	// RemoteTokenEnvVar names the environment variable that holds the caller's
	// ACR API token. Each client expands it at connect time.
	RemoteTokenEnvVar = "ACR_MCP_TOKEN"
	// RemoteProtocolRevision is the MCP revision the hosted server lists first.
	// It is a server fact; whether a client speaks it is a client fact recorded
	// in docs/mcp-sidecar.md, never a reason to change the server.
	RemoteProtocolRevision = "2026-07-28"
)

// RenderClaudeCodeRemoteJSON renders the Claude Code project-scoped `.mcp.json`
// entry for a remote HTTP server. Claude Code expands ${VAR} in `url` and
// `headers`.
func RenderClaudeCodeRemoteJSON() string {
	return fmt.Sprintf(`{
  "mcpServers": {
    %q: {
      "type": "http",
      "url": %q,
      "headers": {
        "Authorization": "Bearer ${%s}"
      }
    }
  }
}
`, RemoteServerName, ExampleRemoteURL, RemoteTokenEnvVar)
}

// RenderClaudeCodeRemoteAddCommand renders the `claude mcp add` form. The
// header value is single-quoted so the shell passes ${ACR_MCP_TOKEN} through
// unexpanded and the token is never written to the client's config file.
func RenderClaudeCodeRemoteAddCommand() string {
	return fmt.Sprintf("claude mcp add --transport http %s %s --header 'Authorization: Bearer ${%s}'",
		RemoteServerName, ExampleRemoteURL, RemoteTokenEnvVar)
}

// RenderCursorRemoteJSON renders the Cursor `.cursor/mcp.json` remote entry.
// Cursor interpolates ${env:NAME}.
func RenderCursorRemoteJSON() string {
	return fmt.Sprintf(`{
  "mcpServers": {
    %q: {
      "url": %q,
      "headers": {
        "Authorization": "Bearer ${env:%s}"
      }
    }
  }
}
`, RemoteServerName, ExampleRemoteURL, RemoteTokenEnvVar)
}

// RenderOpenCodeRemoteJSON renders the OpenCode v1 `opencode.json` remote
// entry (servers directly under "mcp"). OpenCode substitutes {env:NAME}.
func RenderOpenCodeRemoteJSON() string {
	return fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    %q: {
      "type": "remote",
      "url": %q,
      "enabled": true,
      "headers": {
        "Authorization": "Bearer {env:%s}"
      }
    }
  }
}
`, RemoteServerName, ExampleRemoteURL, RemoteTokenEnvVar)
}

// RenderOpenCodeV2RemoteJSON renders the OpenCode v2 `opencode.json` remote
// entry: servers nest under "mcp.servers", OAuth is on by default so it is
// switched off (the bearer header is the credential), and "protocol" is left
// on negotiation so the client can use 2026-07-28 against this server.
func RenderOpenCodeV2RemoteJSON() string {
	return fmt.Sprintf(`{
  "mcp": {
    "servers": {
      %q: {
        "type": "remote",
        "url": %q,
        "oauth": false,
        "protocol": "auto",
        "headers": {
          "Authorization": "Bearer {env:%s}"
        }
      }
    }
  }
}
`, RemoteServerName, ExampleRemoteURL, RemoteTokenEnvVar)
}

const codexRemoteHeaderComment = `# Example ACR MCP remote (hosted) server entry for Codex CLI.
# Codex sends the value of the named environment variable as
# "Authorization: Bearer <value>" on every request. Export the variable in the
# shell that starts Codex; never write the token into this file. Place this
# table in ~/.codex/config.toml (user scope) or .codex/config.toml (project
# scope, requires trusting the project on first use).
`

// RenderCodexRemoteTOML renders the Codex CLI `config.toml` remote server
// entry. `bearer_token_env_var` is the Codex-native bearer form.
func RenderCodexRemoteTOML() string {
	return fmt.Sprintf(`%s
[mcp_servers.%s]
url = %q
bearer_token_env_var = %q
enabled = true
`, codexRemoteHeaderComment, RemoteServerName, ExampleRemoteURL, RemoteTokenEnvVar)
}

// RemoteClient names one client this package can print a remote config for.
type RemoteClient string

const (
	RemoteClaudeCode RemoteClient = "claude-code"
	RemoteCodex      RemoteClient = "codex"
	RemoteCursor     RemoteClient = "cursor"
	RemoteOpenCode   RemoteClient = "opencode"
	// RemoteOpenCodeV2 is the OpenCode v2 config shape, which is not
	// accepted by v1 and does not accept v1's.
	RemoteOpenCodeV2 RemoteClient = "opencode-v2"
)

// RemoteClients lists every client with a remote fixture, in a fixed order.
var RemoteClients = []RemoteClient{RemoteClaudeCode, RemoteCodex, RemoteCursor, RemoteOpenCode, RemoteOpenCodeV2}

// RenderRemote returns the remote config for one client, or false for a client
// without a remote fixture.
func RenderRemote(client RemoteClient) (string, bool) {
	switch client {
	case RemoteClaudeCode:
		return RenderClaudeCodeRemoteJSON(), true
	case RemoteCodex:
		return RenderCodexRemoteTOML(), true
	case RemoteCursor:
		return RenderCursorRemoteJSON(), true
	case RemoteOpenCode:
		return RenderOpenCodeRemoteJSON(), true
	case RemoteOpenCodeV2:
		return RenderOpenCodeV2RemoteJSON(), true
	}
	return "", false
}
