# ACR implementation backlog handoff

This document translates the pre-v2.1 Linear backlog into the decided private Go service boundary.

## Superseded deployment plan status

`.omo/plans/acr-developer-deployment.md` is superseded for remaining work by
`.omo/plans/acr-project-completion.md`, but its already-completed evidence
remains valid and is tracked here rather than re-executed.

- **Todo 8** (reproducible `acr-api`/`acr-mcp` container images, CHAOS-2943) is
  **complete**: merged at `11c44ef812f9f9ae71a044d64f00ebae1ea1602f`
  (`feat(containers): add hardened reproducible images (CHAOS-2943)`). See
  [`container-images.md`](container-images.md) and
  `.omo/evidence/task-8-acr-developer-deployment.txt`. No target publishes or
  pushes an image to any registry.
- **Todo 9** (root local Compose integration with isolated ACR persistence and
  migrations) is **pending**. It is not yet implemented or tested here; the
  equivalent work continues under `.omo/plans/acr-project-completion.md` Todo 7
  and must not be documented as working, merged, or verified until that todo's
  Compose acceptance evidence exists.
## Phase 1 foundation

| Linear issue | Repository | Decided implementation |
| --- | --- | --- |
| CHAOS-2921 | `dev-health-ops`, `dev-health-web` | Correct BSL package metadata and publish the hosted `agent_context_runtime` entitlement/integration state. |
| CHAOS-2922 | `dev-health-acr` | Create private repository and import this contract baseline. |
| CHAOS-2923 | `dev-health-acr` | Go API shell, capabilities, configuration, service wiring, and storage interfaces. |
| CHAOS-2924 | `dev-health-acr` | Hashed `fcacr_` credentials, org/repo authorization, rotation/revocation, audit. |
| CHAOS-2925 | `dev-health-acr`, `dev-health-web` | Threat model, retention, redaction, sanitization, and negative tests. |
| CHAOS-2926 | `dev-health-acr` | Signed private binaries/containers, SBOM, compatibility, rollback. |
| CHAOS-2927 | `dev-health-acr` | Rate limits, usage metering, service metrics, tracing, and SLOs. |

## Existing issue reinterpretation

All backend work below is implemented in `dev-health-acr`, not as Python ACR runtime code in `dev-health-ops`.

| Existing issue | v2.1 contract |
| --- | --- |
| CHAOS-2904 | Implement the Go `EpisodeStore`, `acr.agent_episodes` migration behavior, idempotency, retention, and redaction tombstones. |
| CHAOS-2905 | Implement the Go deterministic ContextAssembler over typed `EvidenceStore` adapters. Enforce packet budgets, scope fallback disclosure, query/ranking versions, freshness, coverage, and partial/degraded status. |
| CHAOS-2906 | Implement Go evidence expansion by opaque `evidence_ref_id`, with independent org/repo authorization and bounded untrusted excerpts. |
| CHAOS-2907 | Restrict to release-critical read API routes: capabilities, packet generation, and evidence expansion. It must not wait on snapshots or episodes. |
| CHAOS-2908 | Implement the private Go STDIO MCP server, Git context discovery, capabilities handshake, Claude/Cursor/Codex recipes, `version`, and `doctor`. |
| CHAOS-2909 | Add opt-in Go `record_episode` only after the write API exists. |
| CHAOS-2917 | Implement immutable Go/Postgres packet snapshots and 30-day default retention. |
| CHAOS-2910/2911 | Keep the UI in `dev-health-web`; add entitlement/unavailable states, scope/freshness/degraded display, evidence sanitization, admin debug restrictions, and evaluation feedback. |
| CHAOS-2912 | Document private Go binary installation, hosted-only boundary, client setup, credential scopes, and External Push separation. |
| CHAOS-2913 | Run multiple controlled repeated tasks and score errors, recall, citation precision, irrelevant context, latency, and token cost. |
| CHAOS-2914 | Read-only E2E is release-critical. Episode writeback E2E is a separate, non-blocking track. |
| CHAOS-2918 | Choose and seed the safe demo scenario early; it should inform retrieval/ranking implementation and block the evaluation harness. |

## Critical path

```text
CHAOS-2921 + CHAOS-2922
  → CHAOS-2923 + CHAOS-2924 + CHAOS-2925
  → CHAOS-2905 + CHAOS-2906 + CHAOS-2927
  → CHAOS-2907 read API
  → CHAOS-2908 MCP read tools
  → CHAOS-2911 web integration
  → CHAOS-2914 read-only E2E
  → CHAOS-2913 controlled evaluation
  → CHAOS-2915 prospect validation
```

## Parallel writeback track

```text
CHAOS-2917 packet snapshots
  + CHAOS-2904 episode persistence
  → write/replay API route issue
  → CHAOS-2909 MCP write tool
  → writeback-specific E2E issue
```

The read-only product must not wait for this track.

## Missing Linear split issues

Create or split these before implementation reaches the API layer:

1. **ACR Go API: write/replay routes** — `GET context-packets/{id}` and `POST episodes`; blocked by CHAOS-2917 and CHAOS-2904.
2. **ACR QA: writeback E2E** — verifies idempotent retries, disabled-by-default sidecar behavior, redaction, and no External Push/recompute calls.

## Interface ownership

- `contracts/**` and `internal/contracts/**`: contract owner; cross-repository changes require coordinated review.
- `internal/storage/**`, `migrations/**`: ACR data/platform team.
- `cmd/acr-api/**`, future `internal/api/**`: hosted Go API team.
- `cmd/acr-mcp/**`, future `internal/mcp/**`: sidecar team.
- Context Packet Explorer: `dev-health-web` frontend team.
- Entitlement metadata only: `dev-health-ops` integration owner.
