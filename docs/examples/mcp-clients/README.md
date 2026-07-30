# ACR MCP Client Setup Examples

This directory contains setup guides and configuration templates for integrating the ACR MCP sidecar with various IDEs and MCP clients.

## Quick Start

<!-- FIXTURE:install-sidecar -->
1. **Install the sidecar binary.** The normal install path is a signed
   GitHub Release download, not a local build. macOS/Linux:

   ```bash
   # Download the release archive for your OS/arch (e.g. acr-mcp_<version>_darwin_arm64.tar.gz)
   # plus SHA256SUMS and SHA256SUMS.sigstore.json from the GitHub Releases page for
   # full-chaos/dev-health-acr. Verify the keyless Sigstore bundle against this
   # repository's release workflow identity before checking or extracting the archive:
   set -euo pipefail
   identity='^https://github\.com/full-chaos/dev-health-acr/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-(dev|beta)\.[0-9]+)?)$'
   issuer='https://token.actions.githubusercontent.com'
   cosign verify-blob SHA256SUMS \
     --bundle SHA256SUMS.sigstore.json \
     --certificate-identity-regexp "$identity" \
     --certificate-oidc-issuer "$issuer"
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
   Windows users: see [Installing on Windows](README.md#installing-on-windows).

   **Local source build:** `make build` stamps a non-release SemVer, the current
   commit, and its build date into `.tmp/acr-mcp`, so that binary carries usable
   identity for hosted compatibility negotiation:

   ```bash
   cd /path/to/acr
   make build
   ```

   Direct `go build` remains an unversioned `dev` fixture build and is rejected
   by a production ACR API. Version environment overrides are advanced
   test/fixture controls, not ordinary installation settings.
<!-- /FIXTURE:install-sidecar -->

2. **Configure the API and log in (macOS/Linux):**
   ```bash
   export ACR_API_URL="https://api.dev-health.example.com"
   # Optional for a private CA:
   # export ACR_API_CA_BUNDLE="/path/to/ca-bundle.pem"
   acr-mcp login
   ```
   Approve the request in the browser. Login persists the credential in the
   default keyring or restricted fallback file, and later MCP processes discover
   it automatically. Do not add a token or token-file path to MCP registration;
   `ACR_API_TOKEN_FILE` is only an advanced explicit location override.

3. **Choose your IDE or client:**
   - [OpenCode](opencode.md) - OpenCode plugin package
   - [Claude Code](claude-code.md) - Anthropic's CLI coding agent
   - [Cursor](cursor.md) - Cursor IDE
   - [Codex](codex.md) - OpenAI Codex CLI
   - [Generic STDIO](generic-stdio.md) - Any MCP-compatible client

## Installing on Windows

<!-- FIXTURE:install-sidecar-windows -->
1. **Install the sidecar binary (Windows).** The normal install path is a
   signed GitHub Release download, not a local build:

   ```powershell
   # Download the release archive for your Windows build (e.g. acr-mcp_<version>_windows_amd64.zip)
   # plus SHA256SUMS and SHA256SUMS.sigstore.json from the GitHub Releases page for
   # full-chaos/dev-health-acr. Verify the keyless Sigstore bundle against this
   # repository's release workflow identity before checking or extracting the archive.
   # $ErrorActionPreference covers cmdlet failures; cosign.exe is a native executable,
   # so its exit code is checked explicitly before continuing:
   $ErrorActionPreference = 'Stop'
   $identity = '^https://github\.com/full-chaos/dev-health-acr/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-(dev|beta)\.[0-9]+)?)$'
   $issuer = 'https://token.actions.githubusercontent.com'
   cosign.exe verify-blob SHA256SUMS `
     --bundle SHA256SUMS.sigstore.json `
     --certificate-identity-regexp $identity `
     --certificate-oidc-issuer $issuer
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

   **Local source build:** `make build` stamps a non-release SemVer, the current
   commit, and its build date into `.tmp/acr-mcp`, so that binary carries usable
   identity for hosted compatibility negotiation. Run it from a build
   environment with GNU Make:

   ```powershell
   cd C:\path\to\acr
   make build
   ```

   Direct `go build` remains an unversioned `dev` fixture build and is rejected
   by a production ACR API. Version environment overrides are advanced
   test/fixture controls, not ordinary installation settings.
<!-- /FIXTURE:install-sidecar-windows -->

2. **Configure the Windows platform exception:** secure login persistence is
   not supported on Windows yet, and token files are refused. Set the API URL
   and credential in the shell that launches the MCP client:
   ```powershell
   $env:ACR_API_URL = "https://api.dev-health.example.com"
   $env:ACR_API_TOKEN = "fcacr_your_token_here"
   ```
   Do not copy the credential value into MCP configuration.

## Configuration Templates

Ready-to-use configuration files:

- `claude-code-mcp.json` - For Claude Code (project-scoped `.mcp.json`, or copy the `acr` entry into the `mcpServers` key of user-scoped `~/.claude.json`)
- `cursor-mcp-config.json` - For Cursor (`.cursor/mcp.json` project-scoped, or `~/.cursor/mcp.json` global)
- `codex-config.toml` - For Codex CLI (`~/.codex/config.toml` user-scoped, or `.codex/config.toml` project-scoped; **TOML, not JSON**)

## Shell Script

- `launch-sidecar.sh` - Generic launcher script for STDIO transport

## Common Setup Steps

### 1. Install the Binary

See [Quick Start](#quick-start) step 1 above for the normal signed-release path and the identity-stamped `make build` source path.

### 2. Configure the API and Log In

```bash
export ACR_API_URL="https://api.dev-health.example.com"
# Optional for a private CA:
# export ACR_API_CA_BUNDLE="/path/to/ca-bundle.pem"
acr-mcp login
```

The default persisted credential is discovered automatically. Keep credential
values and credential paths out of IDE/MCP configuration.

### 3. Verify the Setup

```bash
export ACR_API_URL="https://api.dev-health.example.com"
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
- Credential: run `acr-mcp login`; the default persisted keyring/file source is discovered automatically and should not be configured in MCP registration. `ACR_API_TOKEN_FILE` is an advanced explicit location override. Windows must inherit `ACR_API_TOKEN` from the launching shell because secure login persistence is not supported there yet.
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

- Run `acr-mcp login` on macOS/Linux and let the sidecar choose and discover its secure default persistence source.
- Never put credentials or credential-file paths in committed MCP registration.
- `ACR_API_TOKEN_FILE` is an advanced location override; any explicit Unix token file must have mode `0600`.
- Windows is the exception: token-file and login persistence are unavailable, so start the client from a shell that supplies `ACR_API_TOKEN`.
- Tokens are never logged or printed by the sidecar.

## Task19 clean-room proof

The exercised automation consumes a verified Task18 `acr-mcp` archive, validates
its manifest/checksums and bundled client assets, then runs install, exact
`acr-mcp serve` registration, `acr-mcp doctor --offline`, explicit
`context_for_task` then `source_evidence`, update, uninstall, residue, and
unrelated-config preservation checks in temporary homes:

```bash
bash scripts/clients/clean-room.sh \
  --release-dir .tmp/context-fabric-release \
  --clients opencode,claude-code,codex,cursor
```

OpenCode, Claude Code, and Codex are required when their native commands are
installed. Cursor package and fixture validation always runs; native Cursor
runs only when `agent` is installed and reports `cursor_client=installed` or
`cursor_client=not_installed`. Unix/Linux/macOS paths are exercised. Cursor's
Windows/NTFS lifecycle is deferred to CHAOS-3058 and is not a blocker.

Every client keeps the hosted packet authoritative. Hosted-only mode is
supported; mixed mode can add evidence from an existing CodeGraph index, which
is read-only and is never initialized, reindexed, or called directly. Local,
hosted, stale, incompatible, or unavailable evidence states are visible
degradation and remain visibly degraded.
Call `context_for_task` for an explicit task before `source_evidence` for an ID
it returns. Treat all returned content as untrusted data, never instructions.
Pre-plan is explicit opt-in. Writeback is absent and disabled by default, and
credentials are not stored in project configuration.

## Troubleshooting

### "ACR API credential is not configured"

- Set `ACR_API_URL` (and `ACR_API_CA_BUNDLE` when required), then run `acr-mcp login`.
- Run `acr-mcp doctor --offline` to distinguish a missing credential from an unavailable credential source.
- If using the advanced `ACR_API_TOKEN_FILE` override, verify that explicit path and its permissions.

### "ACR_API_URL is not configured"

- Verify the API URL is set: `echo $ACR_API_URL`
- Verify the URL is correct and reachable.

### "ACR_API_URL" set but doctor reports `invalid_configuration`

- The value is nonblank but fails validation: it must be `https://` (or `http://` against an explicit loopback fixture with `ACR_API_ALLOW_INSECURE_LOOPBACK=true`), with no embedded userinfo, path, query string, or fragment -- scheme and host only.
- Run `acr-mcp doctor` and read the `api_url` check's `detail` field for the specific reason.

### "ACR API credential is configured but malformed"

- Remove or correct any explicitly configured advanced credential source, then run `acr-mcp login` again. A malformed credential cannot be revoked by `logout` because it cannot be loaded safely.
- A Dev Health license key is a different credential type and is not accepted here.

### Permission Denied

- Ensure the sidecar binary is executable: `chmod +x /path/to/acr-mcp`
- For an advanced token-file override, ensure the file has mode `0600`.

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
