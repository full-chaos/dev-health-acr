# CHAOS-2907 hosted read API evidence

## Implemented boundary

- `GET /api/v1/agent-context/capabilities` authenticates `fcacr_` credentials,
  requires `context:read`, applies auth-class quotas, resolves the independent
  organization entitlement, derives permissions from the authenticated
  principal, and enforces the optional client-version floor.
- `POST /api/v1/agent-context/context-packets` strictly decodes one bounded JSON
  value, rejects unknown fields, validates contract and configured limits,
  discards caller repository metadata, authorizes the normalized repository,
  checks entitlement, calls the real assembler, completes usage, audits, and
  writes the bounded packet only after all checks succeed.
- `GET /api/v1/agent-context/evidence/{evidence_ref_id}` treats the ID as opaque,
  checks evidence scope and entitlement, relies on independent store-level
  organization/repository authorization, collapses malformed/unknown/foreign/
  deleted/unauthorized references to one generic `404`, completes usage, and
  writes only bounded sanitized expansion output.
- Packet replay and episode routes are not registered.
- The runtime dependency bundle is all-or-nothing. Development without adapters
  exposes safe retryable `503` read stubs and not-ready status. A production
  configuration refuses startup until a hosting build links concrete adapters.

## Security behavior

- Organization identity always comes from the authenticated credential.
- Product entitlement is not inferred from credential scopes or environment
  flags.
- Capabilities providers receive a request clone without the bearer header or
  body.
- Request bodies, bearer values, evidence handles, goals, excerpts, dependency
  errors, and repository candidates are absent from logs and safe errors.
- Detached usage/audit operations have a one-second context bound.
- Forwarded client IPs are accepted only from explicitly configured trusted
  proxy CIDRs, with the nearest untrusted hop selected for auth limiting.
- Keyed repository-routing tags reduce evidence resolution from a potential
  64-repository fan-out to zero reference queries for unroutable handles and one
  for valid routes before full MAC verification.
- Authenticated responses set `Cache-Control: no-store` and
  `X-Content-Type-Options: nosniff`.
- Runtime bundles must supply credential, entitlement, and evidence connectivity
  checks; `/readyz` executes and reports each without dependency details.
- Context responses are re-checked against the caller's requested item, token,
  and serialized-byte budgets even when the assembler is injected.
- Client compatibility follows SemVer precedence, including prerelease and build
  metadata rules.
- Trusted web assertions remain disabled until their verifier contract exists;
  license keys are never bearer credentials.

## Verification

- `go test -race -shuffle=on -count=1 ./...`
- `make contract-test`
- `make verify`
- `git diff --check`
- LSP diagnostics: zero errors in changed Go packages.
- Changed Go files: no file exceeds 250 pure LOC.
- Built-binary QA: health returned `200`; readiness named
  `runtime_dependencies` as not-ready; capabilities and context returned safe
  retryable `503` envelopes when runtime adapters were absent.
- Injected-runtime QA: authenticated capabilities, fixture-backed context packet
  generation, evidence expansion, auth/scope/entitlement/repository denials,
  incompatible versions, strict body bounds, rate limiting, dependency failure,
  and timeout behavior all passed through the HTTP router.
- Adapter-path QA: the HTTP router generated a packet and expanded its signed
  evidence using the real PostgreSQL credential/audit adapters and real
  ClickHouse catalog/evidence adapters over deterministic database fixtures.
- Final security review and Oracle acceptance review: PASS, zero warnings.
