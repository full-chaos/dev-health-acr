# CHAOS-2927 integration evidence

## Baseline

- `make test` passed before integration changes.

## Integration behavior

- `internal/api/limits_middleware_test.go` drives an authenticated HTTP handler through an allowed request and a denied request. The denied response is `429`, has `error.code=rate_limited`, retains the canonical request ID in its header and error envelope, and provides `Retry-After: 60` without exposing organization, credential, policy, or secret data.
- `internal/api/observability_test.go` drives the API capabilities HTTP route with a canonical caller request ID and proves the recorded request observation uses that same ID.
- Invalid caller request IDs are replaced before request logging and telemetry correlation.
- Invalid injected request-ID generators are also replaced with canonical `req_` IDs. Recovered panics now complete the access-observation path as correlated failed requests.
- Explicit denial classification is now sourced from the component that knows the reason (auth middleware and the limiter's rate-limit mapper) via a structural `SetDenialCode(string)` marker on the access-log response writer, then converted to a closed `observability.DenialClass` at the observation boundary. No package introduces a new inter-package import cycle: `internal/auth` remains free of any `internal/observability` dependency.
- `App.InstrumentedHandler` factors the recovery/timeout/access-log/request-ID pipeline so protected routes (auth + limits) get the same panic-safe, correlated, and observed handling as the built-in routes. `App.ProtectedHandler` and `App.AuthenticatedHandler` compose per-principal limiting and authentication on top of it without inventing new public HTTP routes.
- `TestInstrumentedHandlerClassifiesScopeRepositoryAndRateLimitDenials` proves scope-denied, repository-denied, allowed, and rate-limited requests each produce the correct `observability.DenialClass` end-to-end through the full instrumented pipeline.
- `internal/config/limits.go` exposes `Config.LimitOptions()` translating env-driven per-request-class windows/limits/resource budgets into `limits.Options`; the default maximum retry-after is now derived from the largest configured window so a quota denial's `Retry-After` is never silently truncated below the real reset time.
- `cmd/acr-api/main.go` wires the limiter manager and a slog-backed operational sink into the App via dependency injection; no route yet requires authentication, so `AuthAttempts`/`AuthenticatedHandler` are validated by dedicated API tests pending the first protected route.
- Existing context-packet assembler coverage verifies complete, partial, degraded, and empty packet outcomes. Existing auth coverage verifies authentication, scope, repository, and rate-limit denial classes, plus a bounded in-memory attempt limiter that reclaims expired windows under a tracked-key cap.
- `TestSeededProtectedFlowCorrelatesRealPacketPipeline` drives one canonical request ID through authentication, context-class admission, the real evaluation evidence store and assembler/ranker, exact item/token/byte completion, and the terminal response observation.
- The production evidence-store factory injects packet-assembly and evidence-expansion observers, rather than relying on test-only construction.
- Each concrete ClickHouse catalog query emits a bounded terminal observation, including individual failures and timeouts.
- Request, store-query, ranking, and final-assembly spans originate from the real packet assembler and correlate with request snapshots.
- Packet compatibility is derived from the client sidecar version and assembled schema version.
- Real episode create/redact service terminals synchronously emit episode observations, while only actual episode-store calls emit independent store duration/outcome observations; both boundaries isolate observer panics.
- Authentication 429 responses include a bounded `Retry-After` hint from the real IP-keyed limiter.
- No packet/evidence/snapshot/episode/debug product route was added. A real seeded HTTP packet flow remains explicitly blocked on CHAOS-2907.

## Verification

- `make fmt` passed.
- `go test -race -shuffle=on -count=1 ./...` passed.
- `make contract-test` passed.
- `make verify` passed (vet, tests, contracts, and all production binaries build).
- LSP diagnostics were clean for every changed Go file.

## Manual HTTP QA

- The built `acr-api` was launched on a loopback port and `GET /healthz` returned `200` with service/version JSON.
- `GET /api/v1/agent-context/capabilities` with a secret-shaped caller request ID returned `200` and replaced it with canonical lowercase `req_` plus 32 hex characters.
- Structured request and observability records shared the replacement ID and contained neither the rejected caller value nor raw request paths/errors.
