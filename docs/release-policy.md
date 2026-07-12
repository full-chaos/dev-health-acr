# Private release policy

The GitHub `Release` workflow is read-only. It validates the canonical,
annotated GPG-signed tag, exact pinned signer fingerprint, operator allowlist,
`main` ancestry, deterministic matrix build, vulnerability scan, SBOMs, and
native Linux/macOS/Windows smoke tests. It uploads only the private Actions
artifact named `release`; it never receives signing keys and cannot create,
edit, delete, or publish a GitHub Release.

`SHA256SUMS` is recomputed after SBOM generation and is the authoritative sorted
manifest for every archive, every `*.spdx.json`, and `release-manifest.json`.
`release-manifest.json` remains the builder identity/target manifest. Windows
ARM64 is deliberately deferred because the supported release matrix currently
contains Windows AMD64 only; adding it requires builder, native-runner, and
compatibility coverage together.

## Authorization

Before any build, the workflow imports the tracked
`signing/release-tag-signing-key.asc`, requires exact fingerprint
`9DCD0E7D385C8247E2F5E7FC2C43EBC02D8C8781`, and runs `git verify-tag`. It also
requires the triggering actor to be an exact comma-delimited member of the
administrator-controlled repository variable `ACR_RELEASE_OPERATORS` (initial
value: `chrisgeo`). These are defense-in-depth gates; protected `main` and
restricted tag creation remain required organization controls.

## Local key setup

On a secure operator machine, run `scripts/release/setup-cosign-key.sh`. It
creates an encrypted Cosign v3.1.1 key beneath `~/.config/acr/release`, stores
its password in macOS Keychain when available (otherwise the operator must enter
it securely for publishing), and writes only `signing/cosign.pub` for review and
commit. No Cosign private key or password is stored in GitHub.

## Local publish contract

After the Actions run succeeds, an authorized operator runs:

```bash
scripts/release/publish-private-release.sh vX.Y.Z RUN_ID
```

The script verifies the authenticated GitHub actor/repository allowlist, clones
the configured private origin into a temporary directory, verifies the signed
tag and remote `main` ancestry, requires the named Release workflow run to have
successfully built that exact commit, downloads its artifact, validates the
builder manifest and final checksums, signs `SHA256SUMS` locally with
`--use-signing-config=false --new-bundle-format=false --tlog-upload=false`, and
verifies it with the tracked key. It creates a draft Release, uploads assets,
downloads the draft into a fresh directory for independent signature/checksum
verification, re-fetches the tag again, then publishes with prerelease/latest
semantics. `signing/cosign.pub` is never a Release asset: obtain it only from a
reviewed repository commit before verifying `SHA256SUMS.sig`.

Consumers obtain `signing/cosign.pub` from a reviewed repository commit, verify
the signature over the full manifest, then verify exactly the archive they
downloaded. Never run a bare full-manifest checksum command when other listed
assets were not downloaded.

```bash
set -euo pipefail
git verify-tag v1.2.3
cosign verify-blob --key signing/cosign.pub --signature SHA256SUMS.sig \
  --insecure-ignore-tlog SHA256SUMS
archive='acr-api_1.2.3_linux_amd64.tar.gz'
checksum_line="$(awk -v name="$archive" '$2 == name' SHA256SUMS)"
test "$(printf '%s\n' "$checksum_line" | wc -l | tr -d ' ')" = 1
if command -v sha256sum >/dev/null; then
  printf '%s\n' "$checksum_line" | sha256sum --check -
else
  printf '%s\n' "$checksum_line" | shasum -a 256 --check -
fi
```

On Windows PowerShell, use the same reviewed key and full-manifest signature
verification, then target a single archive:

```powershell
$ErrorActionPreference = 'Stop'
git verify-tag v1.2.3
if ($LASTEXITCODE -ne 0) { throw 'tag signature verification failed' }
cosign verify-blob --key signing/cosign.pub --signature SHA256SUMS.sig --insecure-ignore-tlog SHA256SUMS
if ($LASTEXITCODE -ne 0) { throw 'checksum signature verification failed' }
$archive = 'acr-api_1.2.3_windows_amd64.zip'
$line = @(Get-Content SHA256SUMS | Where-Object { $_.EndsWith("  $archive") })
if ($line.Count -ne 1) { throw "expected one checksum for $archive" }
$expected = $line[0].Split(' ')[0]
if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected) { throw "checksum mismatch: $archive" }
```

## Revocation

Run `scripts/release/revoke-private-release.sh TAG INCIDENT_REFERENCE` locally
as an allowlisted operator and type the exact confirmation. The script lists
Release assets, deletes each asset in a separate fail-closed CLI call, then
preserves the immutable signed tag and a clearly marked revoked Release record
containing the incident/audit reference. It cannot erase already-downloaded
bytes. Credential revocation is the security control; a server version denylist
is a separate compatibility control.

## Rollback and non-production exercise

Rollback selects a previously verified immutable tag, re-runs the targeted
signature/checksum verification, and deploys that exact version through the
normal deployment control. Never move a tag, replace an asset, or rebuild an
old version. Record the incident reference, rollback tag, operator, UTC start
and completion times, verification output reference, and post-rollback health
result.

Exercise the procedure only with a new `-dev.N` tag in non-production: complete
the read-only Release workflow, perform the local draft publish/verification,
verify one archive using the targeted selector above, revoke it with an incident
reference, confirm its Release asset list is empty while the tag remains, and
record the same evidence fields. Private GitHub controls audit authorization and
access changes plus publish/revoke actions, and expose aggregate download counts.
They do not identify individual browser downloaders in this implementation.
