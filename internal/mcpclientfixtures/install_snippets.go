package mcpclientfixtures

import "strings"

// This file holds the canonical "install the sidecar binary" setup step
// every guide under docs/examples/mcp-clients/ embeds verbatim, one
// generator per platform: a signed private release download is the
// normal install path on both, `go build`/`go build -o acr-mcp.exe` is
// explicitly labeled development-only against a production API that
// rejects an unversioned "dev" sidecar, and the ACR_SIDECAR_VERSION/
// ACR_SIDECAR_CLIENT_VERSION override is the one genuinely supported way
// to test a locally built binary against a hosted API's minimum sidecar
// version gate (see internal/mcp/compat.go's effectiveSidecarVersion and
// internal/version's EffectiveVersion). Both use the "@BT@" placeholder
// in place of a literal backtick -- see installSidecarSnippetRaw's own
// comment for why.

// installSidecarSnippetRaw is InstallSidecarSnippet's source text for
// macOS and Linux, mirroring docs/release-policy.md's "Local publish
// contract" consumer verification exactly: SHA256SUMS lists every
// artifact in the release -- all ten platform archives *and* one SBOM
// ("<archive-name>.spdx.json") per archive -- so a naive substring or
// prefix match on the archive's filename (even a fixed-string `grep -F`)
// also matches that archive's own SBOM sibling line, since the SBOM's
// filename literally starts with the archive's filename. The snippet
// therefore selects the checksum line with `awk -v name="$archive"
// '$2 == name'`: awk's default whitespace field-splitting makes column 2
// exactly the filename token, so this is an exact-field-equality match --
// never a substring/prefix match -- and cannot also select
// "$archive.spdx.json". A consumer downloads only the one archive for
// their own OS/arch, so the snippet verifies the signature over the
// *entire* SHA256SUMS file first (integrity of the manifest itself),
// then selects and verifies only that single exact-match line, asserting
// exactly one such line exists before trusting it. It never `--check`s
// the whole file directly, which would report every other, un-downloaded
// artifact as "FAILED open or read" and exit non-zero even though the
// one archive that matters verified correctly. The checksum step
// deliberately does not assume GNU coreutils: stock macOS ships `shasum`
// (a Perl script), not `sha256sum`, while most Linux distributions ship
// the reverse, so the snippet probes for whichever is present rather
// than hard-coding one -- both accept the same "<hash>  <filename>"
// SHA256SUMS format the release builder writes
// (internal/releasebuild/build.go's writeMetadata) piped in on stdin via
// the explicit "-" argument.
const installSidecarSnippetRaw = `1. **Install the sidecar binary.** The normal install path is a signed
   private release download, not a local build. macOS/Linux:

   ` + "```" + `bash
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
   ` + "```" + `

   See @BT@docs/release-policy.md@BT@ for the full verification runbook.
   Windows users: see [Installing on Windows](#installing-on-windows) below.

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

// installSidecarWindowsSnippetRaw is InstallSidecarWindowsSnippet's
// source text, mirroring docs/release-policy.md's PowerShell consumer
// verification exactly: as with the POSIX snippet, a consumer downloads
// only one archive out of everything SHA256SUMS lists, so this verifies
// the signature over the full SHA256SUMS file first, then locates and
// checks only the single relevant line -- PowerShell has no direct
// equivalent of `sha256sum -c`'s checksum-file batch-verify mode, so the
// snippet filters for the one line ending in the archive's exact
// filename (`EndsWith`, not a substring match, so one filename can never
// accidentally match another that merely contains it), asserts exactly
// one such line exists, then compares it against Get-FileHash directly.
// `$ErrorActionPreference = 'Stop'` converts a failing PowerShell cmdlet
// (e.g. Expand-Archive) into a script-halting error, but it has NO effect
// on a native executable's exit code: git.exe and cosign.exe are native,
// not cmdlets, so `$LASTEXITCODE` is checked explicitly right after each
// -- a failed signature verification must never fall through to
// checksum selection or extraction. cosign.exe takes the identical flags
// as the POSIX cosign binary, and PowerShell's backtick is its own line-
// continuation character, not Markdown's, so a literal PowerShell
// backtick continuation is written directly rather than through the
// "@BT@" placeholder machinery below.
const installSidecarWindowsSnippetRaw = `1. **Install the sidecar binary (Windows).** The normal install path is a
   signed private release download, not a local build:

   ` + "```" + `powershell
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
   cosign.exe verify-blob --key signing/cosign.pub --signature SHA256SUMS.sig ` + "`" + `
     --insecure-ignore-tlog SHA256SUMS
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
