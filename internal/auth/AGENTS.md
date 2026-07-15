# AUTHENTICATION AND AUTHORIZATION

## OVERVIEW

Owns ACR token lifecycle, repository-scope normalization, authentication middleware, client IP handling, and failed-attempt limiting. It constructs `storage.Principal` only from a validated credential.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Token shape/hash | `token.go` | `fcacr_` token generation, validation, hashing |
| Credential lifecycle | `service.go` | Create, rotate, revoke, safe audit metadata |
| Repository scopes | `repository.go` | Exact `owner/repo`, `owner/*`, explicit `*` |
| HTTP authentication | `middleware.go` | Bearer lookup, revocation/expiry, Principal context |
| Attempt limiting | `limiter.go` | Per-client failure controls and retry timing |
| Client address | `client_ip.go` | Trusted proxy handling and safe fallback |
| Scope constants/errors | `types.go` | Read defaults and opt-in write scope |

## INVARIANTS

- Build `Principal` from credential metadata only; never copy org, repository, permissions, or entitlement from a request payload.
- Default normalized permissions are `context:read` and `evidence:read`; `episode:write` is explicit opt-in.
- Repository scope is required, lowercased, deduplicated, sorted, and restricted to exact, owner wildcard, or explicit global wildcard forms.
- Authentication failures for missing, malformed, unknown, revoked, and expired credentials share a safe external response.
- Store only token hashes. Return plaintext only at creation/rotation and never place it in logs or audit metadata.
- Scope denial and repository denial remain separate authorization decisions and audit actions.
- Successful-read usage telemetry is lifecycle-owned, bounded, coalesced, and asynchronous; queue saturation and crash-before-flush loss are observable best-effort outcomes. Known authorization denials make one synchronous detached bounded audit attempt and remain denied if it fails. Credential lifecycle audit in `service.go` stays atomic with its mutation.

## TESTING

- `token_test.go` locks generation, shape, and hashing.
- `repository_test.go` covers normalization, wildcards, and path-like attacks.
- Middleware tests cover indistinguishable failures, Principal context, permission/repository denial, rate limits, and audit safety.
- `service_test.go` covers defaults, rotation overlap, revocation, and one-time secret handling.
- Run `go test ./internal/auth`; security changes should also run `go test -race ./internal/auth`.

## ANTI-PATTERNS

- Do not accept license artifacts or generic bearer strings as ACR tokens.
- Do not expose lookup failure reasons or full token prefixes beyond the public metadata contract.
- Do not make repository wildcard scope implicit; `*` must be explicit.
- Do not skip attempt limiting before expensive credential lookup.
