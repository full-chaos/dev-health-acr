# ACR release notes template

Use this template for a GitHub Release description. Do not include credentials,
evidence bodies, customer data, or local filesystem paths.

## ACR `<version tag or full main commit SHA>`

**Channel:** main | stable | beta | development

**Commit:** `<40-character SHA>`

**Release date:** `<UTC RFC3339 timestamp>`

**Compatibility:** `<minimum sidecar/server compatibility and migration notes>`

For the `main` channel, the GitHub Release tag is the full commit SHA. The
Release is marked Latest, and its container digests are also reachable through
the mutable `latest` alias, only when the commit remains the current tip of
`main` at publication time.

### Changes

- `<user-visible change>`

### Upgrade and rollback

- Upgrade: `<required steps>`
- Rollback target: `<known-good immutable full SHA, version, or digest>`
- Do not move immutable tags or replace assets. See `docs/release-policy.md`.

### Verification

- Download the binary or OCI archive, authoritative `SHA256SUMS`, and
  `SHA256SUMS.sigstore.json` from the same GitHub Release.
- Verify `SHA256SUMS` with `cosign verify-blob --bundle SHA256SUMS.sigstore.json`
  and the release workflow's documented certificate identity and OIDC issuer.
- Set `archive='<downloaded filename>'`, select
  `awk -v name="$archive" '$2 == name' SHA256SUMS`, require exactly one line,
  then verify that line with `sha256sum --check -` or
  `shasum -a 256 --check -` on macOS.
- Do not extract or execute an archive until release identity, Cosign, and
  targeted checksum verification succeed.
- Verify the `acr-api` and `acr-mcp` GHCR references from
  `container-release-manifest.json` by immutable digest and Cosign signature.
  Treat `latest` only as a pointer to the current `main` build.

### Security and operations

- Known security impact: `<none or approved advisory reference>`
- Revocation/incident reference: `<none or approved incident reference>`
- Distribution note: GitHub download counts are aggregate only; this release
  process does not provide identity-level download audit.
