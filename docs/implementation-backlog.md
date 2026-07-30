# ACR implementation handoff closure record

**Status:** Historical handoff closed on 2026-07-30.

This file previously translated the pre-v2.1 backlog into repository work. It
is no longer an active plan or tracker. The canonical project state, PRD, TRD,
decisions, and remaining work live in the Linear project
[Dev Health Agent Context Runtime (Context Fabric)](https://linear.app/fullchaos/project/dev-health-agent-context-runtime-context-fabric-2dcfe2d161a5).

## Completed foundation

- **Todo 8 — reproducible images and release artifacts:** complete. The release
  workflow publishes verified cross-platform binaries, OCI archives, checksums,
  SBOMs, signatures, and immutable GHCR images. See
  [`container-images.md`](container-images.md) and
  [`release-policy.md`](release-policy.md).
- **Todo 9 — local Compose integration:** complete. The repository owns the
  portable overlay at [`../deploy/compose/acr.compose.yml`](../deploy/compose/acr.compose.yml),
  isolated ACR persistence and migrations, TLS fixture setup, and the
  acceptance driver at [`../scripts/e2e/compose.sh`](../scripts/e2e/compose.sh).
- **Go API and MCP sidecar:** complete for the read-only SVS path, including
  capabilities, device login, credential lifecycle, context packets, evidence
  expansion, and client setup.
- **Web integration:** the Context Packet Explorer and trusted web assertion
  boundary are implemented in `dev-health-web` and `dev-health-acr`.
- **Helm and Kustomize:** repository-owned deployment artifacts, migration
  ordering, immutable images, existing-secret references, probes, rollback,
  and clean-room verification are implemented.

## Remaining work

Do not add active backlog items to this repository document. Track and close
remaining acceptance, product-validation, distribution, and follow-up defects
in Linear so issue state and decision context stay together.

## Interface ownership

- `contracts/**` and `internal/contracts/**`: contract owner; cross-repository changes require coordinated review.
- `internal/storage/**`, `migrations/**`: ACR data/platform team.
- `cmd/acr-api/**`, `internal/api/**`: hosted Go API team.
- `cmd/acr-mcp/**`, `internal/mcp/**`: sidecar team.
- Context Packet Explorer: `dev-health-web` frontend team.
- Entitlement metadata and service authentication: `dev-health-ops` integration owner.
