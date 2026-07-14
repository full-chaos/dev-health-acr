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
| `ACR_WRITE_TIMEOUT` | `20s` | HTTP write timeout |
| `ACR_IDLE_TIMEOUT` | `60s` | HTTP idle timeout |
| `ACR_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown budget |
| `ACR_MINIMUM_SIDECAR_VERSION` | `0.1.0` | Capabilities handshake floor |
| `ACR_ENTITLEMENT_KEY` | `agent_context_runtime` | Fixed product entitlement key; other values are rejected |
| `ACR_MAX_ITEMS` | `30` | Packet item limit advertised by capabilities |
| `ACR_MAX_OUTPUT_TOKENS` | `4000` | Output token budget advertised by capabilities |
| `ACR_MAX_SERIALIZED_BYTES` | `262144` | Serialized packet byte limit |
| `ACR_REQUESTS_PER_MINUTE` | `60` | Initial advertised request limit |
| `ACR_REQUIRE_BACKING_STORES` | environment dependent | Defaults true in staging/production |
| `ACR_CLICKHOUSE_DSN` | empty | Read-only Dev Health evidence store configuration |
| `ACR_CLICKHOUSE_CA_BUNDLE` | empty | Optional PEM CA bundle for ClickHouse TLS |
| `ACR_POSTGRES_DSN` | empty | ACR operational store configuration |
| `ACR_POSTGRES_POOLER_ADMIN_DSN` | empty | Optional PgBouncer admin connection for transaction-pool validation |
| `ACR_POSTGRES_CONNECTION_KIND` | required for hosted | `direct` or `pgbouncer`; must not contradict `ACR_POSTGRES_POOLER_ADMIN_DSN` presence |
| `ACR_POSTGRES_MAX_OPEN_CONNS` | `12` | PostgreSQL pool open-connection limit |
| `ACR_POSTGRES_MAX_IDLE_CONNS` | `min(4, max-open)` | PostgreSQL pool idle-connection limit; explicit `0` disables idle connections |
| `ACR_POSTGRES_CONN_MAX_LIFETIME` | `30m` | PostgreSQL connection lifetime |
| `ACR_POSTGRES_CONN_MAX_IDLE_TIME` | `5m` | PostgreSQL idle connection lifetime |
| `ACR_POSTGRES_PING_TIMEOUT` | `5s` | PostgreSQL startup ping and readiness timeout |
| `ACR_ALLOW_INSECURE_POSTGRES` | `false` | Test-environment-only override for disposable plaintext PostgreSQL fixtures |
| `ACR_ENABLE_EPISODE_WRITEBACK` | `false` | Explicitly enables the hosted episode service and route permission |
| `ACR_DEV_HEALTH_ENTITLEMENT_URL` | empty | Dev Health entitlement/health service origin (HTTPS required outside loopback) |
| `ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE` | empty | Path to the bearer token file used to authenticate to the entitlement service |
| `ACR_DEV_HEALTH_ENTITLEMENT_TIMEOUT` | `5s` | Per-request timeout for entitlement and health checks |
| `ACR_DEV_HEALTH_ENTITLEMENT_MAX_RESPONSE_BYTES` | `16384` | Maximum bytes read from an entitlement response body |
| `ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL` | empty | Optional explicit HTTP(S) proxy for entitlement requests; no credentials permitted |
| `ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE` | empty | Optional PEM CA bundle for entitlement service TLS verification |
| `ACR_DEV_HEALTH_ENTITLEMENT_ALLOW_INSECURE_LOOPBACK` | `false` | Allows plaintext HTTP only when the entitlement origin is loopback (local development) |
| `ACR_TRUSTED_PROXY_CIDRS` | empty | Comma-separated proxy networks allowed to supply `X-Forwarded-For` |

DSN values are never emitted by `SafeAttributes` or startup logs. DSN presence
alone is not readiness: staging/production startup composes the complete hosted
runtime, verifies the PostgreSQL schema and privileges, executes a bounded
ClickHouse catalog probe, and checks the entitlement service before listening.
Hosted, credential-administration, and migration PostgreSQL/PgBouncer network
DSNs require certificate-verified TLS; Unix sockets are accepted without TLS.
The plaintext override is rejected outside `ACR_ENVIRONMENT=test`.

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

## Dependency seams

`internal/api` defines readiness, capabilities, and the all-or-nothing
`RuntimeDependencies` boundary. `internal/storage` owns evidence, packet,
episode, credential, and audit stores. The API package does not choose database
drivers. See `docs/read-api.md` for production composition and authorization.
