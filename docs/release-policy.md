# Release and publication policy

The `Release` workflow is the canonical build-and-publish path for
`dev-health-acr`. It serves two publication channels from the same verified
build pipeline:

- every successful push to `main` publishes an immutable full-SHA image and a
  `main-<full-sha>` GitHub Release, then promotes that exact build to the moving
  `latest` aliases when the commit is still the current tip of `main`;
- a canonical version tag publishes an immutable versioned release without
  moving `latest`.

Both channels produce deterministic binary archives, multi-platform OCI
archives, SPDX SBOMs, manifests, checksums, and Sigstore verification material.

## Trigger and authorization

The workflow supports three entry points:

1. Pushing to `main`. The protected branch and merge permissions are the
   authorization boundary. The workflow publishes the full 40-character commit
   SHA as the immutable GHCR image tag, creates a `main-<full-sha>` GitHub
   Release, and, after rechecking the branch tip, moves the `latest` aliases.
2. Pushing a canonical tag:
   `vMAJOR.MINOR.PATCH`, `vMAJOR.MINOR.PATCH-dev.N`, or
   `vMAJOR.MINOR.PATCH-beta.N`.
3. Running **Actions → Release → Run workflow** from `main` and supplying an
   existing canonical tag. This is the recovery path for a tag whose earlier
   run failed before publication.

A version tag must exist and resolve to a commit that is an ancestor of `main`.
If the repository variable `ACR_RELEASE_OPERATORS` is configured, the actor for
a version-tag or manual recovery run must be an exact comma-delimited member.
When the variable is unset, repository and tag permissions are the authorization
boundary. Annotated tag signatures are reported when locally verifiable, but a
lightweight tag or an annotated tag without a locally available signer key does
not silently block publication.

Do not move or reuse an immutable version tag, `main-<full-sha>` GitHub
Release tag, or full-SHA GHCR image tag. Versioned recovery always runs the
current workflow against the existing tag and original source commit.

### Re-releasing an older tag (CHAOS-4889)

`release.yml`'s `mirror-preflight` job (see "The ghcr.io/full-chaos/dev-
health-acr mirror" in [`container-images.md`](container-images.md)) validates
the Docker Hub image inventory of the exact commit entry point 3 above
resolves to -- not current `main`. `mirror-images.yml` only ever mirrors the
pins of whatever ref it last ran against, which for its `schedule` and `push`
triggers is always `main`. An older tag's Dockerfile or testcontainers pins
can differ from `main`'s (a different `golang` base digest, for example), so
a `workflow_dispatch` release against a tag whose pins were never mirrored
fails `mirror-preflight` with those images reported `MISSING`, even though
the same re-release would have worked, unmirrored, before CHAOS-4855.

`mirror-preflight`'s failure message names the exact fix: it prints a
`gh workflow run mirror-images.yml -f ref=<commit>` command for the resolved
release commit. The recovery is two dispatches, in order:

1. Mirror the old ref's pins: **Actions → Mirror images → Run workflow**,
   setting `ref` to the tag or commit being re-released (or the equivalent
   `gh workflow run mirror-images.yml -f ref=<tag-or-sha>`), and wait for it
   to complete.
2. Re-run **Actions → Release → Run workflow** with the same tag (entry
   point 3 above).

`mirror-images.yml`'s `ref` input defaults to `main`, so an ordinary manual
mirror refresh (no old-tag re-release involved) needs no input at all.

## Main publication identity

A `main` build uses the full lowercase commit SHA as its immutable source
identity and GHCR image tag. GitHub does not allow a branch or tag name that is
exactly 40 or 64 hexadecimal characters, so the corresponding GitHub Release
uses the valid immutable tag `main-<full-sha>`. The binary's embedded version
remains canonical SemVer: the workflow finds the highest canonical release core
reachable from that commit, increments its patch component, and emits:

```text
MAJOR.MINOR.NEXT_PATCH-main.<40-character-commit-SHA>
```

If no canonical release tag exists in the commit's ancestry, the base is
`1.0.0`, producing `1.0.1-main.<SHA>`. The derived version is used in archive
filenames and manifests; the GitHub Release tag is `main-<full-sha>`, while the
immutable GHCR image tag remains the bare full commit SHA.

Before changing either moving `latest` alias, the final publication job reads
the current `refs/heads/main` value from GitHub. A superseded run still publishes
and verifies its immutable SHA release, but it cannot move `latest`. This closes
the race where two main builds finish out of order.

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

`contents: write` creates and updates GitHub Releases, `packages: write`
publishes the two GHCR packages, and `id-token: write` enables keyless Sigstore
signing. No long-lived registry password, GitHub personal access token, GPG
private key, or Cosign private key is stored in Actions.

## Build and verification stages

Every publication must pass all of the following before the final publish step:

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

## Published artifacts and references

Every GitHub Release contains:

- ten `acr-api` and `acr-mcp` binary archives;
- one multi-platform OCI archive for each image;
- binary and container SPDX JSON SBOMs;
- `release-manifest.json`;
- `container-release-manifest.json`;
- `SHA256SUMS`;
- `SHA256SUMS.sigstore.json`.

The workflow also retains the same assembled set as the Actions artifact named
`release` for seven days, including when publication fails after assembly.

For the current tip of `main`, the exact verified OCI archives are published
without rebuilding to both the immutable SHA and mutable convenience alias:

```text
ghcr.io/full-chaos/dev-health-acr/acr-api:<40-character-commit-SHA>
ghcr.io/full-chaos/dev-health-acr/acr-api:latest
ghcr.io/full-chaos/dev-health-acr/acr-mcp:<40-character-commit-SHA>
ghcr.io/full-chaos/dev-health-acr/acr-mcp:latest
```

The GitHub Release tagged `main-<full-sha>` and targeted at that exact commit
is marked as the repository's **Latest** release. GitHub's Latest marker is a
moving pointer; the prefixed Release tag and its assets remain immutable and
directly addressable.

Canonical version tags publish these immutable references and do not replace the
main channel's Latest marker:

```text
ghcr.io/full-chaos/dev-health-acr/acr-api:vX.Y.Z
ghcr.io/full-chaos/dev-health-acr/acr-mcp:vX.Y.Z
```

Pre-release tags retain their full `-dev.N` or `-beta.N` suffix. Deployment
automation should use the digest recorded in `container-release-manifest.json`:

```text
ghcr.io/full-chaos/dev-health-acr/acr-api@sha256:<digest>
ghcr.io/full-chaos/dev-health-acr/acr-mcp@sha256:<digest>
```

`latest` is intentionally mutable and is suitable for following `main` in
development environments. Production and rollback controls remain
digest-oriented.

## Idempotency and conflict handling

Publication is retry-safe:

- If an immutable version or commit-SHA GHCR tag does not exist, the verified
  OCI archive is copied with all platforms and preserved digests.
- If an immutable tag already resolves to the expected digest, publication
  continues.
- If an immutable tag resolves to different bytes, the workflow fails rather
  than replacing it.
- `latest` is updated only from a `main` run whose commit still equals the
  current branch tip; its resulting digest is verified after the copy.
- If the GitHub Release already exists, the workflow downloads it, verifies the
  Sigstore bundle and all checksums, and succeeds only when its asset manifest
  exactly matches the rebuilt release.
- An unrelated or incomplete draft Release is never overwritten automatically.
- Stable, beta, and development version releases are explicitly marked
  `latest=false`; the main channel owns the Latest marker.

A failure after image upload but before GitHub Release publication can be
retried with the same immutable tag. A versioned failure caused by an old
workflow should be recovered with `workflow_dispatch` from `main`, not by moving
the tag.

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
$identity = '^https://github\.com/full-chaos/dev-health-acr/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-(dev|beta)\.[0-9]+)?)$'
$issuer = 'https://token.actions.githubusercontent.com'
cosign.exe verify-blob SHA256SUMS `
  --bundle SHA256SUMS.sigstore.json `
  --certificate-identity-regexp $identity `
  --certificate-oidc-issuer $issuer
if ($LASTEXITCODE -ne 0) { throw "cosign verify-blob failed with exit code $LASTEXITCODE" }

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
the automated and local publishers concurrently for the same immutable tag.

The optional MCP binary approval receipt remains available only for a future
policy that requires a separate owner approval. It is not part of the automated
release path.

## Rollback and revocation

Rollback selects a previously verified immutable digest, verifies its Cosign
signature and matching release manifest, and deploys that digest through the
normal deployment control. Never move an immutable tag or rebuild an old
version.

If a release must be revoked:

1. Remove or mark the GitHub Release according to the incident procedure.
2. Deny the affected image digests in deployment controls.
3. Delete exact GHCR package versions only with a separately authorized
   credential.
4. Record the incident reference, tag, image digests, operator, UTC timestamps,
   and post-revocation verification.

Revocation cannot retract bytes already downloaded. Credential and deployment
denylists remain the controlling security mechanisms.
