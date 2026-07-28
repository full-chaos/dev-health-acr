package mcpclientfixtures

import "strings"

// This file holds the canonical "install the sidecar binary" setup step
// every guide under docs/examples/mcp-clients/ embeds verbatim, one
// generator per platform. The normal install path is the GitHub Release
// workflow's keylessly signed SHA256SUMS plus its Sigstore bundle;
// `go build`/`go build -o acr-mcp.exe` remains explicitly development-only
// against a production API that rejects an unversioned "dev" sidecar. The
// ACR_SIDECAR_VERSION/ACR_SIDECAR_CLIENT_VERSION override is the one
// supported way to test a locally built binary against a hosted API's
// minimum sidecar version gate (see internal/mcp/compat.go's
// effectiveSidecarVersion and internal/version's EffectiveVersion). Both
// snippets use the "@BT@" placeholder in place of a literal Markdown
// backtick; see the raw constants below.

// installSidecarSnippetRaw is InstallSidecarSnippet's source text for
// macOS and Linux, mirroring docs/release-policy.md's "Signing and consumer
// verification" contract. The Sigstore bundle authenticates the entire
// SHA256SUMS manifest against the exact GitHub Actions workflow identity.
// SHA256SUMS lists every release artifact, including each archive's
// "<archive-name>.spdx.json" sibling, so a naive substring or prefix match
// on an archive filename would match both entries. The snippet therefore
// selects the checksum line with `awk -v name="$archive" '$2 == name'`,
// requires exactly one match, and verifies only that downloaded archive.
// It never checks the whole manifest as a local file list, which would fail
// for every other asset the consumer intentionally did not download. The
// checksum step probes for GNU sha256sum or macOS shasum so both platforms
// consume the same canonical "<hash>  <filename>" manifest format.
const installSidecarSnippetRaw = `1. **Install the sidecar binary.** The normal install path is a signed
   GitHub Release download, not a local build. macOS/Linux:

   ` + "```" + `bash
   # Download the release archive for your OS/arch (e.g. acr-mcp_<version>_darwin_arm64.tar.gz)
   # plus SHA256SUMS and SHA256SUMS.sigstore.json from the GitHub Releases page for
   # full-chaos/dev-health-acr. The Sigstore bundle binds SHA256SUMS to the release
   # workflow's GitHub Actions OIDC identity. set -euo pipefail ensures a failed
   # signature or checksum step cannot fall through to extraction:
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
   ` + "```" + `

   See @BT@docs/release-policy.md@BT@ for the full verification runbook.
   Windows users: see [Installing on Windows](README.md#installing-on-windows).

   **Development only:** @BT@go build@BT@ produces an unversioned @BT@dev@BT@ binary. A
   production ACR API rejects a @BT@dev@BT@-identified sidecar outright (426 Upgrade
   Required, before any tool call is accepted) -- only use this against a
   non-production/test fixture API, never a real hosted ACR API:

   ` + "```" + `bash
   cd /path/to/acr
   go build -o acr-mcp ./cmd/acr-mcp
   ` + "```" + `

   To test a locally built binary against a hosted API that enforces a
   minimum sidecar version, set an explicit valid version override instead
   of relying on the compiled-in @BT@dev@BT@ identity:

   ` + "```" + `bash
   export ACR_SIDECAR_VERSION="1.0.0"        # must satisfy the target API's minimum_sidecar_version
   export ACR_SIDECAR_CLIENT_VERSION="1.0.0"
   ` + "```" + `
`

// InstallSidecarSnippet is the single canonical macOS/Linux source every
// guide embeds verbatim, marked with "<!-- FIXTURE:install-sidecar -->" /
// "<!-- /FIXTURE:install-sidecar -->" HTML comments.
var InstallSidecarSnippet = strings.ReplaceAll(installSidecarSnippetRaw, "@BT@", "`")

// installSidecarWindowsSnippetRaw is the Windows equivalent. It verifies
// SHA256SUMS against the same GitHub Actions OIDC identity and Sigstore bundle,
// then filters for exactly one archive line with EndsWith before comparing
// Get-FileHash. `$ErrorActionPreference = 'Stop'` covers cmdlet failures;
// cosign.exe is native, so its exit code is checked explicitly before any
// checksum selection or extraction can proceed. PowerShell line-continuation
// backticks are split out of the Go raw string below.
const installSidecarWindowsSnippetRaw = `1. **Install the sidecar binary (Windows).** The normal install path is a
   signed GitHub Release download, not a local build:

   ` + "```" + `powershell
   # Download the release archive for your Windows build (e.g. acr-mcp_<version>_windows_amd64.zip)
   # plus SHA256SUMS and SHA256SUMS.sigstore.json from the GitHub Releases page for
   # full-chaos/dev-health-acr. The Sigstore bundle binds SHA256SUMS to the release
   # workflow's GitHub Actions OIDC identity. $ErrorActionPreference = 'Stop' covers
   # later failing cmdlets; cosign.exe is native, so $LASTEXITCODE is checked:
   $ErrorActionPreference = 'Stop'
   $identity = '^https://github\.com/full-chaos/dev-health-acr/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-(dev|beta)\.[0-9]+)?)$'
   $issuer = 'https://token.actions.githubusercontent.com'
   cosign.exe verify-blob SHA256SUMS ` + "`" + `
     --bundle SHA256SUMS.sigstore.json ` + "`" + `
     --certificate-identity-regexp $identity ` + "`" + `
     --certificate-oidc-issuer $issuer
   if ($LASTEXITCODE -ne 0) { throw "cosign verify-blob failed with exit code $LASTEXITCODE" }

   $archive = "acr-mcp_<version>_windows_amd64.zip"
   $line = @(Get-Content SHA256SUMS | Where-Object { $_.EndsWith("  $archive") })
   if ($line.Count -ne 1) { throw "expected exactly one checksum line for $archive" }
   $expectedHash = $line[0].Split(' ')[0]
   $actualHash = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant()
   if ($actualHash -ne $expectedHash) { throw "checksum mismatch for $archive" }

   Expand-Archive -Path $archive -DestinationPath .
   ` + "```" + `

   See @BT@docs/release-policy.md@BT@ for the full verification runbook.
   There is no @BT@chmod@BT@ equivalent on Windows: an extracted @BT@.exe@BT@ is directly
   runnable.

   **Development only:** @BT@go build@BT@ produces an unversioned @BT@dev@BT@ binary. A
   production ACR API rejects a @BT@dev@BT@-identified sidecar outright (426 Upgrade
   Required, before any tool call is accepted) -- only use this against a
   non-production/test fixture API, never a real hosted ACR API:

   ` + "```" + `powershell
   cd C:\path\to\acr
   go build -o acr-mcp.exe .\cmd\acr-mcp
   ` + "```" + `

   To test a locally built binary against a hosted API that enforces a
   minimum sidecar version, set an explicit valid version override instead
   of relying on the compiled-in @BT@dev@BT@ identity:

   ` + "```" + `powershell
   $env:ACR_SIDECAR_VERSION = "1.0.0"        # must satisfy the target API's minimum_sidecar_version
   $env:ACR_SIDECAR_CLIENT_VERSION = "1.0.0"
   ` + "```" + `
`

// InstallSidecarWindowsSnippet is the single canonical Windows source
// every guide embeds verbatim, marked with
// "<!-- FIXTURE:install-sidecar-windows -->" /
// "<!-- /FIXTURE:install-sidecar-windows -->" HTML comments.
var InstallSidecarWindowsSnippet = strings.ReplaceAll(installSidecarWindowsSnippetRaw, "@BT@", "`")
