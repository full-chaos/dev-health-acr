# HOSTED API PACKAGE

## OVERVIEW

HTTP boundary for health/readiness and protected ACR reads. The package composes injected adapters, authentication, limits, entitlement, audit, safe errors, and request observability; it does not select storage drivers.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Route/middleware composition | `app.go` | `Handler` and `InstrumentedHandler` |
| Runtime bundle | `runtime.go` | All-or-nothing credentials, audit, entitlement, assembler, evidence |
| Read handlers | `read_routes.go`, `read_capabilities.go` | Capabilities, context packets, evidence |
| Decode/encode limits | `read_decode.go` | Strict JSON, body and response bounds |
| Error envelope | `response.go` | Fixed codes/messages and request IDs |
| Rate/resource limits | `limits_middleware.go` | Claim before work, complete with actual usage |
| Server lifecycle | `server.go` | Timeouts and graceful shutdown |
| Integration seams | `*_integration_test.go`, `seeded_flow_test.go` | MCP bootstrap and real adapters |

## REQUEST INVARIANTS

- Preserve middleware order in `InstrumentedHandler`: request ID, access observation/logging, timeout, recovery must continue to produce safe correlated responses.
- Protected routes use `protectedRuntimeHandler`; authentication, required scope, request limits, and optional entitlement are independent gates.
- `RuntimeDependencies` is complete or absent. Never partially enable protected routes.
- `Principal` comes from authentication. Normalize and authorize repository scope independently of request fields.
- Dependency failures fail closed with versioned safe responses; never expose raw adapter errors.
- Audit writes are synchronous with a detached one-second context; they may delay the response within that bound, but persistence failure cannot rewrite the primary result.
- Request IDs are correlation fields in responses/logs, not metric labels.

## ADAPTER BOUNDARY

- API code consumes interfaces from `internal/storage`, `internal/limits`, and the assembler boundary.
- Database construction, driver choice, DSN parsing, and migrations belong to hosting composition or adapter packages.
- Development may be healthy but intentionally not ready; protected reads return safe 503s without a runtime bundle.

## TESTING

- `read_test_helpers_test.go` and assertion helpers provide the standard app fixtures.
- Route tests must cover denial class, retryability, request ID, and safe body shape.
- Integration tests lock API↔assembler, API↔MCP bootstrap, and storage adapter behavior.
- `openapi_compat_test.go` covers route/contract parity; run `make contract-test` for endpoint changes.
- Run `go test ./internal/api` plus `make verify` for middleware/runtime changes.

## ANTI-PATTERNS

- Do not bypass `protectedRuntimeHandler` for protected reads.
- Do not construct a partial runtime bundle or silently skip readiness checks.
- Do not log authorization headers, request bodies, repository content, or raw dependency errors.
- Do not choose memory/Postgres/ClickHouse drivers inside handlers.
