# Claude Code MCP Setup

[Claude Code](https://code.claude.com/docs/en/mcp) (Anthropic's CLI coding agent -- not the Claude Desktop consumer app) integrates MCP servers through a project-scoped `.mcp.json` file or a user-scoped entry in `~/.claude.json`. This guide covers both, plus the `claude mcp add` CLI shortcut.

## Configuration File

### Project scope (shared via git): `.mcp.json`

Create `.mcp.json` in your project root:

```json
{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": "/path/to/acr-mcp",
      "args": ["serve"],
      "env": {
        "ACR_API_URL": "https://api.dev-health.example.com",
        "ACR_API_TOKEN_FILE": "${HOME}/.acr/token"
      }
    }
  }
}
```

`.mcp.json` is meant to be committed so the whole team gets the same server definitions; keep the actual token out of it (use `ACR_API_TOKEN_FILE`, not `ACR_API_TOKEN`, for anything checked into git-adjacent config).

### User scope (all projects): `~/.claude.json`

For a server available in every project, add the same `mcpServers` entry under the top-level `mcpServers` key of `~/.claude.json` (this file also stores Claude Code's own state, so edit it carefully and keep a backup):

```json
{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": "acr-mcp",
      "args": ["serve"],
      "env": {
        "ACR_API_URL": "https://api.dev-health.example.com",
        "ACR_API_TOKEN_FILE": "${HOME}/.acr/token"
      }
    }
  }
}
```

On Windows, `~/.claude.json` resolves to `%USERPROFILE%\.claude.json`.

## Setup Steps

1. **Build the sidecar binary:**
   ```bash
   cd /path/to/acr
   go build -o acr-mcp ./cmd/acr-mcp
   ```

2. **Create a token file:**
   ```bash
   mkdir -p ~/.acr
   echo "fcacr_your_token_here" > ~/.acr/token
   chmod 600 ~/.acr/token
   ```
   `fcacr_your_token_here` is a placeholder, not a real token shape -- see [Token Format](../../mcp-sidecar.md#token-format) in the main sidecar doc for the exact `fcacr_` + 43-character shape. Replace it with your actual credential.

3. **Add the server**, either by hand-editing `.mcp.json` / `~/.claude.json` as shown above, or with the CLI:
   ```bash
   claude mcp add --scope project --transport stdio acr --env ACR_API_URL=https://api.dev-health.example.com --env ACR_API_TOKEN_FILE="$HOME/.acr/token" -- /path/to/acr-mcp serve
   ```
   Use `--scope user` instead of `--scope project` for the `~/.claude.json` (all-projects) form. The `--` separates Claude's own flags from the server's command and arguments.

4. **Verify Claude Code sees the server:**
   ```bash
   claude mcp list
   claude mcp get acr
   ```

5. **Use it:** In a Claude Code session, ask it to use the ACR tools. It should have access to `context_for_task` and `source_evidence`.

## CLI Reference

```bash
# List configured servers
claude mcp list

# Inspect one server
claude mcp get acr

# Remove a server
claude mcp remove acr

# Add directly from a JSON blob
claude mcp add-json acr '{"type":"stdio","command":"acr-mcp","args":["serve"],"env":{"ACR_API_URL":"https://api.dev-health.example.com"}}'
```

## Environment Variables

The sidecar reads these from the `env` block:

- `ACR_API_URL` (required): Base URL of the ACR API.
- `ACR_API_TOKEN_FILE` (required, or `ACR_API_TOKEN`/`ACR_API_TOKEN_KEYRING_SERVICE`): Path to the token file. Use `${HOME}` for the home directory -- Claude Code expands `${VAR}` / `${VAR:-default}` inside `command`, `args`, `env`, `url`, and `headers`.
- `ACR_API_TIMEOUT` (optional): Request timeout as a Go duration string (e.g. `20s`). Default: `20s`.
- `ACR_API_PROXY_URL` (optional): HTTP proxy URL.
- `ACR_API_CA_BUNDLE` (optional): Path to a PEM-encoded CA bundle file.
- `ACR_ENABLE_WRITEBACK` (optional): Read only for `acr-mcp doctor` diagnostics; it does not enable `record_episode`, which is unavailable in this release (tracked under CHAOS-2909).

## Token File Permissions

- Unix/Linux/macOS: the sidecar rejects a token file with group- or world-readable permissions; restrict it yourself first:
  ```bash
  chmod 600 ~/.acr/token
  ```
- Windows: `ACR_API_TOKEN_FILE` is not supported. The sidecar fails closed and refuses to load any token file on Windows; use `ACR_API_TOKEN` instead -- the OS keyring source is also macOS/Linux only.

## Troubleshooting

### MCP Server Not Appearing

- Check that the binary path is correct and the file is executable.
- Verify the config file syntax with `claude mcp get acr` (it will report a parse error if the JSON is invalid).
- Run `claude mcp list` to confirm the server is registered at the scope you expect (project `.mcp.json` vs. user `~/.claude.json`).

### "ACR API credential is not configured"

- Verify the token file exists and is readable.
- Check that `ACR_API_TOKEN_FILE` points to the correct path.
- Ensure the token file is not empty.

### "ACR_API_URL is not configured"

- Verify `ACR_API_URL` is set in the `env` block.
- Re-run `claude mcp get acr` after updating the config; Claude Code re-reads the file the next time it starts the server.

### Timeout or Slow Responses

- Increase `ACR_API_TIMEOUT` in the `env` block.
- Check network connectivity to the API server.

## Example: Full Configuration (binary on PATH)

This example uses `"command": "acr-mcp"` and relies on the binary being on `PATH` (for example via `go install ./cmd/acr-mcp` or a symlink into a directory already on `PATH`). If it is not on `PATH`, use the absolute path to your built binary instead.

```json
{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": "acr-mcp",
      "args": ["serve"],
      "env": {
        "ACR_API_URL": "https://api.dev-health.example.com",
        "ACR_API_TOKEN_FILE": "${HOME}/.acr/token",
        "ACR_API_TIMEOUT": "60s"
      }
    }
  }
}
```

## Next Steps

- See `docs/mcp-sidecar.md` for detailed configuration and troubleshooting.
- Run `acr-mcp doctor` to verify your setup.
- Official reference: <https://code.claude.com/docs/en/mcp>
