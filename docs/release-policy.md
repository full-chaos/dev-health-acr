# Private release policy

The GitHub `Release` workflow is read-only. It validates the canonical,
annotated GPG-signed tag, exact pinned signer fingerprint, operator allowlist,
`main` ancestry, deterministic binary matrix, vulnerability scans, SBOMs,
native Linux/macOS/Windows smoke tests, and the exact Linux AMD64/ARM64 OCI
archives for `acr-api` and `acr-mcp`. Only after every gate passes does it
assemble the private Actions artifact named `release`. The workflow never
receives signing or registry keys and cannot create, edit, delete, or publish a
GitHub Release or GHCR package.

`SHA256SUMS` is recomputed after binary and container assembly and is the
authoritative sorted manifest for every binary archive, OCI archive,
`*.spdx.json`, `release-manifest.json`, and
`container-release-manifest.json`.
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

Release publication requires Cosign v3.1.1 and Skopeo v1.23.0 on the trusted
operator machine. The authenticated `gh` token must have private repository and
`write:packages` access. Add that scope with
`gh auth refresh -h github.com -s write:packages`; deletion uses a separate credential with
`delete:packages`. The organization package policy must default new GHCR
packages to private. Both `dev-health-acr/acr-api` and
`dev-health-acr/acr-mcp` must be pre-provisioned, linked to this repository,
and private; the publisher refuses a missing or non-private package before
uploading bytes.

After the Actions run succeeds, download the complete artifact and read the
immutable image and checksum digests from its manifests. Publish both images
before publishing the GitHub Release:

```bash
gh run download RUN_ID --repo full-chaos/dev-health-acr \
  --name release --dir .tmp/release-vX.Y.Z

scripts/release/publish-private-image.sh \
  --digest sha256:API_INDEX_DIGEST \
  acr-api vX.Y.Z RUN_ID
scripts/release/publish-private-image.sh \
  --digest sha256:MCP_INDEX_DIGEST \
  acr-mcp vX.Y.Z RUN_ID
scripts/release/publish-private-release.sh \
  --digest sha256:SHA256SUMS_DIGEST \
  vX.Y.Z RUN_ID
```

These standard publication commands use the authenticated GitHub identity,
repository operator allowlist, signed release tag, successful workflow run, and
explicit immutable digest. They do not accept or require approval receipts.

The image publisher verifies the exact successful workflow run and complete
release artifact, then copies the approved multi-platform OCI archive to its
untagged GHCR digest with `skopeo copy --all --preserve-digests`. It requires the
remote index digest and Linux AMD64/ARM64 platform set to match the manifest,
requires the GHCR package to remain private and anonymously unreadable, signs
and verifies the digest with the local Cosign key, and does not create a GHCR
tag. The signed container release manifest is the release-version-to-digest
mapping, eliminating mutable-tag replacement and publication races. The
publisher never rebuilds an image. Deployments consume only `@sha256:`
references.

The GitHub Release publisher verifies the authenticated GitHub
actor/repository allowlist, clones
the configured private origin into a temporary directory, verifies the signed
tag and remote `main` ancestry, requires the named Release workflow run to have
successfully built that exact commit, downloads its artifact, validates the
builder manifest and final checksums, signs `SHA256SUMS` locally with
`--use-signing-config=false --new-bundle-format=false --tlog-upload=false`, and
verifies it with the tracked key. It creates a draft Release, uploads assets,
downloads the draft into a fresh directory for independent signature/checksum
verification, verifies both private GHCR digests and Cosign signatures, re-fetches
the tag again, then publishes with prerelease/latest semantics. The GitHub
Release includes both OCI archives as offline/private installation assets in
addition to the API and MCP binaries. `signing/cosign.pub` is never a Release
asset: obtain it only from a reviewed repository commit before verifying
`SHA256SUMS.sig`.

Consumers obtain `signing/cosign.pub` from a reviewed repository commit, verify
the signature over the full manifest, then verify exactly the archive they
downloaded. Never run a bare full-manifest checksum command when other listed
assets were not downloaded.

## Optional MCP binary approval

The signed receipt implementation remains available only for a future policy
that may require separate owner approval of the MCP binary set. It is not part
of the normal release path and no publisher calls it. To validate or consume an
owner-signed, expiring, single-use receipt for an exact MCP artifact-set digest:

```bash
scripts/release/verify-mcp-binary-approval.sh \
  --approval-receipt MCP_APPROVAL.json \
  --digest sha256:MCP_BINARY_SET_DIGEST \
  release-manifest.json vX.Y.Z
```

`MCP_BINARY_SET_DIGEST` is the SHA-256 of the canonical sorted JSON projection
of the five `acr-mcp` entries in `release-manifest.json`; the verifier computes
that value itself and rejects a receipt for any other bytes. Add `--dry-run` to
verify the receipt without consuming its nonce. Nonce single-use is enforced by
one controlled local ledger; copying a receipt to a fresh ledger is outside that
guarantee. The
`ACR_APPROVAL_VERIFICATION_KEY`, `ACR_APPROVAL_FINGERPRINT`, and
`ACR_APPROVAL_LEDGER` overrides are test-only trust-boundary controls and must
not be attacker-controlled if this optional gate is activated.

Automated authenticated consumer verification and exact GHCR package-version
revocation remain tracked in CHAOS-3067; neither blocks normal publication.

The signed `acr-mcp` archives also contain the four client packages and their
conformance identity. The Task19 clean-room check consumes an archive only
after this signature and targeted checksum verification, then validates the
bundled client bytes. This is current private release policy; it does not claim
that a production release has been created or published.

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
bytes. Compromised GHCR image digests must also be denied in deployment controls
and deleted as exact package versions by an independently approved operator
holding the separate `delete:packages` credential. Deletion cannot retract
already-pulled bytes. Credential revocation is the security control; a server
version denylist is a separate compatibility control. Automating this separately
approved GHCR action is tracked in CHAOS-3067; the current revocation script
removes GitHub Release assets only and does not claim to revoke GHCR bytes.

## Rollback and non-production exercise

Rollback selects a previously verified immutable tag, re-runs the targeted
signature/checksum verification, verifies the matching GHCR Cosign signature,
and deploys the exact `@sha256:` digest through the normal deployment control.
Never move a tag, replace an asset, or rebuild an old version. Record the
incident reference, rollback tag and digest, operator, UTC start and completion
times, verification output reference, and post-rollback health result.

Exercise the procedure only with a new `-dev.N` tag in non-production: complete
the read-only Release workflow, perform the local draft publish/verification,
verify one archive using the targeted selector above, revoke it with an incident
reference, confirm its Release asset list is empty while the tag remains, and
record the same evidence fields. Private GitHub controls audit authorization and
access changes plus publish/revoke actions, and expose aggregate download counts.
They do not identify individual browser downloaders in this implementation.
