# ACR release notes: credential lifecycle runbooks and drills

This page follows [the release notes template](../release-notes-template.md). The
`<...>` fields are filled in when the GitHub Release is published.

## ACR `<version tag or main-<full-sha>>`

**Channel:** main

**Commit:** `<40-character SHA>`

**Release date:** `<UTC RFC3339 timestamp>`

**Compatibility:** documentation only. No configuration, schema or API change.

### Changes

- New runbook: [revoke an ACR credential](../runbooks/credential-revoke.md). It
  documents the operator revoke, its checks and its errors. In a drill on
  2026-11-12 the revoked credential was refused at once and showed as revoked in
  the identity service projection; the refusal held with the identity service down
  and after an `acr-api` restart.
- New runbook: [rotate the `acr-api` workload credential](../runbooks/workload-rotation.md).
  It rotates into a second file and delivers it by one rename inside a bounded
  overlap, then revokes the previous value. In a drill on 2026-11-10 `acr-api`
  kept exchanging during the overlap and the previous value was refused after it.
- The rollback rehearsal for the `acr-api` credential flip is recorded in
  [the workload token page](../acr-api-workload-token.md#rollback).
- Named limits: the workload rotation runbook covers Compose only; a credential in
  an organization the identity service does not know ends in a final
  `409 projection_refused` in the projection (it stays refused in `acr`); two
  rollbacks were not rehearsed (`acr-projector`, and a rollback while `go-api` is
  down). Drill numbers were measured once and are not service levels.

### Upgrade and rollback

- Upgrade: none.
- Rollback target: not applicable (documentation only).
- Do not move immutable tags or replace assets. See [the release policy](../release-policy.md).

### Verification

Follow the verification steps of the release notes template for the release
assets. This page ships no binary of its own.
