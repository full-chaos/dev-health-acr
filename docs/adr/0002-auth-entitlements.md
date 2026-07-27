# ADR-0002: Separate authentication credentials from product entitlement

**Status:** Accepted  
**Date:** 2026-07-10

## Decision

The local MCP sidecar authenticates with a dedicated ACR client credential. It does not use a Dev Health commercial license key as an API bearer credential.

The hosted API enforces both:

1. **Authentication and credential permissions**
   - `context:read`
   - `evidence:read`
   - `episode:write`
2. **Organization product entitlement**
   - `agent_context_runtime`

The capability handshake reports entitlement separately from the current credential permissions. Read tools are enabled by default. Episode writeback requires explicit local enablement, `episode:write`, and the product entitlement.

## Credential requirements

- Organization scoped.
- Repository allowlist or explicit all-repositories grant.
- Expiration, rotation, revocation, last-used timestamp, and audit history.
- Secret shown only once at creation; only a secure hash is stored.
- Never accepted in query parameters.
- Never logged.

## Web authentication

`dev-health-web` uses its existing user session and a server-side exchange/proxy to call ACR. ACR validates the resulting Dev Health user token or trusted service assertion and independently enforces organization/repository authorization.

Trusted web assertions have a separate permission vocabulary from client credentials. In addition to the read permissions, a short-lived, request-bound assertion may carry `credential:issue` only to authorize an exact device-approval request after the web application has derived the user's bounded organization and repository grant from its authenticated session. `credential:issue` is never a client-credential scope, cannot authorize arbitrary credential administration, and never carries or returns an `fcacr_` secret. Device-authorization record compare-and-set transitions, rather than the per-process assertion replay cache, are the authority for approval idempotency.

## No auth-service spinout

SVS reuses existing Dev Health identity and entitlement primitives. A dedicated auth service may be considered only after multiple API products duplicate credential lifecycle and policy enforcement.
