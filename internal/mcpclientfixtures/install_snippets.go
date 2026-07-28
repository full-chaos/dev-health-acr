package mcpclientfixtures

import "strings"

// This file holds the canonical "install the sidecar binary" setup step
// every guide under docs/examples/mcp-clients/ embeds verbatim, one generator
// per platform. The normal path is a signed GitHub Release download; local
// go build output is explicitly development-only against production ACR.
//
// Both snippets verify the keyless Sigstore bundle over the complete
// SHA256SUMS manifest against the exact GitHub Actions release-workflow
// identity before selecting the one archive the consumer downloaded. They then
// verify only that archive's exact manifest line. This avoids batch-checking
// every platform archive and SBOM that the consumer did not download.
//
// The source strings use @BT@ in place of literal backticks because Go raw
// string literals cannot contain a backtick. strings.ReplaceAll restores both
// Markdown fences/inline code and PowerShell line-continuation characters.

const installSidecarSnippetRaw = `1. **Install the sidecar binary.** The normal install path is a signed
   GitHub Release download, not a local build. macOS/Linux:

   @BT@@BT@@BT@bash
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
   @BT@@BT@@BT@

   See @BT@docs/release-policy.md@BT@ for the full verification runbook.
   Windows users: see [Installing on Windows](README.md#installing-on-windows).

   **Development only:** @BT@go build@BT@ produces an unversioned @BT@dev@BT@ binary. A
   production ACR API rejects a @BT@dev@BT@-identified sidecar outright (426 Upgrade
   Required, before any tool call is accepted) -- only use this against a
   non-production/test fixture API, never a real hosted ACR API:

   @BT@@BT@@BT@bash
   cd /path/to/acr
   go build -o acr-mcp ./cmd/acr-mcp
   @BT@@BT@@BT@

   To test a locally built binary against a hosted API that enforces a
   minimum sidecar version, set an explicit valid version override instead
   of relying on the compiled-in @BT@dev@BT@ identity:

   @BT@@BT@@BT@bash
   export ACR_SIDECAR_VERSION="1.0.0"        # must satisfy the target API's minimum_sidecar_version
   export ACR_SIDECAR_CLIENT_VERSION="1.0.0"
   @BT@@BT@@BT@
`

// InstallSidecarSnippet is the single canonical macOS/Linux source every
// guide embeds verbatim, marked with "<!-- FIXTURE:install-sidecar -->" /
// "<!-- /FIXTURE:install-sidecar -->" HTML comments.
var InstallSidecarSnippet = strings.ReplaceAll(installSidecarSnippetRaw, "@BT@", "`")

const installSidecarWindowsSnippetRaw = `1. **Install the sidecar binary (Windows).** The normal install path is a
   signed GitHub Release download, not a local build:

   @BT@@BT@@BT@powershell
   # Download the release archive for your Windows build (e.g. acr-mcp_<version>_windows_amd64.zip)
   # plus SHA256SUMS and SHA256SUMS.sigstore.json from the GitHub Releases page for
   # full-chaos/dev-health-acr. Verify the keyless Sigstore bundle against this
   # repository's release workflow identity before checking or extracting the archive.
   # $ErrorActionPreference covers cmdlet failures; cosign.exe is a native executable,
   # so its exit code is checked explicitly before continuing:
   $ErrorActionPreference = 'Stop'
   $identity = '^https://github\.com/full-chaos/dev-health-acr/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-(dev|beta)\.[0-9]+)?)$'
   $issuer = 'https://token.actions.githubusercontent.com'
   cosign.exe verify-blob SHA256SUMS @BT@
     --bundle SHA256SUMS.sigstore.json @BT@
     --certificate-identity-regexp $identity @BT@
     --certificate-oidc-issuer $issuer
   if ($LASTEXITCODE -ne 0) { throw "cosign verify-blob failed with exit code $LASTEXITCODE" }

   $archive = "acr-mcp_<version>_windows_amd64.zip"
   $line = @(Get-Content SHA256SUMS | Where-Object { $_.EndsWith("  $archive") })
   if ($line.Count -ne 1) { throw "expected exactly one checksum line for $archive" }
   $expectedHash = $line[0].Split(' ')[0]
   $actualHash = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant()
   if ($actualHash -ne $expectedHash) { throw "checksum mismatch for $archive" }

   Expand-Archive -Path $archive -DestinationPath .
   @BT@@BT@@BT@

   See @BT@docs/release-policy.md@BT@ for the full verification runbook.
   There is no @BT@chmod@BT@ equivalent on Windows: an extracted @BT@.exe@BT@ is directly
   runnable.

   **Development only:** @BT@go build@BT@ produces an unversioned @BT@dev@BT@ binary. A
   production ACR API rejects a @BT@dev@BT@-identified sidecar outright (426 Upgrade
   Required, before any tool call is accepted) -- only use this against a
   non-production/test fixture API, never a real hosted ACR API:

   @BT@@BT@@BT@powershell
   cd C:\path\to\acr
   go build -o acr-mcp.exe .\cmd\acr-mcp
   @BT@@BT@@BT@

   To test a locally built binary against a hosted API that enforces a
   minimum sidecar version, set an explicit valid version override instead
   of relying on the compiled-in @BT@dev@BT@ identity:

   @BT@@BT@@BT@powershell
   $env:ACR_SIDECAR_VERSION = "1.0.0"        # must satisfy the target API's minimum_sidecar_version
   $env:ACR_SIDECAR_CLIENT_VERSION = "1.0.0"
   @BT@@BT@@BT@
`

// InstallSidecarWindowsSnippet is the single canonical Windows source every
// guide embeds verbatim, marked with
// "<!-- FIXTURE:install-sidecar-windows -->" /
// "<!-- /FIXTURE:install-sidecar-windows -->" HTML comments.
var InstallSidecarWindowsSnippet = strings.ReplaceAll(installSidecarWindowsSnippetRaw, "@BT@", "`")
