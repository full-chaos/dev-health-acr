# PRD v2.1: Dev Health Agent Context Runtime

**Status:** Active implementation plan  
**Date:** July 10, 2026  
**Supersedes:** PRD v2 recommendations and the v1 full-vision framing  
**Linear project:** Dev Health Agent Context Runtime  
**Tracking issue:** CHAOS-2898

## 1. Thesis

**ACR is the Dev Health diagnosis loop — State → Pressure → Cause → Evidence → Action — exposed to the agents increasingly doing engineering work.**

It is not a generic memory product, a separate customer-facing brand, or a context graph platform. It is a new agent-facing consumer of the same Dev Health evidence backbone.

## 2. Product and repository shape

ACR is a spin-off service for SVS:

- `dev-health-acr` source repository, publicly visible for unrestricted CI.
- Go hosted API (`acr-api`).
- Go local STDIO MCP sidecar (`acr-mcp`).
- Shared Go types, JSON Schema, OpenAPI, and golden fixtures.
- Context Packet Explorer remains in `dev-health-web` as the only non-Go ACR component.
- `dev-health-ops` remains the engineering evidence ingestion, work graph, billing, and entitlement system.

Public repository visibility is an operational CI choice, not an open-source
license grant. The intended future license for the sidecar, if approved, is
Apache 2.0. The commercial boundary is the hosted ACR service and organization
entitlement, not hidden client-side enforcement.

## 3. Smallest viable slice

### Read path

1. `context_for_task` MCP tool.
2. `source_evidence` MCP tool.
3. Hosted ACR read API.
4. Context Packet Explorer in `dev-health-web`.
5. Evidence-backed packet generation from existing Dev Health ClickHouse data.

### Optional write path

1. `record_episode` MCP tool disabled by default.
2. Idempotent append-only episode API.
3. ACR-owned Postgres episode persistence.
4. No fact promotion, review queue, docs drift, or active-intent system.

### Cut from SVS

- Memory review and durable memory facts.
- Active intents and fleet coordination.
- Docs drift as a standalone product.
- Production context graph backend.
- Separate frontend application.
- Hosted remote MCP endpoint; SVS uses a local STDIO server calling the hosted HTTP API.

## 4. Architecture

```text
Claude Code / Cursor / Codex / generic MCP client
                 │ STDIO
                 ▼
          acr-mcp (Go binary)
                 │ HTTPS + ACR client credential
                 ▼
          acr-api (Go hosted service)
        ┌────────┼──────────────┐
        ▼        ▼              ▼
 Dev Health   ACR Postgres   Entitlement/Auth
 ClickHouse   packets,       integration with
 evidence     episodes,      Dev Health
 (read-only)  credentials
        │
        ▼
 dev-health-web Context Packet Explorer
 (server-side call to acr-api)
```

### Service boundaries

- ACR reads existing work graph and AI workflow evidence through a read-only ClickHouse adapter.
- ACR owns its operational state in its own Postgres schema/database.
- ACR does not write final Dev Health metrics.
- ACR does not use External Push as transport.
- ACR does not ship in the default self-hosted Dev Health distribution.
- Public repositories may contain entitlement keys and integration hooks. The
  hosted service and entitlement remain the commercial boundary regardless of
  source visibility.

## 5. External API

Dedicated hosted service endpoints:

```http
GET  /healthz
GET  /readyz
GET  /api/v1/agent-context/capabilities
POST /api/v1/agent-context/context-packets
GET  /api/v1/agent-context/context-packets/{context_packet_id}
GET  /api/v1/agent-context/evidence/{evidence_ref_id}
POST /api/v1/agent-context/episodes
```

External Push remains separately namespaced under `/api/v1/external-ingest/*` and handles source-fact ingestion, validation, durable batching, and metric recompute. `agent_episode` is not an External Push record kind.

The read-only path must not wait on episode writeback. Packet generation and evidence expansion ship independently from snapshot replay and episode persistence.

## 6. Authentication and entitlement

The sidecar uses a dedicated ACR client credential, not a commercial license key.

Required credential scopes:

```text
context:read
evidence:read
episode:write
```

Required product entitlement:

```text
agent_context_runtime
```

The capability handshake reports the product entitlement separately from the credential permissions granted to the current principal.

Credentials are organization-scoped, optionally repository-scoped, expiring, revocable, rotatable, and audited. Current interactive web approval always grants all current and future repositories in the authenticated organization (`repository_scopes: ["*"]`). Exact repository grants remain protocol and service support for explicit clients and a future opt-down UX; they are not a current web approval choice. Repository hints, local checkout state, and analytics inventory never participate in authorization. The web uses its existing Dev Health session through a server-side proxy or trusted assertion; browser code never receives the ACR service credential.

No standalone auth service is introduced in SVS.

## 7. Context packet contract

Versioned contracts:

```text
context_packet_request.v1
context_packet.v1
context_packet_item.v1
evidence_ref.v1
expanded_evidence.v1
capabilities.v1
acr_client_credential.v1
agent_episode_create.v1
agent_episode.v1
error.v1
```

The packet records:

- Exact request and generated timestamp.
- Resolved organization, repo, branch, and commit.
- Scope resolution: `exact_commit`, `branch_filtered`, `repo_fallback`, or `unresolved`.
- Fallback reasons.
- Query and ranking versions.
- State, Pressure, Cause, Evidence, and Action items.
- Claim kind: observed, inferred, or recommendation.
- Rule ID, confidence, priority, validity scope, conflict/staleness flags.
- Stable evidence references and related entities.
- Required checks and next steps.
- Source watermarks, coverage, partial/degraded state.
- Item and output-token budget.

The initial Pressure category uses evidence already available: churn/hotspot signals, open PR overlap, requested changes, failed CI, incidents, and stale/missing ingestion. Active agent intents are deferred.

The initial Cause category reuses existing AI workflow traversal, review outcomes, closed/unmerged attempts, and deployment-to-incident evidence.

## 8. Scope semantics

- `commit_sha` is authoritative when resolvable.
- `branch` is authoritative only for branch-bearing evidence and otherwise acts as a hint.
- Repo-wide fallback is explicit in `resolved_scope`.
- The Go sidecar discovers the local Git root, remote slug, branch, SHA, and MCP roots rather than relying on model-entered values.
- Every packet explains missing and stale sources.

## 9. Evidence and retrieval

Retrieval is deterministic and evidence-first for SVS:

1. Direct task/work graph linkage.
2. PR/commit/file adjacency.
3. Existing AI workflow traversal.
4. Native and explicit provenance before heuristic evidence.
5. Rule-identified recommendations rather than uncited generated claims.

Every observed or inferred item must have evidence. Recommendations may be rule-derived but must identify the rule and supporting packet context.

The service has explicit item, byte, token, query-time, and source limits. Timeouts can return a clearly partial packet.

## 10. Persistence and privacy

- Existing ClickHouse is read-only evidence storage.
- ACR-owned Postgres stores packet snapshots, episode metadata, client credentials, and audit records.
- Packet snapshots default to 30-day retention.
- Episode metadata defaults to 90-day retention.
- Raw transcripts are not stored in SVS.
- Optional transcript references are opaque and never fetched.
- Episode writes require `Idempotency-Key` and `client_episode_id`.
- Normal episode history is immutable; administrative redaction/purge leaves an audit tombstone.

## 11. Security

- Retrieved content is untrusted data, never instructions.
- Evidence URLs are references only; the service does not fetch arbitrary URLs.
- Organization and repository scope is derived from authenticated server context.
- The all-repositories selector never weakens the authenticated organization boundary.
- Evidence reference lookup independently enforces scope.
- Credentials are never logged or accepted via query parameters.
- Web output sanitizes markdown and links.
- Read and write scopes are separate.
- Rate limits, payload limits, audit logs, and cross-tenant negative tests are mandatory.

## 12. Sidecar and client support

The sidecar is one generic MCP server with thin setup recipes for:

- Claude Code.
- Cursor.
- Codex.
- Other standards-compliant MCP clients.

SVS transport is local STDIO. The sidecar calls the hosted ACR API over HTTPS.

Required binary behavior:

- `serve` or default STDIO MCP mode.
- `version`.
- `doctor` with secret-safe diagnostics.
- Local Git context discovery.
- Backend contract compatibility check through `/api/v1/agent-context/capabilities`.
- Read-only default.
- Explicit writeback enablement.

## 13. Web surface

`dev-health-web` receives one ACR route: Context Packet Explorer.

It must support:

- Goal/repo/branch/task inputs.
- State/Pressure/Cause/Evidence/Action grouping.
- “Why included” and evidence expansion.
- Freshness, coverage, fallback, partial, and not-entitled states.
- Sanitized evidence text.
- Admin-only retrieval debug information.
- Lightweight “incorrect / stale / irrelevant” feedback for evaluation, not a memory-review queue.

## 14. Release and operations

The Go repository owns:

- Cross-platform macOS/Linux/Windows builds for arm64/amd64.
- Checksums and signed artifacts.
- SBOM and dependency scanning.
- Private download authorization.
- Service/sidecar compatibility metadata.
- Release channels and rollback.

The hosted service defines SLOs and metrics for packet latency, empty packets, partial/degraded packets, evidence failures, entitlement denials, rate limiting, packet size/token use, and episode write failures.

## 15. Evaluation

Validation uses repeated, controlled cold-agent vs context-agent runs:

- Same agent, model version, task, and permissions.
- Multiple fixed tasks, not one cherry-picked case.
- Factual-error count.
- Relevant file/test recall.
- Citation precision.
- Irrelevant packet-item rate.
- Time to first correct plan.
- Added latency and context-token cost.

The public demo must use safe synthetic/public evidence and the real sidecar/API contracts.

## 16. Phase sequence

### Phase 0 — closed by v2.1 decisions

- Go private service and Go sidecar.
- Separate hosted ACR API.
- Dedicated credentials and entitlement keys.
- External Push boundary.
- Storage split and context-graph compatibility.
- Contract v1 shapes.
- Threat model, privacy, and retention defaults.

### Phase 1 — read-only vertical slice

- Go EvidenceStore adapters and ContextAssembler.
- Evidence expansion.
- Hosted read API.
- Go MCP read tools.
- Context Packet Explorer.
- Read-only E2E and controlled demo.

### Parallel writeback track

- Packet snapshots.
- Episode persistence.
- Episode API.
- Opt-in `record_episode`.
- Writeback E2E.

### Phase 2 — prospect signal

- 5–10 prospect conversations.
- Positioning and installation signal.
- Phase 3 only for prospect-validated needs.

## 17. Non-goals

- No separate frontend app.
- No Python ACR service components.
- No production context graph backend in SVS.
- No memory promotion/review queue.
- No active intents/fleet coordination.
- No docs drift product.
- No auth-service spinout.
- No External Push reuse as ACR transport.
- No raw transcript storage.
