# SVS threat model, privacy, redaction, and retention contract

This document is the implementation contract for ACR security and data handling in SVS. Requirements not marked **current** are owned by the named downstream issue and must be implemented and tested before that issue can close.

## Trust boundaries and data flow

```text
Untrusted browser content
  -> dev-health-web server (sanitizes rendered evidence)
  -> trusted hosted user assertion/JWT
  -> acr-api

Untrusted agent output and task input
  -> acr-mcp over local STDIO
  -> fcacr_ bearer over HTTPS
  -> acr-api

dev-health-ops entitlement source
  -> organization-scoped agent_context_runtime decision
  -> acr-api independently enforces entitlement, permission, org, and repo scope

acr-api
  -> read-only, scoped ClickHouse evidence adapters
  -> ACR-owned Postgres credentials, snapshots, episodes, and audit events

Existing local CodeGraph index
  -> read-only JSON commands inside acr-mcp
  -> bounded local evidence only; never uploaded to acr-api
```

Trust does not transfer across an arrow automatically:

- The browser and every retrieved excerpt, issue, PR, review, document, artifact, URL, task goal, and transcript reference are untrusted data.
- `acr-mcp` does not make evidence trustworthy. It must not execute retrieved instructions and must keep write tools disabled unless the user explicitly enables them.
- `acr-api` derives organization identity, repositories, permissions, and product entitlement from authenticated server context. Request bodies cannot override them.
- A trusted web assertion and an ACR client credential are distinct authentication paths. A Dev Health license artifact is never an ACR bearer credential.
- Every opaque packet, evidence, and episode lookup rechecks organization and repository authorization. Incident evidence reaches a repository only through an active service-to-repository mapping; unmapped and foreign incidents are not disclosed. A denied explicit repository selector may return `repo_forbidden`; a foreign, deleted, or unknown opaque object must use the same generic not-found response so existence is not disclosed.
- ClickHouse is read-only evidence. ACR Postgres owns operational state. Neither path uses External Push.
- ACR's CodeGraph integration is direct/managed guarded: it consumes an existing
  local index through fixed read-only JSON commands only. It never installs,
  creates, refreshes, queries/parses SQLite storage, or uploads CodeGraph
  source/index data. A bounded read-only `codegraph.db` identity check is allowed
  on supported Unix platforms and is not an evidence/query implementation.
- Local provider failure is never authority for a local-only result. Hosted
  bootstrap and the hosted packet remain required; unavailable local state
  degrades to hosted-only behavior.

## Data classes

| Class | Meaning | Examples |
| --- | --- | --- |
| Secret | Credential material that must never be returned or logged | plaintext `fcacr_` token; bearer header |
| Confidential | Customer/repository content or linkable operational detail | goals, excerpts, summaries, paths, repository/commit scope, payload JSON |
| Internal | Identifiers, policy, lifecycle, and audit metadata | org/repo IDs, versions, status, timestamps, actor/resource IDs |

Plaintext credentials and raw transcript content are not persistence classes because ACR must not persist them.

## Persisted fields and retention

The column lists below are exhaustive for `migrations/postgres/0001_acr_core.sql`. JSON payloads are classified again by logical content because a single JSONB column contains fields with different sensitivity.

### `acr.client_credentials`

| Fields | Class | Retention requirement |
| --- | --- | --- |
| `credential_id`, `org_id`, `name`, `token_prefix`, `repository_scopes`, `scopes`, `created_by`, `created_at`, `expires_at`, `revoked_at`, `last_used_at` | Internal | Current migration has no purge timestamp or deletion job. Rows remain after expiry/revocation until an authorized credential-deletion policy is implemented and disclosed; revocation only makes a row unusable. |
| `token_hash` | Secret | Same row lifetime; never logged or returned. Plaintext is never persisted. |
| `last_used_ip`, `last_used_user_agent` | Confidential | Same row lifetime, with access restricted to authorized operational use. |

### `acr.context_packet_snapshots`

| Fields | Class | Retention requirement |
| --- | --- | --- |
| `context_packet_id`, `org_id`, `repo_id`, `request_id`, `schema_version`, `query_version`, `ranking_version`, `scope_resolution`, `status`, `generated_at`, `expires_at`, `created_at` | Internal | `expires_at` is mandatory. The migration records 30 days as the default; CHAOS-2917 must set and enforce expiry and purge. |
| `repo_slug`, `payload` | Confidential | Same snapshot expiry. Payload redaction/purge cannot silently recompute a packet. |

Snapshot `payload` logical fields are Confidential when they contain the goal, task reference, repository/branch/commit/files, item titles/summaries, evidence excerpts or references, next steps, and required checks. Schema/query/ranking versions, coverage, freshness, and unavailable-source status are Internal unless combined with confidential source identifiers.

### `acr.agent_episodes`

| Fields | Class | Retention requirement |
| --- | --- | --- |
| `episode_id`, `org_id`, `repo_id`, `context_packet_id`, `client_episode_id`, `idempotency_key`, `schema_version`, `outcome`, `retention_class`, `redaction_state`, `started_at`, `ended_at`, `created_at`, `expires_at`, `redacted_at` | Internal | Driven by the validated retention class below. |
| `repo_slug`, `payload` | Confidential | Same retention class. Raw transcripts remain prohibited. |

Episode `payload` logical fields classify goal, task reference, repository/branch/commit, agent/model identity, summary, files touched, artifact URIs, tests, opaque transcript reference, and redacted transcript summary as Confidential. Client/version values and outcome are Internal when stored separately from customer content.

Retention classes are contract values, not suggestions:

- `default_90d`: T6 sets `expires_at` to 90 days from creation.
- `short_30d`: T6 sets `expires_at` to 30 days from creation.
- `legal_hold`: T6 leaves expiry unset and requires an authorized administrative release process.
- `no_persist`: T6 stores no episode content. To preserve identical-retry and conflicting-key behavior, it may retain an internal idempotency tombstone/digest under the existing unique keys, with `redaction_state=purged_tombstone` and no readable episode payload. Public reads use generic not-found. If the existing table cannot represent that tombstone without retaining prohibited content, T6 must make the full contract/migration update before shipping rather than weakening idempotency.

### `acr.audit_events`

| Fields | Class | Retention requirement |
| --- | --- | --- |
| `audit_event_id`, `org_id`, `repo_id`, `actor_type`, `actor_id`, `action`, `resource_type`, `resource_id`, `status`, `request_id`, `created_at` | Internal | Existing Dev Health compliance policy; this repository does not define a duration or expiry column. Deployment documentation must disclose the effective policy. |
| `metadata` | Confidential | Same audit policy. Metadata must be allowlisted and must exclude credentials, bearer headers, raw transcripts, and raw evidence bodies. |

## Redaction and purge semantics

CHAOS-2904 and CHAOS-2917 implement these rules without changing v1 wire semantics silently:

1. `active` records are readable only after independent org/repo authorization.
2. Episode redaction changes `redaction_state` to `redacted`, records `redacted_at`, and writes an audit event whose allowlisted metadata carries the administrative reason. A readable redacted projection preserves required IDs, repository/scope, client, timestamps, outcome, and retention metadata; replaces `goal` and `summary` with a fixed redacted marker; clears optional task reference and artifact arrays; and sets transcript mode to `none`. The projection must validate as `agent_episode.v1`.
3. Expiry purge retains only the database columns required for org/repo scoping, uniqueness/idempotency, lifecycle state, and audit correlation; sets `redaction_state=purged_tombstone`; and replaces episode content with a non-readable tombstone representation. Code must branch on `redaction_state` before decoding payload. Public reads return generic not-found. If the NOT-NULL payload column or service return type cannot support this safely, T6 must update the migration and every contract-first artifact before shipping.
4. Packet expiry deletes the snapshot row and writes an audit event; the current packet table has no tombstone state and its payload is NOT NULL. Retrieval never regenerates a packet in place of an expired snapshot.
5. A redaction/purge operation is org-scoped, auditable, idempotent, and cannot erase the audit event that records it.
6. If implementation cannot satisfy these rules with existing required response fields, the owning issue must perform the full contract-first update before shipping; it must not return an invalid partial object.

## Residency disclosure

- The repository fixes storage roles, not geographic regions: ClickHouse holds read-only engineering evidence, including service-to-repository associations used to scope incidents, and ACR Postgres holds operational state.
- The actual region, subprocessors, backups, replicas, and cross-region transfer behavior are deployment facts. Production deployment documentation must disclose them before customer use; this document does not invent a region.
- `acr-mcp` reads a credential from `ACR_API_TOKEN` (works on every supported platform), from an OS keyring entry, or from `ACR_API_TOKEN_FILE`. Token-file (and CA-bundle) loading is supported only on macOS and Linux, where it checks restrictive file permissions; on every other platform, including Windows, it fails closed and refuses to load the file at all.
- `acr-mcp login` **does** create and manage a home-directory credential file: when the keyring is unavailable it writes `~/.acr/token`, creating that parent at mode `0700` if it does not exist. A parent that already exists is inspected, not rewritten -- group- or world-writable is refused outright and every other mode bit is left as found, because `ACR_API_TOKEN_FILE` is operator-supplied and unconditionally chmod-ing its parent once reduced an entire home directory to `0700`. `acr-mcp logout` removes that file. Only `login`, `login --refresh`, and `logout` write or delete local credential material; `serve` never does.
- The sidecar must not persist packets, episode payloads, or raw transcripts locally.

## Evidence content, markup, and URIs

- URI fields may contain absolute server-generated HTTPS/provider references allowed by the v1 schemas. They are references only.
- ACR never dereferences or fetches an evidence URI discovered in source content. Evidence expansion resolves opaque IDs through typed, authorized server-side adapters.
- CHAOS-2906 allowlists provider URI generation and bounds excerpts/structured fields. It strips hidden provider fields and never exposes credentials or transcript bodies.
- `dev-health-web` sanitizes untrusted Markdown/HTML and external links before rendering. No source text becomes executable HTML, JavaScript, shell, prompt, or tool instruction.
- Public demos and fixtures require explicit synthetic/public-safe review. CHAOS-2918 owns the fixed corpus and hash manifest.

## Local CodeGraph privacy boundary

The local provider can return bounded, untrusted, repository-relative evidence
to the MCP client, but it must not send raw source, complete CodeGraph JSON,
index bytes, local roots, local locators, credentials, or provider payloads to
the hosted API. Local evidence IDs are opaque sidecar identifiers; expansion is
served from the bounded local cache and unknown/expired IDs do not fall through
to hosted expansion. The Task 8 capture harness exercises these negative upload
sentinels; its receipt is evidence of fixture coverage, not a claim that an
operator has run a live CodeGraph environment.

## Local credential lifecycle boundary

`acr-mcp login` and `acr-mcp logout` are the only local operations that create
or destroy credential material, and they hold to four rules.

- **Nothing local is deleted while a credential may still be live.** Logout
  enumerates every configured location -- environment, OS keyring, token file --
  and revokes each distinct value before removing any of them. Resolving the
  single highest-precedence credential instead left lower-precedence ones live on
  the server with their local copies gone. Enumeration fails closed: an
  unreadable keyring, an unparseable disable flag, or an unreadable token file
  removes nothing at all.
- **An ambiguous write is treated as a write.** A credential store that commits
  and then fails -- a keyring mutation whose write-out or reply fails, a token file
  renamed into place whose directory fsync fails -- returns the candidate locator
  alongside the error, so login revokes server-side and purges exactly that
  location instead of leaving a revoked-but-readable secret somewhere nobody was
  told about.
- **Removal is gated on the same boundaries as reading, plus the parent
  directory.** A file is unlinked only after a no-follow open, a regular-file
  fstat, a group/world permission check, and an ACR token-shape check, and only
  if its parent denies group and world write. The proof and the unlink are two
  operations on a path, so a window exists between them; refusing a
  shared-writable parent narrows who can act in that window to principals who
  can already write the directory, rather than any local user. That is a
  reduction of the attacker set, not the elimination of a race.
- **Server-supplied text never becomes local execution or terminal output.** The
  device verification address is validated (https, or http to a validated loopback
  address; no userinfo, control characters, whitespace, or `fcacr_` text) *before*
  it is printed or handed to an opener, and `login --no-browser` skips the launch
  entirely. A launched opener receives an allowlisted environment carrying no
  `ACR_` variable, runs in its own process group, and is reaped under a fixed
  deadline. Operator-facing cleanup locations are token-redacted, bounded, and
  quoted, so a configured path or keyring address cannot forge log lines.

The MCP surface itself is local STDIO with no network listener; credential
lifecycle traffic is outbound HTTPS to the hosted API only, so no lifecycle
operation is reachable from another host.

## Limits: current contract versus downstream enforcement

**Current contract/config validation**:

- request ID: 8-256 characters; goal: 1-4,000; repository slug: at most 512;
- branch: at most 512; task reference: at most 1,024; files: at most 200 entries, each 1-2,048; time window: 1-365 days;
- packet options: 1-50 items, 500-16,000 estimated tokens, 8,192-1,048,576 serialized bytes;
- service request/read/write/idle/shutdown timeouts must be positive; configured requests per minute must be positive;
- episode IDs: at most 256, goal: at most 4,000, summary: at most 8,000, files touched: at most 500, artifact URIs: at most 100, tests: at most 200; string item bounds come from `agent_episode_create.v1.schema.json`;
- transcript mode is limited to `none`, `opaque_ref`, or `redacted_summary`; there is no raw transcript mode.

Validation is not proof of runtime enforcement. The following are required downstream:

- CHAOS-2905 enforces actual item/token/serialized-byte truncation and labeled empty/partial/degraded results.
- `expanded_evidence.v1` currently caps an excerpt at 1,000 characters. CHAOS-2906 must enforce that wire limit and add bounded structured-output tests.
- CHAOS-2907 enforces HTTP body limits, cancellation, timeouts, authorization, and safe error mapping.
- CHAOS-2927 enforces separate authentication/context/evidence/snapshot/episode limits, per-org concurrency, and safe retry hints. A positive configured RPM value alone is not enforcement.
- CHAOS-2904 enforces `no_persist`, retention expiry, artifact/summary limits, and rejects transcript representations outside the contract.

## Negative-test ownership matrix

| Control | Status and evidence | Owner before close |
| --- | --- | --- |
| Reject Dev Health license/malformed token as ACR bearer | Current: `internal/auth/token_test.go` | CHAOS-2924 (Done) |
| Explicit repository-scope denial and independent permission denial | Current: `internal/auth/middleware_test.go`; external response is forbidden | CHAOS-2924 (Done) |
| Unknown/revoked/expired credential reason does not leak | Current: `internal/auth/middleware_test.go` | CHAOS-2924 (Done) |
| Foreign/unknown opaque evidence ID is non-enumerating generic not-found | Future integration test in evidence resolver/API packages | CHAOS-2906 + CHAOS-2907 |
| Packet request schema bounds and unknown fields | Current golden/schema profile; add boundary cases in `internal/contracts/v1/contracts_test.go` | CHAOS-2905 + CHAOS-2907 |
| Deterministic truncation, timeout, empty/partial/degraded behavior | Future assembler tests using `internal/evalfixture` | CHAOS-2905 |
| Prompt-injection text remains data and cannot alter rules/tools | Future assembler/resolver golden negative cases | CHAOS-2905 + CHAOS-2906 |
| Malicious Markdown/HTML/URL is sanitized and never fetched | Future resolver tests plus web rendering tests | CHAOS-2906 + CHAOS-2910/CHAOS-2911 |
| Evidence excerpt/structured-output and safe-URI bounds | Future resolver boundary/allowlist tests | CHAOS-2906 |
| Cross-org snapshot round trip and stable replay after source change | Future memory/Postgres store tests | CHAOS-2917 |
| Episode retention class, `no_persist`, duplicate conflict, cross-org access | Future memory/Postgres service tests | CHAOS-2904 |
| Redaction/purge leaves an auditable tombstone and hides public payload | Future store/service tests with audit assertion | CHAOS-2904 + CHAOS-2925 |
| Bearer, token hash, raw evidence, and transcript never enter logs/traces | Future log-sink/telemetry capture tests | CHAOS-2927 |
| Per-org concurrency, endpoint quotas, retry hints, oversized HTTP body | Future limiter/API tests | CHAOS-2927 + CHAOS-2907 |
| Write tool is disabled by default and requires local enablement plus server scope | Future API/MCP tests | CHAOS-2909 |
| Synthetic/public-safe fixture provenance and hashes | Current after PR #2: `internal/evalfixture/verify_test.go` validates `testdata/evaluation/v1` | CHAOS-2918 (Done) |

## Release gate for dependent issues

CHAOS-2904, CHAOS-2906, CHAOS-2907, CHAOS-2917, CHAOS-2927, and CHAOS-2909 may close only when their rows above have executable tests. Passing `make verify` before those implementations exist proves contract consistency, not that future security controls are already enforced.
