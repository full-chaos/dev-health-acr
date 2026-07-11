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
| `ACR_ENTITLEMENT_KEY` | `agent_context_runtime` | Product entitlement key |
| `ACR_MAX_ITEMS` | `30` | Packet item limit advertised by capabilities |
| `ACR_MAX_OUTPUT_TOKENS` | `4000` | Output token budget advertised by capabilities |
| `ACR_MAX_SERIALIZED_BYTES` | `262144` | Serialized packet byte limit |
| `ACR_REQUESTS_PER_MINUTE` | `60` | Initial advertised request limit |
| `ACR_REQUIRE_BACKING_STORES` | environment dependent | Defaults true in staging/production |
| `ACR_CLICKHOUSE_DSN` | empty | Read-only Dev Health evidence store configuration |
| `ACR_POSTGRES_DSN` | empty | ACR operational store configuration |
| `ACR_TRUSTED_PROXY_CIDRS` | empty | Comma-separated proxy networks allowed to supply `X-Forwarded-For` |

DSN values are never emitted by `SafeAttributes` or startup logs. DSN presence
alone is not readiness: staging/production startup fails until the hosting build
links concrete credential, audit, entitlement, and evidence adapters.

## Existing routes

```http
GET /healthz
GET /readyz
GET /api/v1/agent-context/capabilities
POST /api/v1/agent-context/context-packets
GET /api/v1/agent-context/evidence/{evidence_ref_id}
```

`/healthz` proves that the process is alive. `/readyz` runs named dependency
checks and exposes only safe ready/not-ready states; detailed failures are
structured-log events. All three read routes require an `fcacr_` credential and
return a retryable `503` when the hosted runtime bundle is unavailable.

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
