# ACR MCP Client Setup Examples

This directory contains setup guides and configuration templates for integrating the ACR MCP sidecar with various IDEs and MCP clients.

## Quick Start

1. **Build the sidecar:**
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
   `fcacr_your_token_here` is a placeholder, not a real token shape -- see [Token Format](../../mcp-sidecar.md#token-format) in the main sidecar doc.

3. **Choose your IDE or client:**
   - [Claude Code](claude-code.md) - Anthropic's CLI coding agent
   - [Cursor](cursor.md) - Cursor IDE
   - [Codex](codex.md) - OpenAI Codex CLI
   - [Generic STDIO](generic-stdio.md) - Any MCP-compatible client

## Configuration Templates

Ready-to-use configuration files:

- `claude-code-mcp.json` - For Claude Code (project-scoped `.mcp.json`, or copy the `acr` entry into the `mcpServers` key of user-scoped `~/.claude.json`)
- `cursor-mcp-config.json` - For Cursor (`.cursor/mcp.json` project-scoped, or `~/.cursor/mcp.json` global)
- `codex-config.toml` - For Codex CLI (`~/.codex/config.toml` user-scoped, or `.codex/config.toml` project-scoped; **TOML, not JSON**)

## Shell Script

- `launch-sidecar.sh` - Generic launcher script for STDIO transport

## Common Setup Steps

### 1. Build the Binary

```bash
cd /path/to/acr
go build -o acr-mcp ./cmd/acr-mcp
```

### 2. Create a Token File

```bash
mkdir -p ~/.acr
echo "fcacr_your_token_here" > ~/.acr/token
chmod 600 ~/.acr/token
```

Replace `fcacr_your_token_here` with your actual API token (the real shape is `fcacr_` followed by 43 base64url characters -- see [Token Format](../../mcp-sidecar.md#token-format)).

### 3. Verify the Setup

```bash
export ACR_API_URL="https://api.dev-health.example.com"
export ACR_API_TOKEN_FILE="$HOME/.acr/token"
/path/to/acr-mcp doctor
```

This should output a JSON report with status `ok`.

### 4. Configure Your IDE

Follow the guide for your IDE:
- [Claude Code](claude-code.md)
- [Cursor](cursor.md)
- [Codex](codex.md)
- [Generic STDIO](generic-stdio.md)

## Environment Variables

The sidecar reads these environment variables:

- `ACR_API_URL` (required): Base URL of the ACR API.
- `ACR_API_TOKEN`, an OS keyring entry (`ACR_API_TOKEN_KEYRING_SERVICE`), or `ACR_API_TOKEN_FILE`: API credential (checked in that precedence order; at least one source must resolve).
- `ACR_API_TIMEOUT` (optional): Request timeout as a Go duration string (e.g. `20s`). Default: `20s`.
- `ACR_API_PROXY_URL` (optional): HTTP proxy URL.
- `ACR_API_CA_BUNDLE` (optional): Path to a PEM-encoded CA bundle file.
- `ACR_API_MAX_RESPONSE_BYTES` / `ACR_API_MAX_REQUEST_BODY_BYTES` (optional): Response/request body size caps in bytes. Defaults: `1048576` / `262144`.
- `ACR_ENABLE_WRITEBACK` (optional): Boolean (`true`/`false`). When `true`, enables the `record_episode` tool if all four gates pass: (1) this flag is `true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Default: `false`. Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; doctor --live diagnoses the hosted gates.

## Security Notes

- Never commit token files to version control.
- Use `ACR_API_TOKEN_FILE` for persistent processes on macOS/Linux (preferred there; not supported on Windows -- see below).
- Use `ACR_API_TOKEN` for agent-based workflows (less secure).
- On Unix/Linux/macOS, token files must have permissions `0600`; the sidecar refuses to load a group- or world-readable file. **`ACR_API_TOKEN_FILE` is not supported on Windows**: the sidecar fails closed and refuses to load any token file there; use `ACR_API_TOKEN` instead -- the OS keyring source is also macOS/Linux only.
- Tokens are never logged or printed by the sidecar.

## Troubleshooting

### "ACR API credential is not configured"

- Verify the token file exists: `ls -la ~/.acr/token`
- Verify the token file is not empty: `wc -c ~/.acr/token`
- Verify the environment variable is set: `echo $ACR_API_TOKEN_FILE`

### "ACR_API_URL is not configured"

- Verify the API URL is set: `echo $ACR_API_URL`
- Verify the URL is correct and reachable.

### "ACR_API_URL" set but doctor reports `invalid_configuration`

- The value is nonblank but fails validation: it must be `https://` (or `http://` against an explicit loopback fixture with `ACR_API_ALLOW_INSECURE_LOOPBACK=true`), with no embedded userinfo, path, query string, or fragment -- scheme and host only.
- Run `acr-mcp doctor` and read the `api_url` check's `detail` field for the specific reason.

### "ACR API credential is configured but malformed"

- Verify the token starts with `fcacr_` and is exactly 49 characters (`fcacr_` + 43 base64url characters): `head -c 10 ~/.acr/token` (prefix) and `wc -c < ~/.acr/token` (length, remembering a trailing newline adds 1).
- Verify the token is not truncated or corrupted, and is not a Dev Health license key (a different credential type, not accepted here).

### Permission Denied

- Ensure the sidecar binary is executable: `chmod +x /path/to/acr-mcp`
- Ensure the token file has correct permissions: `chmod 600 ~/.acr/token` (Unix/Linux/macOS only -- see Security Notes above for Windows).

### Timeout or Slow Responses

- Increase `ACR_API_TIMEOUT` (must be between `1s` and `2m`): `export ACR_API_TIMEOUT="60s"`
- Check network connectivity to the API server.

## Next Steps

- See `docs/mcp-sidecar.md` for detailed configuration and troubleshooting.
- Run `acr-mcp doctor` to verify your setup.
- Check the hosted API documentation for tool schemas and examples.

## Support

For issues or questions, refer to:
- `docs/mcp-sidecar.md` - Comprehensive sidecar documentation
- IDE-specific guides in this directory
- Hosted API documentation
