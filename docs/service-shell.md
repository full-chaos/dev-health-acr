# `acr-api` service shell

The Phase 1 service shell exposes health plus a fail-closed hosted read boundary.

## Commands

```bash
go run ./cmd/acr-api version
go run ./cmd/acr-api serve
```

The default listen address is `:8080`. Override it through `ACR_ADDR` or the `serve -listen` flag.

## Process configuration

| Variable | Default | Purpose |
|---|---:|---|
| `ACR_ENVIRONMENT` | `development` | `development`, `test`, `staging`, or `production` |
| `ACR_ADDR` | `:8080` | HTTP listen address |
| `ACR_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `ACR_REQUEST_TIMEOUT` | `15s` | Per-request context deadline |
| `ACR_READ_HEADER_TIMEOUT` | `5s` | HTTP header timeout |
| `ACR_READ_TIMEOUT` | `20s` | HTTP read timeout |
| `ACR_WRITE_TIMEOUT` | `20s` | HTTP write timeout — must stay >= `ACR_REQUEST_TIMEOUT` + 5s (CHAOS-4330); rejected at startup otherwise |
| `ACR_IDLE_TIMEOUT` | `60s` | HTTP idle timeout |
| `ACR_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown budget |
| `ACR_MINIMUM_SIDECAR_VERSION` | `0.1.0` | Capabilities handshake floor |
| `ACR_ENTITLEMENT_KEY` | `agent_context_runtime` | Fixed product entitlement key; other values are rejected |
| `ACR_MAX_ITEMS` | `30` | Packet item limit advertised by capabilities |
| `ACR_MAX_OUTPUT_TOKENS` | `4000` | Output token budget advertised by capabilities |
| `ACR_MAX_SERIALIZED_BYTES` | `262144` | Serialized packet byte limit |
| `ACR_REQUESTS_PER_MINUTE` | `60` | Initial advertised request limit |
| `ACR_REQUIRE_BACKING_STORES` | environment dependent | Defaults true in staging/production |
| `ACR_CLICKHOUSE_DSN` / `ACR_CLICKHOUSE_DSN_FILE` | empty | Read-only Dev Health evidence store configuration |
| `ACR_CLICKHOUSE_CA_BUNDLE` | empty | Optional PEM CA bundle for ClickHouse TLS |
| `ACR_POSTGRES_DSN` / `ACR_POSTGRES_DSN_FILE` | empty | ACR operational store configuration |
| `ACR_POSTGRES_POOLER_ADMIN_DSN` / `ACR_POSTGRES_POOLER_ADMIN_DSN_FILE` | empty | Optional PgBouncer admin connection for transaction-pool validation |
| `ACR_POSTGRES_CONNECTION_KIND` | required for hosted | `direct` or `pgbouncer`; must not contradict `ACR_POSTGRES_POOLER_ADMIN_DSN` presence |
| `ACR_POSTGRES_MAX_OPEN_CONNS` | `12` | PostgreSQL pool open-connection limit |
| `ACR_POSTGRES_MAX_IDLE_CONNS` | `min(4, max-open)` | PostgreSQL pool idle-connection limit; explicit `0` disables idle connections |
| `ACR_POSTGRES_CONN_MAX_LIFETIME` | `30m` | PostgreSQL connection lifetime |
| `ACR_POSTGRES_CONN_MAX_IDLE_TIME` | `5m` | PostgreSQL idle connection lifetime |
| `ACR_POSTGRES_PING_TIMEOUT` | `5s` | PostgreSQL startup ping and readiness timeout |
| `ACR_ENABLE_EPISODE_WRITEBACK` | `false` | Explicitly enables the hosted episode service and route permission |
| `ACR_EVIDENCE_ID_ACTIVE_KID` / `ACR_EVIDENCE_ID_ACTIVE_KID_FILE` | empty | Active evidence-ID signing key identifier |
| `ACR_EVIDENCE_ID_KEYS` / `ACR_EVIDENCE_ID_KEYS_FILE` | empty | Comma-separated evidence-ID key material |
| `ACR_DEV_HEALTH_ENTITLEMENT_URL` | empty | Dev Health entitlement/health service HTTP(S) origin |
| `ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE` | empty | Path to the bearer token file used to authenticate to the entitlement service |
| `ACR_DEV_HEALTH_ENTITLEMENT_TIMEOUT` | `5s` | Per-request timeout for entitlement and health checks |
| `ACR_DEV_HEALTH_ENTITLEMENT_MAX_RESPONSE_BYTES` | `16384` | Maximum bytes read from an entitlement response body |
| `ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL` | empty | Optional explicit HTTP(S) proxy for entitlement requests; no credentials permitted |
| `ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE` | empty | Optional PEM CA bundle for entitlement service TLS verification |
| `ACR_TRUSTED_PROXY_CIDRS` | empty | Comma-separated proxy networks allowed to supply `X-Forwarded-For` |
| `ACR_WEB_ASSERTION_ISSUER` | empty | Fixed trusted-web assertion issuer; must be set with audience and JWKS file |
| `ACR_WEB_ASSERTION_AUDIENCE` | empty | Fixed trusted-web assertion audience; must be set with issuer and JWKS file |
| `ACR_WEB_ASSERTION_JWKS_FILE` | empty | Local public Ed25519 JWKS file; enables trusted-web read assertions |

DSN values are never emitted by `SafeAttributes` or startup logs. DSN presence
alone is not readiness: staging/production startup composes the complete hosted
runtime, verifies the PostgreSQL schema and privileges, executes a bounded
ClickHouse catalog probe, and checks the entitlement service before listening.
Hosted, credential-administration, and migration PostgreSQL/PgBouncer transports
follow their DSNs. Plaintext and certificate-verified TLS are both supported in
every environment. ClickHouse likewise follows its explicit DSN: native port
9000 is the ordinary private-service default, while `secure=true` plus an
optional CA bundle keeps certificate verification enabled when TLS is selected.

Entitlement provider selection is automatic. A complete
`ACR_DEV_HEALTH_ENTITLEMENT_URL` plus
`ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE` selects the remote provider. Omitting
both selects an offline allow-all provider only in `development` and `test`;
that provider still accepts only a non-empty organization and the fixed
`agent_context_runtime` key. Partial remote configuration and orphaned proxy,
CA options are rejected. `staging` and `production` always require a remote
HTTP(S) entitlement origin and preserve the fail-before-listen
health check. Remote readiness and request authorization remain fail closed
during a Dev Health outage; local entitlement performs no network operation.

Each listed secret pair accepts exactly one source. A `_FILE` source must name
a regular, non-symlink file that is not writable by group or others; its
trimmed contents are bounded to 64 KiB. Invalid paths, permissions, file sizes,
and conflicting direct/file sources fail configuration without reporting file
contents or paths. `acr-migrate` accepts the same source pair for
`ACR_POSTGRES_MIGRATION_DSN`; credential administration accepts it for
`ACR_POSTGRES_DSN`.

### Compose database password files

`deploy/compose/acr.compose.yml` requires these host-side password-file
variables: `ACR_POSTGRES_ADMIN_PASSWORD_FILE`,
`ACR_RUNTIME_DB_PASSWORD_FILE`, and `ACR_MIGRATION_DB_PASSWORD_FILE`. Point
each at an owner-only regular file with mode `0600`; do not use symlinks or
group-/other-writable files. Compose maps those paths to the corresponding
container `_FILE` variables, so Compose deployments use the file source.
Direct and `_FILE` sources are mutually exclusive; there is no fallback precedence.
The checked-in root-local Compose fragment enables backing stores but omits all
remote entitlement inputs, so `acr-api` can start independently of Dev Health
Ops restart order. Helm verification also renders an explicit remote URL, token,
and CA configuration to keep the production-style boundary covered.

### Hosted database operations

Hosted startup requires the embedded migration history to match its required
prefix; later additive migration suffixes are permitted so older and newer
binaries remain compatible during a rolling deployment. The hosted runtime also
performs an initial and periodic bounded packet purge. Its PostgreSQL role needs
`SELECT`, `DELETE`, `UPDATE`, and audit-event privileges for those operations.

## Existing routes

```http
GET /healthz
GET /readyz
GET /api/v1/agent-context/capabilities
POST /api/v1/agent-context/context-packets
GET /api/v1/agent-context/evidence/{evidence_ref_id}
POST /api/v1/agent-context/episodes
```

`/healthz` proves that the process is alive. `/readyz` runs named dependency
checks and exposes only safe ready/not-ready states; detailed failures are
structured-log events. The hosted read routes require an authenticated `fcacr_`
credential, the `agent_context_runtime` entitlement, and their applicable
read scope before they can return a retryable `503` for an unavailable hosted
runtime bundle.

When the three `ACR_WEB_ASSERTION_*` values are configured, read routes also
accept a compact `X-ACR-Web-Assertion` JWT. The assertion uses an `EdDSA`
signature from the local JWKS and binds its configured issuer/audience, `sub`,
server-derived `org_id`, explicit repository slugs, read permissions, short
timestamps, `jti`, method, path, and body digest. The process never logs the
assertion or its body. JWKS key removal takes effect on the next assertion
because the file is re-read for verification. Web assertions cannot authorize
credentials, administration, or episode writes. Duplicate `jti` attempts are
audited and rate-limited; they are not represented as impossible.

`POST /api/v1/agent-context/episodes` is protected by authentication, the
`agent_context_runtime` entitlement, and an `episode:write` scope authorized
for the target repository. Invalid or unentitled callers may receive `401` or
`403` before runtime availability is evaluated. A valid authorized caller
receives a retryable `503` while writeback is disabled; enabling
`ACR_ENABLE_EPISODE_WRITEBACK=true` makes the hosted episode service available.

## Request behavior

* A valid caller-provided `X-Request-ID` is propagated.
* Missing or overlong request IDs are replaced with a generated opaque value.
* Structured access logs contain operation, status, response bytes, duration, and request ID only.
* Authorization headers, API credentials, DSNs, and request bodies are not logged.
* Handler panics are recovered and returned as the versioned `error.v1` envelope.
* Capability, entitlement, and evidence dependency failures return safe versioned errors.
* SIGINT and SIGTERM trigger graceful HTTP shutdown within `ACR_SHUTDOWN_TIMEOUT`.

### Credential usage telemetry

Successful credential and trusted-web reads enqueue best-effort usage telemetry
without waiting for a `last_used_at` or audit-store write. The lifecycle-owned
single worker has a bounded 256-record queue (hard maximum 4096) and coalesces
records by actor and usage action. A credential batch writes only the newest
`last_used_at`, preserving the storage adapter's monotonic update rule, and
records the number of successful uses in one audit event.

Queue saturation drops the newest successful-use record and emits the
low-cardinality `credential usage telemetry dropped` warning with
`reason=queue_full`; `UsageTelemetryStats` exposes queue capacity, enqueued,
coalesced, dropped, delivery-failure, delivered, and shutdown-drop counters for
metrics export. A database outage affects only those counters and safe warnings,
never the authenticated response. A nil telemetry close result means only that
the worker is quiesced; it does not claim durable delivery. Queue saturation,
process crash, forced termination, or an unjoined shutdown timeout can lose
successful-use telemetry. An unjoined worker keeps PostgreSQL open and returns
an error so process termination reclaims the process instead of permitting a
post-close store call. During a joined shutdown the worker completes before the
PostgreSQL pool closes.

Known authorization denials make exactly one synchronous, detached,
deadline-bounded audit delivery attempt. An unavailable audit store emits the
safe `credential denial audit delivery failed` warning but cannot permit access,
change the denial response, retry the delivery, or claim that the denial audit
persisted.

## Dependency seams

`internal/api` defines readiness, capabilities, and the all-or-nothing
`RuntimeDependencies` boundary. `internal/storage` owns evidence, packet,
episode, credential, and audit stores. The API package does not choose database
drivers. See `docs/read-api.md` for production composition and authorization.
