# Codex CLI MCP Setup

[Codex](https://developers.openai.com/codex/mcp) (OpenAI's terminal coding agent) configures MCP servers with **TOML**, not JSON: `~/.codex/config.toml` for the user scope, or `.codex/config.toml` in a project root for the project scope (project-scoped servers require trusting the project on first use). TOML has no `${VAR}` expansion syntax, so paths in `config.toml` must be written out in full (or forwarded from the environment with `env_vars`/`bearer_token_env_var`, not interpolated).

## Configuration File

Add this table to `~/.codex/config.toml` (or `.codex/config.toml` for a project-scoped server):

```toml
[mcp_servers.acr]
command = "/path/to/acr-mcp"
args = ["serve"]
enabled = true

[mcp_servers.acr.env]
ACR_API_URL = "https://api.dev-health.example.com"
ACR_API_TOKEN_FILE = "/home/you/.acr/token"
```

A ready-to-copy template is at `codex-config.toml` in this directory.

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

3. **Add the server**, either by hand-editing `config.toml` as shown above, or with the CLI:
   ```bash
   codex mcp add acr --env ACR_API_URL=https://api.dev-health.example.com --env ACR_API_TOKEN_FILE="$HOME/.acr/token" -- /path/to/acr-mcp serve
   ```
   `codex mcp add` writes the `[mcp_servers.acr]` table into `~/.codex/config.toml` for you.

4. **Verify:**
   ```bash
   codex mcp list
   codex mcp get acr
   ```

5. **Use it:** run `codex` in your project directory (or anywhere, for the user-scoped entry). It should load the MCP configuration and make the ACR tools available; a project-scoped `.codex/config.toml` prompts for trust confirmation the first time.

## CLI Reference

```bash
# Add a server
codex mcp add acr -- /path/to/acr-mcp serve

# List configured servers
codex mcp list
codex mcp list --json

# Inspect one server
codex mcp get acr --json

# Remove a server
codex mcp remove acr
```

## Environment Variables

The sidecar reads these from the `[mcp_servers.acr.env]` table:

- `ACR_API_URL` (required): Base URL of the ACR API.
- `ACR_API_TOKEN_FILE` (required, or `ACR_API_TOKEN`/`ACR_API_TOKEN_KEYRING_SERVICE`): Absolute path to the token file -- write it out in full; TOML does not expand `$HOME` or `${HOME}`.
- `ACR_API_TIMEOUT` (optional): Request timeout as a Go duration string (e.g. `20s`). Default: `20s`.
- `ACR_API_PROXY_URL` (optional): HTTP proxy URL.
- `ACR_API_CA_BUNDLE` (optional): Path to a PEM-encoded CA bundle file.
- `ACR_ENABLE_WRITEBACK` (optional): Read only for `acr-mcp doctor` diagnostics; it does not enable `record_episode`, which is unavailable in this release (tracked under CHAOS-2909).

Codex also supports top-level fields on `[mcp_servers.acr]` itself (outside the `env` table), notably `enabled` (default `true`), `startup_timeout_sec`, and `cwd`; these are Codex-level knobs, not variables the sidecar reads.

## Token File Permissions

- Unix/Linux/macOS: the sidecar rejects a token file with group- or world-readable permissions; restrict it yourself first:
  ```bash
  chmod 600 ~/.acr/token
  ```
- Windows: `ACR_API_TOKEN_FILE` is not supported. The sidecar fails closed and refuses to load any token file on Windows; use `ACR_API_TOKEN` instead -- the OS keyring source is also macOS/Linux only.

## Codex CLI Environment Variables

If you prefer not to bake credentials into `config.toml`, you can still export the sidecar's own environment variables before running Codex, and reference them in the `env` table via `env_vars` forwarding, or run the sidecar manually to sanity-check it first:

```bash
export ACR_API_URL="https://api.dev-health.example.com"
export ACR_API_TOKEN_FILE="$HOME/.acr/token"
acr-mcp doctor
```

## Troubleshooting

### MCP Server Not Loading

- Check that the binary path is correct and the file is executable.
- Verify the TOML syntax (`codex mcp get acr --json` will surface a parse error).
- A project-scoped `.codex/config.toml` server will not start until you have trusted the project.

### "ACR API credential is not configured"

- Verify the token file exists and is readable.
- Check that `ACR_API_TOKEN_FILE` points to the correct absolute path.
- Ensure the token file is not empty.

### "ACR_API_URL is not configured"

- Verify `ACR_API_URL` is set in the `env` table (or as a process environment variable, if you're launching Codex with it exported).
- Re-run `codex mcp get acr` after updating the config.

### Timeout or Slow Responses

- Increase `ACR_API_TIMEOUT` in the `env` table.
- Check network connectivity to the API server.

## Example: Full Configuration (binary on PATH)

This example uses `command = "acr-mcp"` and relies on the binary being on `PATH` (for example via `go install ./cmd/acr-mcp` or a symlink into a directory already on `PATH`). If it is not on `PATH`, use the absolute path to your built binary instead.

```toml
[mcp_servers.acr]
command = "acr-mcp"
args = ["serve"]
enabled = true
startup_timeout_sec = 10.0

[mcp_servers.acr.env]
ACR_API_URL = "https://api.dev-health.example.com"
ACR_API_TOKEN_FILE = "/home/you/.acr/token"
ACR_API_TIMEOUT = "60s"
```

## Next Steps

- See `docs/mcp-sidecar.md` for detailed configuration and troubleshooting.
- Run `acr-mcp doctor` to verify your setup.
- Official reference: <https://developers.openai.com/codex/mcp>
