# Release and publication policy

The `Release` workflow is the canonical build-and-publish path for
`dev-health-acr`. A successful run produces a GitHub Release, versioned GHCR
images, deterministic binary archives, OCI archives, SPDX SBOMs, manifests,
checksums, and Sigstore verification material.

## Trigger and authorization

The workflow supports two entry points:

1. Pushing a canonical tag:
   `vMAJOR.MINOR.PATCH`, `vMAJOR.MINOR.PATCH-dev.N`, or
   `vMAJOR.MINOR.PATCH-beta.N`.
2. Running **Actions → Release → Run workflow** from `main` and supplying an
   existing canonical tag. This is the recovery path for a tag whose earlier
   run failed before publication.

The tag must exist and resolve to a commit that is an ancestor of `main`. If
the repository variable `ACR_RELEASE_OPERATORS` is configured, the triggering
actor must be an exact comma-delimited member. When the variable is unset,
repository and tag permissions are the authorization boundary. Annotated tag
signatures are reported when locally verifiable, but a lightweight tag or an
annotated tag without a locally available signer key does not silently block
publication.

Do not move or reuse a release tag. Recovery always runs the current workflow
against the existing tag and original source commit.

## Permission boundary

The workflow remains read-only through source verification, binary builds,
container builds, vulnerability scans, SBOM generation, and native smoke tests.
Only the final `assemble` job receives:

```yaml
permissions:
  actions: read
  contents: write
  id-token: write
  packages: write
```

`contents: write` creates the GitHub Release, `packages: write` publishes the
two GHCR packages, and `id-token: write` enables keyless Sigstore signing. No
long-lived registry password, GitHub personal access token, GPG private key, or
Cosign private key is stored in Actions.

## Build and verification stages

The release must pass all of the following before publication:

1. Source, contract, race, cross-compilation, full-stack contract, dependency,
   and vulnerability checks.
2. Two deterministic builds of the complete binary matrix.
3. Native smoke tests on Linux, macOS, and Windows.
4. Multi-platform `linux/amd64` and `linux/arm64` OCI image builds for
   `acr-api` and `acr-mcp`.
5. Container archive validation, Trivy scanning, and SPDX SBOM generation.
6. Final checksum and manifest reconciliation across binary and container
   outputs.

The release matrix contains five archives per binary:

- Linux AMD64
- Linux ARM64
- macOS AMD64
- macOS ARM64
- Windows AMD64

Windows ARM64 remains deferred until builder, native-runner, and compatibility
coverage are added together.

## Published artifacts

The final GitHub Release contains:

- ten `acr-api` and `acr-mcp` binary archives;
- one multi-platform OCI archive for each image;
- binary and container SPDX JSON SBOMs;
- `release-manifest.json`;
- `container-release-manifest.json`;
- `SHA256SUMS`;
- `SHA256SUMS.sigstore.json`.

The workflow also retains the same assembled set as the Actions artifact named
`release` for seven days, including when the publication step fails after
assembly.

The container images are published without rebuilding:

```text
ghcr.io/full-chaos/dev-health-acr/acr-api:vX.Y.Z
ghcr.io/full-chaos/dev-health-acr/acr-mcp:vX.Y.Z
```

Pre-release tags retain their full `-dev.N` or `-beta.N` suffix. Deployment
automation should use the digest recorded in
`container-release-manifest.json`:

```text
ghcr.io/full-chaos/dev-health-acr/acr-api@sha256:<digest>
ghcr.io/full-chaos/dev-health-acr/acr-mcp@sha256:<digest>
```

The workflow does not publish a mutable GHCR `latest` tag. GitHub may mark the
newest stable GitHub Release as latest, but container deployment remains
digest-oriented.

## Idempotency and conflict handling

Publication is retry-safe:

- If a GHCR version tag does not exist, the verified OCI archive is copied with
  all platforms and preserved digests.
- If the tag already resolves to the expected digest, publication continues.
- If the tag resolves to different bytes, the workflow fails rather than
  replacing it.
- If the GitHub Release already exists, the workflow downloads it, verifies the
  Sigstore bundle and all checksums, and succeeds only when its asset manifest
  exactly matches the rebuilt release.
- An unrelated or incomplete draft Release is never overwritten automatically.

A failure after image upload but before GitHub Release publication can therefore
be retried with the same tag. A failure caused by an old workflow should be
recovered with `workflow_dispatch` from `main`, not by moving the tag.

## Signing and consumer verification

Cosign v3 signs both image digests and `SHA256SUMS` with the GitHub Actions OIDC
identity for this workflow. The accepted identity is restricted to the release
workflow on `main` or a canonical release tag:

```bash
identity='^https://github\.com/full-chaos/dev-health-acr/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-(dev|beta)\.[0-9]+)?)$'
issuer='https://token.actions.githubusercontent.com'

cosign verify \
  --certificate-identity-regexp "$identity" \
  --certificate-oidc-issuer "$issuer" \
  ghcr.io/full-chaos/dev-health-acr/acr-api@sha256:<digest>

cosign verify-blob SHA256SUMS \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp "$identity" \
  --certificate-oidc-issuer "$issuer"
```

After verifying the bundle over the complete manifest, verify only the archive
you intend to use:

```bash
set -euo pipefail
archive='acr-api_1.2.3_linux_amd64.tar.gz'
checksum_line="$(awk -v name="$archive" '$2 == name' SHA256SUMS)"
test "$(printf '%s\n' "$checksum_line" | wc -l | tr -d ' ')" = 1
if command -v sha256sum >/dev/null; then
  printf '%s\n' "$checksum_line" | sha256sum --check -
else
  printf '%s\n' "$checksum_line" | shasum -a 256 --check -
fi
```

On Windows PowerShell:

```powershell
$ErrorActionPreference = 'Stop'
$archive = 'acr-api_1.2.3_windows_amd64.zip'
$line = @(Get-Content SHA256SUMS | Where-Object { $_.EndsWith("  $archive") })
if ($line.Count -ne 1) { throw "expected one checksum for $archive" }
$expected = $line[0].Split(' ')[0]
if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected) {
  throw "checksum mismatch: $archive"
}
```

## Local operator fallback

The `publish-private-image.sh` and `publish-private-release.sh` scripts remain
available as an emergency operator path for previously assembled private
artifacts. They are not the normal release mechanism and retain their separate
local-key, package-privacy, and exact-tool-version requirements. Do not run both
the automated and local publishers concurrently for the same tag.

The optional MCP binary approval receipt remains available only for a future
policy that requires a separate owner approval. It is not part of the automated
release path.

## Rollback and revocation

Rollback selects a previously verified immutable digest, verifies its Cosign
signature and matching release manifest, and deploys that digest through the
normal deployment control. Never move a tag or rebuild an old version.

If a release must be revoked:

1. Remove or mark the GitHub Release according to the incident procedure.
2. Deny the affected image digests in deployment controls.
3. Delete exact GHCR package versions only with a separately authorized
   credential.
4. Record the incident reference, tag, image digests, operator, UTC timestamps,
   and post-revocation verification.

Revocation cannot retract bytes already downloaded. Credential and deployment
denylists remain the controlling security mechanisms.
