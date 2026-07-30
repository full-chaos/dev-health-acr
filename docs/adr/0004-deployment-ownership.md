# ADR-0004: ACR deployment ownership stays out of Ops

**Status:** Accepted
**Date:** 2026-07-14

## Decision

Deployment packaging is split by repository ownership, not by infrastructure
technology:

- `dev-health-acr` owns 100% of ACR
  Helm/Kubernetes/Compose deployment artifacts.
- `dev-health-ops` owns provider hardening only and ships zero ACR workload,
  image reference, database-init script, or deployment manifest.
- The local root orchestration file `../compose.yml` is an operator-owned
  artifact outside both repositories and is the only place ACR and Ops
  services run together for local development. It is never vendored into
  either product repository's tracked deployment assets.

## Ownership table

| Deployment surface | Path | Owning repository | Notes |
| --- | --- | --- | --- |
| Helm chart | `deploy/helm/acr` | `dev-health-acr` | Existing-Secret-only, immutable image required |
| Raw Kubernetes | `deploy/kubernetes/acr/base` plus `overlays/{development,staging,production}` | `dev-health-acr` | Mirrors Helm security, migration, and probe semantics |
| Local Compose overlay | `deploy/compose/acr.compose.yml` plus `deploy/compose/root-compose.patch` | `dev-health-acr` | Overlay/patch only; the root file itself stays operator-owned |
| Local orchestration file | `../compose.yml` (outside both repositories) | Operator, not either repository | Never committed inside `dev-health-acr` or `dev-health-ops` |
| Existing provider Helm chart | `deploy/helm/dev-health` | `dev-health-ops` | Read-only pattern reference; never an ACR destination |
| Ops internal ACR entitlement API | `src/dev_health_ops/api/internal/acr.py` | `dev-health-ops` | Application code, not a deployment artifact; out of scope for this boundary |

No path under `dev-health-ops/deploy/**` or `dev-health-ops/docker/**` may
name or package ACR. [`scripts/deploy/verify-boundaries.sh`](../../scripts/deploy/verify-boundaries.sh)
enforces this mechanically by scanning those two directories in a live Ops
checkout for any ACR-named artifact.

## External Postgres, ClickHouse, and Ops dependencies

Every ACR deployment surface (Helm, raw Kubernetes, Compose overlay) treats
the following as external, pre-existing endpoints supplied by the deployment
environment, never provisioned by ACR packaging:

- **Postgres**: an ACR-owned schema/database on an externally provisioned
  instance; only a connection string is supplied at deploy time.
- **ClickHouse**: read-only Dev Health evidence access ([ADR-0003](0003-storage-and-evidence.md));
  ACR packaging never provisions, seeds, or migrates ClickHouse.
- **Dev Health Ops entitlement/service-token endpoint**: the existing
  `dev-health-ops` internal ACR API ([ADR-0002](0002-auth-entitlements.md));
  ACR packaging never deploys or forks an Ops service.

## Existing-Secret-only contract

ACR deployment manifests (Helm and raw Kubernetes) reference Kubernetes
`Secret` objects by name only. They never template, generate, or embed
secret literals in a `ConfigMap`, values file, or rendered manifest.
Offline validation asserts every credential, DSN, and token field is a
non-empty reference to an existing Secret (`existingSecret`/`secretName`
style keys), never an inline value.

## Immutable images

Every ACR container reference in a deployment manifest is an immutable,
fully qualified image tag or digest, consistent with the immutable
input discipline already required for build inputs in
[`docs/container-images.md`](../container-images.md). The Helm chart and raw
Kubernetes manifests both reject a mutable (for example `latest`) image
reference before rendering. Images are built only from `dev-health-acr`
([ADR-0001](0001-go-service-boundary.md)) and never built by, or reside in,
`dev-health-ops`.

## No MCP workload

`acr-mcp` is a local STDIO binary invoked by the operator or agent host
([`docs/prd-v2.1.md`](../prd-v2.1.md), Section 2). No deployment surface —
Helm chart, raw Kubernetes manifest, or Compose overlay — instantiates it as
a `Service`, `Deployment`, container, or long-running process. Template
guards in each surface reject any manifest that would run `acr-mcp` as a
workload.

## Superseded paths

This ADR supersedes the Ops-owned packaging direction recorded in the
superseded `acr-developer-deployment` plan, Todos 9-11, which proposed
`../ops/deploy/helm/acr/` and `../ops/deploy/kubernetes/acr/` inside
`dev-health-ops`. That direction is no longer valid: ACR packaging
must never live inside `dev-health-ops`. The replacement paths are the
`dev-health-acr`-owned paths in the ownership table above, tracked as
`acr-project-completion` plan Todos 5 (this ADR), 7 (Compose overlay), 8
(Helm chart), and 9 (raw Kubernetes overlays).

## Consequences

- [`scripts/deploy/verify-boundaries.sh`](../../scripts/deploy/verify-boundaries.sh)
  enforces this boundary mechanically: it fails closed if this ADR or any of
  its required decisions is missing, and it fails closed if any ACR-named
  deployment artifact appears under `dev-health-ops/deploy/**` or
  `dev-health-ops/docker/**`.
- Future Todos 7-9 implement the ACR-owned Helm/Kubernetes/Compose paths
  named above; none of that work may be redirected into `dev-health-ops`.
- The operator-owned root Compose file remains outside both repositories'
  Git history; only a portable overlay and patch are committed, inside
  `dev-health-acr`.
- Threat-model residency and retention requirements
  ([`docs/threat-model.md`](../threat-model.md)) apply unchanged to whichever
  deployment surface (Helm, raw Kubernetes, or Compose) is in use; this ADR
  only fixes packaging ownership, not data handling.
