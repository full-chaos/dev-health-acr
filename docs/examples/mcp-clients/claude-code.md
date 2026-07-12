# Claude Code MCP Setup

[Claude Code](https://code.claude.com/docs/en/mcp) (Anthropic's CLI coding agent -- not the Claude Desktop consumer app) integrates MCP servers through a project-scoped `.mcp.json` file or a user-scoped entry in `~/.claude.json`. This guide covers both, plus the `claude mcp add` CLI shortcut.

## Configuration File

### Project scope (shared via git): `.mcp.json`

Create `.mcp.json` in your project root:

```json
<!-- FIXTURE:claude-code-project-json -->
{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": "/path/to/acr-mcp",
      "args": ["serve"],
      "env": {
        "ACR_API_URL": "https://api.dev-health.example.com",
        "ACR_API_TOKEN_FILE": "${HOME}/.acr/token",
        "ACR_API_TIMEOUT": "30s"
      }
    }
  }
}
<!-- /FIXTURE:claude-code-project-json -->
```

`.mcp.json` is meant to be committed so the whole team gets the same server definitions; keep the actual token out of it (use `ACR_API_TOKEN_FILE`, not `ACR_API_TOKEN`, for anything checked into git-adjacent config).

### User scope (all projects): `~/.claude.json`

For a server available in every project, add the same `mcpServers` entry under the top-level `mcpServers` key of `~/.claude.json` (this file also stores Claude Code's own state, so edit it carefully and keep a backup):

```json
<!-- FIXTURE:claude-code-userscope-json -->
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
<!-- /FIXTURE:claude-code-userscope-json -->
```

On Windows, `~/.claude.json` resolves to `%USERPROFILE%\.claude.json`.

## Setup Steps

<!-- FIXTURE:install-sidecar -->
1. **Install the sidecar binary.** The normal install path is a signed
   private release download, not a local build. macOS/Linux:

   ```bash
   # Download the release archive for your OS/arch (e.g. acr-mcp_<version>_darwin_arm64.tar.gz)
   # plus SHA256SUMS and SHA256SUMS.sig from the private GitHub Releases page for
   # full-chaos/dev-health-acr. Do NOT trust a cosign.pub bundled alongside the
   # release assets -- obtain signing/cosign.pub from a reviewed commit in this
   # repository instead, then verify. set -euo pipefail so a failed git,
   # cosign, or checksum step halts here rather than falling through to
   # extract an unverified archive:
   set -euo pipefail
   git show <trusted-ref>:signing/cosign.pub > signing/cosign.pub
   cosign verify-blob --key signing/cosign.pub --signature SHA256SUMS.sig \
     --insecure-ignore-tlog SHA256SUMS
   archive="acr-mcp_<version>_<os>_<arch>.tar.gz"
   checksum_line="$(awk -v name="$archive" '$2 == name' SHA256SUMS)"
   test "$(printf '%s\n' "$checksum_line" | wc -l | tr -d ' ')" = 1
   if command -v sha256sum >/dev/null 2>&1; then
     printf '%s\n' "$checksum_line" | sha256sum --check -
   else
     printf '%s\n' "$checksum_line" | shasum -a 256 --check -
   fi
   tar -xzf "$archive"
   chmod +x acr-mcp
   ```

   See `docs/release-policy.md` for the full verification runbook.
   Windows users: see [Installing on Windows](#installing-on-windows) below.

   **Development only:** `go build` produces an unversioned `dev` binary. A
   production ACR API rejects a `dev`-identified sidecar outright (426 Upgrade
   Required, before any tool call is accepted) -- only use this against a
   non-production/test fixture API, never a real hosted ACR API:

   ```bash
   cd /path/to/acr
   go build -o acr-mcp ./cmd/acr-mcp
   ```

   To test a locally built binary against a hosted API that enforces a
   minimum sidecar version, set an explicit valid version override instead
   of relying on the compiled-in `dev` identity:

   ```bash
   export ACR_SIDECAR_VERSION="1.0.0"        # must satisfy the target API's minimum_sidecar_version
   export ACR_SIDECAR_CLIENT_VERSION="1.0.0"
   ```
<!-- /FIXTURE:install-sidecar -->

### Installing on Windows

<!-- FIXTURE:install-sidecar-windows -->
1. **Install the sidecar binary (Windows).** The normal install path is a
   signed private release download, not a local build:

   ```powershell
   # Download the release archive for your Windows build (e.g. acr-mcp_<version>_windows_amd64.zip)
   # plus SHA256SUMS and SHA256SUMS.sig from the private GitHub Releases page for
   # full-chaos/dev-health-acr. Do NOT trust a cosign.pub bundled alongside the
   # release assets -- obtain signing/cosign.pub from a reviewed commit in this
   # repository instead, then verify. $ErrorActionPreference = 'Stop' covers
   # any later failing cmdlet; git.exe and cosign.exe are native
   # executables, so $LASTEXITCODE is checked explicitly right after each:
   $ErrorActionPreference = 'Stop'
   git show <trusted-ref>:signing/cosign.pub > signing/cosign.pub
   if ($LASTEXITCODE -ne 0) { throw "git show failed with exit code $LASTEXITCODE" }
   cosign.exe verify-blob --key signing/cosign.pub --signature SHA256SUMS.sig `
     --insecure-ignore-tlog SHA256SUMS
   if ($LASTEXITCODE -ne 0) { throw "cosign verify-blob failed with exit code $LASTEXITCODE" }

   $archive = "acr-mcp_<version>_windows_amd64.zip"
   $line = @(Get-Content SHA256SUMS | Where-Object { $_.EndsWith("  $archive") })
   if ($line.Count -ne 1) { throw "expected exactly one checksum line for $archive" }
   $expectedHash = $line[0].Split(' ')[0]
   $actualHash = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant()
   if ($actualHash -ne $expectedHash) { throw "checksum mismatch for $archive" }

   Expand-Archive -Path $archive -DestinationPath .
   ```

   See `docs/release-policy.md` for the full verification runbook.
   There is no `chmod` equivalent on Windows: an extracted `.exe` is directly
   runnable.

   **Development only:** `go build` produces an unversioned `dev` binary. A
   production ACR API rejects a `dev`-identified sidecar outright (426 Upgrade
   Required, before any tool call is accepted) -- only use this against a
   non-production/test fixture API, never a real hosted ACR API:

   ```powershell
   cd C:\path\to\acr
   go build -o acr-mcp.exe .\cmd\acr-mcp
   ```

   To test a locally built binary against a hosted API that enforces a
   minimum sidecar version, set an explicit valid version override instead
   of relying on the compiled-in `dev` identity:

   ```powershell
   $env:ACR_SIDECAR_VERSION = "1.0.0"        # must satisfy the target API's minimum_sidecar_version
   $env:ACR_SIDECAR_CLIENT_VERSION = "1.0.0"
   ```
<!-- /FIXTURE:install-sidecar-windows -->

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

See [Proxy and Custom CA Configuration](proxy-and-custom-ca.md) for validation rules and bounds for both settings.
- `ACR_ENABLE_WRITEBACK` (optional): Boolean (`true`/`false`). When `true`, enables the `record_episode` tool if all four gates pass: (1) this flag is `true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Default: `false`. <!-- FIXTURE:doctor-gate-note -->Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; `doctor` diagnoses the hosted gates automatically once local configuration is valid (network-free otherwise), `doctor --offline` forces a network-free check regardless of configuration validity, and `doctor --live` is an explicit, equivalent alias for that automatic behavior.<!-- /FIXTURE:doctor-gate-note -->

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
<!-- FIXTURE:claude-code-fullexample-json -->
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
<!-- /FIXTURE:claude-code-fullexample-json -->
```

## Next Steps

- See `docs/mcp-sidecar.md` for detailed configuration and troubleshooting.
- Run `acr-mcp doctor` to verify your setup, or `acr-mcp diagnostics --output ./acr-diagnostics.tar` for <!-- FIXTURE:bundle-share-caution -->a bundle safe to share only through an approved private support channel (never a public issue tracker)<!-- /FIXTURE:bundle-share-caution --> (see [Diagnostic Bundles](README.md#diagnostic-bundles)).
- Official reference: <https://code.claude.com/docs/en/mcp>
