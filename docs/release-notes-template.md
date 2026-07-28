# ACR release notes template

Use this template for the GitHub Release description. Do not include
credentials, evidence bodies, customer data, or local filesystem paths.

## ACR `vX.Y.Z`

**Channel:** stable | beta | development  
**Commit:** `<40-character SHA>`  
**Release date:** `<UTC RFC3339 timestamp>`  
**Compatibility:** `<minimum sidecar/server compatibility and migration notes>`

### Changes

- `<user-visible change>`

### Upgrade and rollback

- Upgrade: `<required steps>`
- Rollback target: `<known-good immutable exact version>`
- Do not move tags or replace assets. See `docs/release-policy.md`.

### Verification

- Download the binary or OCI archive, authoritative `SHA256SUMS`, and
  `SHA256SUMS.sigstore.json` from the same GitHub Release.
- Verify `SHA256SUMS` with `cosign verify-blob --bundle SHA256SUMS.sigstore.json`
  and the release workflow's documented certificate identity and OIDC issuer.
- Set `archive='<downloaded filename>'`, select `awk -v name="$archive" '$2 == name' SHA256SUMS`, require exactly one line, then verify that line with `sha256sum --check -` or `shasum -a 256 --check -` on macOS.
- Do not extract or execute an archive until tag, Cosign, and targeted checksum verification succeed.
- Verify the `acr-api` and `acr-mcp` GHCR references from
  `container-release-manifest.json` by immutable digest and Cosign signature;
  never deploy a mutable tag.

### Security and operations

- Known security impact: `<none or approved advisory reference>`
- Revocation/incident reference: `<none or approved incident reference>`
- Distribution note: GitHub download counts are aggregate only; this release
  process does not provide identity-level download audit.
