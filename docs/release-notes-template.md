# ACR release notes template

Copy this template into the private GitHub Release description. Do not include
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

- Obtain `signing/cosign.pub` only from a reviewed repository commit, then
  download release archives, SPDX JSON, authoritative `SHA256SUMS`, and
  `SHA256SUMS.sig`; the public key is not a Release asset.
- Run `cosign verify-blob --key signing/cosign.pub --signature SHA256SUMS.sig --insecure-ignore-tlog SHA256SUMS`.
- Set `archive='<downloaded filename>'`, select `awk -v name="$archive" '$2 == name' SHA256SUMS`, require exactly one line, then verify that line with `sha256sum --check -` or `shasum -a 256 --check -` on macOS.
- Do not extract or execute an archive until tag, Cosign, and targeted checksum verification succeed.

### Security and operations

- Known security impact: `<none or approved advisory reference>`
- Revocation/incident reference: `<none or approved incident reference>`
- Distribution note: GitHub download counts are aggregate only; this release
  process does not provide identity-level download audit.
