package mcpclientfixtures

import "strings"

// This file holds the canonical "install the sidecar binary" setup step
// every guide under docs/examples/mcp-clients/ embeds verbatim, one generator
// per platform. The normal path is a signed GitHub Release download; local
// source builds use the supported Make target so they carry a usable local
// identity; direct go build output remains an unversioned fixture build.
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

   **Local source build:** @BT@make build@BT@ stamps a non-release SemVer, the current
   commit, and its build date into @BT@.tmp/acr-mcp@BT@, so that binary carries usable
   identity for hosted compatibility negotiation:

   @BT@@BT@@BT@bash
   cd /path/to/acr
   make build
   @BT@@BT@@BT@

   Direct @BT@go build@BT@ remains an unversioned @BT@dev@BT@ fixture build and is rejected
   by a production ACR API. Version environment overrides are advanced
   test/fixture controls, not ordinary installation settings.
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

   **Local source build:** @BT@make build@BT@ stamps a non-release SemVer, the current
   commit, and its build date into @BT@.tmp/acr-mcp@BT@, so that binary carries usable
   identity for hosted compatibility negotiation. Run it from a build
   environment with GNU Make:

   @BT@@BT@@BT@powershell
   cd C:\path\to\acr
   make build
   @BT@@BT@@BT@

   Direct @BT@go build@BT@ remains an unversioned @BT@dev@BT@ fixture build and is rejected
   by a production ACR API. Version environment overrides are advanced
   test/fixture controls, not ordinary installation settings.
`

// InstallSidecarWindowsSnippet is the single canonical Windows source every
// guide embeds verbatim, marked with
// "<!-- FIXTURE:install-sidecar-windows -->" /
// "<!-- /FIXTURE:install-sidecar-windows -->" HTML comments.
var InstallSidecarWindowsSnippet = strings.ReplaceAll(installSidecarWindowsSnippetRaw, "@BT@", "`")
