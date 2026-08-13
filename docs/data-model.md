# ACR operational data model

ACR reads Dev Health engineering evidence from ClickHouse and owns only its operational state in PostgreSQL.

## Read-only evidence boundary

The Go `EvidenceStore` resolves and queries:

- repositories and freshness;
- work graph edges and provenance;
- work items, pull requests, commits, and files;
- review outcomes and AI workflow runs;
- deployments and incidents;
- hotspot, churn, CI, and other packet signals.

No HTTP handler embeds raw ClickHouse SQL. Versioned adapters return typed evidence bundles and record a `query_version` in every packet.

## PostgreSQL schema

`migrations/postgres/0001_acr_core.sql` creates the isolated `acr` schema:

| Table | Purpose | Default retention |
| --- | --- | --- |
| `acr.client_credentials` | Hashed `fcacr_` credentials, scopes, repo allowlists, lifecycle metadata | Until revoked/administratively removed |
| `acr.context_packet_snapshots` | Immutable validated packet replay snapshots | 30 days |
| `acr.agent_episodes` | Idempotent append-only agent-run evidence and redaction tombstones | 90 days |
| `acr.audit_events` | Credential, read, denial, write, redaction, and purge audit trail | Policy-defined |
| `acr.context_fabric_projection_checkpoints` (`0006`) | Per-organization, per-source graph projection cursor | Mutable operational state, no retention |
| `acr.context_fabric_projection_rebuild_markers` (`0007`) | Crash-resumable marker for an in-progress graph rebuild | Deleted when the rebuild completes |
| `acr.context_fabric_investigation_results` (`0009`) | Immutable persisted `context_fabric_investigation_result.v1` snapshots (`internal/contextfabric/pginvestigation`), read back for `PriorSubjectReceipts` follow-up binding | Immutable; no purge path yet |
| `acr.context_fabric_org_model_config` (`0010`) | One row per organization: BYO LLM provider/base URL/model/fallback plus an AES-256-GCM sealed credential (`internal/contextfabric/modelconfigcrypto`, `internal/contextfabric/pgmodelconfig`) | Mutable; replaced on the next `PUT`, removed on `DELETE` |
| `acr.context_fabric_model_execution_receipts` (`0010`) | Insert-only `ModelExecutionReceipt` durable sink (`internal/contextfabric/pgmodelreceipts`), one row per model call | Append-only; no purge path yet |

The database does not hold plaintext bearer secrets or raw transcripts; `credential_ciphertext` is AES-256-GCM sealed, never plaintext.

## Tenant and repository isolation

Every store call receives a validated `Principal`. `org_id` and repository access are derived from authentication and never trusted from request payloads. Evidence expansion rechecks both dimensions even when the caller already owns the packet that contained the reference.

## Idempotency

`agent_episodes` has unique constraints on `(org_id, idempotency_key)` and `(org_id, client_episode_id)`. An identical retry returns the existing record. Reusing a key for a conflicting body returns `409`.

## Privacy lifecycle

Normal packet and episode history is immutable. Administrative redaction replaces sensitive payload fields and marks `redaction_state=redacted`. Expiry/purge leaves a minimal `purged_tombstone` when an audit record must remain.
