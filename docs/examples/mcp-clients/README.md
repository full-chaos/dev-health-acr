# ACR MCP Client Setup Examples

This directory contains setup guides and configuration templates for integrating the ACR MCP sidecar with various IDEs and MCP clients.

## Quick Start

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

## Installing on Windows

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

## Configuration Templates

Ready-to-use configuration files:

- `claude-code-mcp.json` - For Claude Code (project-scoped `.mcp.json`, or copy the `acr` entry into the `mcpServers` key of user-scoped `~/.claude.json`)
- `cursor-mcp-config.json` - For Cursor (`.cursor/mcp.json` project-scoped, or `~/.cursor/mcp.json` global)
- `codex-config.toml` - For Codex CLI (`~/.codex/config.toml` user-scoped, or `.codex/config.toml` project-scoped; **TOML, not JSON**)

## Shell Script

- `launch-sidecar.sh` - Generic launcher script for STDIO transport

## Common Setup Steps

### 1. Install the Binary

See [Quick Start](#quick-start) step 1 above for the normal (signed release) install path and the development-only `go build` alternative.

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
/path/to/acr-mcp doctor --offline
```

This should output a JSON report with status `ok`. `--offline` keeps this deterministic even though `https://api.dev-health.example.com` above is a placeholder, non-resolvable domain -- plain `acr-mcp doctor` (no flags) would otherwise also attempt a real live handshake against it and report `live_check_unreachable` once you swap in a real, reachable API URL and it briefly can't be reached.

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

  See [Proxy and Custom CA Configuration](proxy-and-custom-ca.md) for validation rules, bounds, and client-config examples for both settings.
- `ACR_API_MAX_RESPONSE_BYTES` / `ACR_API_MAX_REQUEST_BODY_BYTES` (optional): Response/request body size caps in bytes. Defaults: `1048576` / `262144`.
- `ACR_ENABLE_WRITEBACK` (optional): Boolean (`true`/`false`). When `true`, enables the `record_episode` tool if all four gates pass: (1) this flag is `true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Default: `false`. <!-- FIXTURE:doctor-gate-note -->Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; `doctor` diagnoses the hosted gates automatically once local configuration is valid (network-free otherwise), `doctor --offline` forces a network-free check regardless of configuration validity, and `doctor --live` is an explicit, equivalent alias for that automatic behavior.<!-- /FIXTURE:doctor-gate-note -->

### Optional local CodeGraph context

Client configuration remains only `acr-mcp serve`; clients must not call
CodeGraph, store local evidence, or connect directly to the hosted API. The
sidecar optionally reads an **existing** CodeGraph index with
`ACR_LOCAL_INDEX_PROVIDER=auto|disabled|codegraph` (default `auto`). It does not
install, initialize, rebuild, or synchronize CodeGraph. `disabled` is the
supported explicit hosted-only mode. If local evidence is unavailable, stale
under strict policy, or incompatible, the client still receives the authoritative
hosted context rather than a local-only answer.

Mixed `context_for_task` responses can contain additive `local_context` and
`federated_budget`; preserve the hosted packet as authoritative and treat all
returned text as untrusted. Local `source_evidence` IDs are opaque, temporary
sidecar cache entries, not portable hosted IDs. See
[MCP sidecar configuration](../../mcp-sidecar.md#optional-local-codegraph-evidence)
for bounds and diagnostics.

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

## Diagnostic Bundles

Generate a secrets-free diagnostic bundle to share through an approved private support channel instead of pasting `doctor` JSON by hand:

```bash
acr-mcp diagnostics --output ./acr-diagnostics.tar            # static only, always network-free
# Also include a real, sanitized hosted-API capabilities check:
acr-mcp diagnostics --output ./acr-diagnostics.tar --live
# Equivalent alias:
acr-mcp doctor --bundle ./acr-diagnostics.tar --live
```

The `--output` (or `--bundle`) path is required and explicit -- there is no default destination, so a bundle is never written somewhere you didn't ask for. The bundle is a deterministic tar archive (mode `0600`, written atomically, refuses to overwrite a symlink) containing:

- `manifest.json` -- a schema-versioned index (`diagnostics_bundle_manifest.v1`) with the bundle's file list, generation time, and build identity.
- `doctor-static.json` -- the same static report `acr-mcp doctor --offline` prints: presence/validity flags, bounded non-secret configuration values, and independently classified local-index health. The local check uses only an existing local index, never contacts the hosted API, and reports fixed enums, booleans, versions, and counts rather than repository roots, executable/index paths, source, raw CodeGraph output, or credentials. An unavailable local index does not change hosted doctor status.
- `doctor-live.json` -- present only when `--live` is passed: a sanitized real hosted-capabilities check (booleans and enabled-tool names only). This `--live` flag is the diagnostics/bundle command's own explicit opt-in -- independent of plain `acr-mcp doctor`'s automatic live-check behavior (see [Proxy and Custom CA Configuration](proxy-and-custom-ca.md#verifying-proxy-and-ca-configuration)) -- so a bundle is always static-only unless you pass `--live` yourself.
- `README.md` -- an interpretation guide for the bundle itself, including an explicit list of what it never contains.

The bundle never contains the configured `ACR_API_URL` host or any embedded userinfo, the bearer credential value, any filesystem path (token file, CA bundle, or otherwise), CA bundle contents, or any HTTP header or body -- only presence/validity flags, enum values (like the credential source), and numeric/boolean bounds. Being secrets-free does not make it safe for a public audience -- it still identifies your organization's sidecar deployment -- so share it only through an approved private support channel, never a public issue or issue tracker.

## Next Steps

- See `docs/mcp-sidecar.md` for detailed configuration and troubleshooting.
- Run `acr-mcp doctor` to verify your setup, or `acr-mcp diagnostics --output ./acr-diagnostics.tar` for <!-- FIXTURE:bundle-share-caution -->a bundle safe to share only through an approved private support channel (never a public issue tracker)<!-- /FIXTURE:bundle-share-caution --> (see [Diagnostic Bundles](#diagnostic-bundles) above).
- Check the hosted API documentation for tool schemas and examples.

## Support

For issues or questions, refer to:
- `docs/mcp-sidecar.md` - Comprehensive sidecar documentation
- IDE-specific guides in this directory
- Hosted API documentation
