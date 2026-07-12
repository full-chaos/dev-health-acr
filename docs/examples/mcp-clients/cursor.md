# Cursor MCP Setup

[Cursor](https://cursor.com/docs/mcp) integrates MCP servers via a `mcp.json` file: `.cursor/mcp.json` in your project root, or `~/.cursor/mcp.json` for a global (all-projects) server. Cursor has no CLI for adding servers -- configuration is a manual JSON edit, managed afterwards from **Settings -> Tools & MCP**.

## Configuration File

Create or edit `.cursor/mcp.json` in your project root, or `~/.cursor/mcp.json` globally (`%USERPROFILE%\.cursor\mcp.json` on Windows):

```json
{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": "/path/to/acr-mcp",
      "args": ["serve"],
      "env": {
        "ACR_API_URL": "https://api.dev-health.example.com",
        "ACR_API_TOKEN_FILE": "${env:HOME}/.acr/token"
      }
    }
  }
}
```

`"type": "stdio"` is a required field for local command-based servers per Cursor's documented STDIO server configuration schema (<https://cursor.com/docs/mcp#stdio-server-configuration>) -- omitting it can cause the server to be misidentified as a different transport and fail to register.

A ready-to-copy template is at `cursor-mcp-config.json` in this directory.

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

3. **Create the config directory and file** (project scope shown; swap `.cursor` for `~/.cursor` for the global scope):
   ```bash
   mkdir -p .cursor
   cat > .cursor/mcp.json << 'EOF'
   {
     "mcpServers": {
       "acr": {
         "type": "stdio",
         "command": "/path/to/acr-mcp",
         "args": ["serve"],
         "env": {
           "ACR_API_URL": "https://api.dev-health.example.com",
           "ACR_API_TOKEN_FILE": "${env:HOME}/.acr/token"
         }
       }
     }
   }
   EOF
   ```

4. **Update the binary path:**
   Replace `/path/to/acr-mcp` with the actual path to your built binary.

5. **Reload the MCP config:**
   Open the Command Palette (`Cmd+Shift+P` / `Ctrl+Shift+P`) and run "Reload Window", or open **Settings -> Tools & MCP** (`Cmd+Shift+J` / `Ctrl+Shift+J`) to confirm the server connected.

6. **Verify:**
   In **Settings -> Tools & MCP**, confirm `acr` shows a connected status and lists `context_for_task` and `source_evidence`.

## Environment Variables

The sidecar reads these from the `env` block:

- `ACR_API_URL` (required): Base URL of the ACR API.
- `ACR_API_TOKEN_FILE` (required, or `ACR_API_TOKEN`/`ACR_API_TOKEN_KEYRING_SERVICE`): Path to the token file. Cursor's interpolation syntax is `${env:VAR}` (not the `${VAR}` shorthand some other clients use), plus `${userHome}` and `${workspaceFolder}`.
- `ACR_API_TIMEOUT` (optional): Request timeout as a Go duration string (e.g. `20s`). Default: `20s`.
- `ACR_API_PROXY_URL` (optional): HTTP proxy URL.
- `ACR_API_CA_BUNDLE` (optional): Path to a PEM-encoded CA bundle file.
- `ACR_ENABLE_WRITEBACK` (optional): Boolean (`true`/`false`). When `true`, enables the `record_episode` tool if all four gates pass: (1) this flag is `true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Default: `false`. Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; doctor --live diagnoses the hosted gates.

## Token File Permissions

- Unix/Linux/macOS: the sidecar rejects a token file with group- or world-readable permissions; restrict it yourself first:
  ```bash
  chmod 600 ~/.acr/token
  ```
- Windows: `ACR_API_TOKEN_FILE` is not supported. The sidecar fails closed and refuses to load any token file on Windows; use `ACR_API_TOKEN` instead -- the OS keyring source is also macOS/Linux only.

## Project vs. Global Configuration

Cursor prefers the project-scoped `.cursor/mcp.json` over the global `~/.cursor/mcp.json` when both define the same server name. Use the global file for a server you want in every project; use the project file for one you want to share with your team via git.

## Troubleshooting

### MCP Server Not Appearing

- Check that the binary path is correct and the file is executable.
- Verify the config file syntax (valid JSON) -- Cursor surfaces a parse error in **Settings -> Tools & MCP** if `mcp.json` is malformed.
- Confirm you edited `.cursor/mcp.json` or `~/.cursor/mcp.json`, not `mcp_config.json` (an older, no-longer-current filename).

### "ACR API credential is not configured"

- Verify the token file exists and is readable.
- Check that `ACR_API_TOKEN_FILE` points to the correct path.
- Ensure the token file is not empty.

### "ACR_API_URL is not configured"

- Verify `ACR_API_URL` is set in the `env` block.
- Reload the window after updating the config.

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
        "ACR_API_TOKEN_FILE": "${env:HOME}/.acr/token",
        "ACR_API_TIMEOUT": "60s"
      }
    }
  }
}
```

## Next Steps

- See `docs/mcp-sidecar.md` for detailed configuration and troubleshooting.
- Run `acr-mcp doctor` to verify your setup.
- Official reference: <https://cursor.com/docs/mcp>
