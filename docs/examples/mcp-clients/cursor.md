# Cursor MCP Setup

[Cursor](https://cursor.com/docs/mcp) integrates MCP servers via a `mcp.json` file: `.cursor/mcp.json` in your project root, or `~/.cursor/mcp.json` for a global (all-projects) server. Cursor has no CLI for adding servers -- configuration is a manual JSON edit, managed afterwards from **Settings -> Tools & MCP**.

## Configuration File

Create or edit `.cursor/mcp.json` in your project root, or `~/.cursor/mcp.json` globally (`%USERPROFILE%\.cursor\mcp.json` on Windows):

```json
<!-- FIXTURE:cursor-project-json -->
{
  "mcpServers": {
    "acr": {
      "type": "stdio",
      "command": "/path/to/acr-mcp",
      "args": ["serve"],
      "env": {
        "ACR_API_URL": "https://api.dev-health.example.com",
        "ACR_API_TOKEN_FILE": "${env:HOME}/.acr/token",
        "ACR_API_TIMEOUT": "30s"
      }
    }
  }
}
<!-- /FIXTURE:cursor-project-json -->
```

`"type": "stdio"` is a required field for local command-based servers per Cursor's documented STDIO server configuration schema (<https://cursor.com/docs/mcp#stdio-server-configuration>) -- omitting it can cause the server to be misidentified as a different transport and fail to register.

A ready-to-copy template is at `cursor-mcp-config.json` in this directory.

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

See [Proxy and Custom CA Configuration](proxy-and-custom-ca.md) for validation rules and bounds for both settings.
- `ACR_ENABLE_WRITEBACK` (optional): Boolean (`true`/`false`). When `true`, enables the `record_episode` tool if all four gates pass: (1) this flag is `true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Default: `false`. <!-- FIXTURE:doctor-gate-note -->Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; `doctor` diagnoses the hosted gates automatically once local configuration is valid (network-free otherwise), `doctor --offline` forces a network-free check regardless of configuration validity, and `doctor --live` is an explicit, equivalent alias for that automatic behavior.<!-- /FIXTURE:doctor-gate-note -->

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
<!-- FIXTURE:cursor-fullexample-json -->
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
<!-- /FIXTURE:cursor-fullexample-json -->
```

## Next Steps

- See `docs/mcp-sidecar.md` for detailed configuration and troubleshooting.
- Run `acr-mcp doctor` to verify your setup, or `acr-mcp diagnostics --output ./acr-diagnostics.tar` for <!-- FIXTURE:bundle-share-caution -->a bundle safe to share only through an approved private support channel (never a public issue tracker)<!-- /FIXTURE:bundle-share-caution --> (see [Diagnostic Bundles](README.md#diagnostic-bundles)).
- Official reference: <https://cursor.com/docs/mcp>
