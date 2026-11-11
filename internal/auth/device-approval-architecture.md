# ACR device approval: diagram source

The public page [docs/device-approval.md](../../docs/device-approval.md) copies the two diagrams and the
first columns of the token, key and setting tables below. This file is the source: change both together
(a test compares them). The web repository holds the same diagrams and rows in
`src/lib/acr/device-approval-architecture.md`; the two copies are byte-equal below this paragraph. Facts are
pinned to web `dea85d0ae0d3`, acr `3c48c78b9b8f`, ops `cfeabc7f4cd1` and deploy `2e54677e3f81` (2026-11-11).
Examples hold names only, never a key, token or code value.

Entry points here: `internal/auth/web_assertion.go` (the verifier of the web assertion),
`internal/api/device_routes.go` (the device approval handler), `internal/auth/device_flow.go`
(the approval checks).

## Sequence

```mermaid
sequenceDiagram
    autonumber
    participant D as Device (CLI or MCP client)
    participant A as ACR (acr-api)
    participant B as User browser
    participant W as Web server
    participant P as Auth service (PDP)
    participant O as Ops API

    D->>A: POST /api/v1/oauth/device_authorization
    A-->>D: device_code, user_code, verification_uri
    D->>B: user opens verification_uri (/acr/device) and types the user code
    B->>W: GET /acr/device (web session cookie)
    B->>W: POST /api/acr/device (preview, then approve: steps 6 to 14 run once for each)
    W->>O: GET impersonation status (user access token as Bearer)
    opt ACR_SCOPES_FROM_GRANTS on
        W->>P: exchange the service-account token for a workload token (cached)
        W->>P: POST /v1/authorize acr.credential.issue (workload token as Bearer, user access token as principal_reference)
        P-->>W: allow with repository scope, or deny
    end
    W->>O: GET entitlement (user access token as Bearer)
    W->>A: POST /api/v1/oauth/device_approval (X-ACR-Web-Assertion)
    A->>O: GET organization state (ACR credential, never cached)
    Note over A: verifies the assertion, then its own approval checks (no PDP call)
    A-->>W: approved
    W-->>B: result
    D->>A: POST /api/v1/oauth/token (device_code), polled
    A-->>D: device credential (opaque, fcacr_ prefix)
```

## Components and tokens

```mermaid
flowchart LR
    B[User browser]
    W[Web server]
    P[Auth service: PDP and token issuer]
    O[Ops API]
    A[ACR acr-api]
    D[Device client]
    WK[(web assertion key file)]
    AK[(ACR web key set file)]
    PK[(auth service key set)]

    B -- "web session cookie" --> W
    W -- "user access token as Bearer (status and entitlement reads)" --> O
    W -- "service-account token, exchanged for a workload token" --> P
    W -- "workload token as Bearer + user access token as principal_reference" --> P
    W -- "web assertion, 30 s (each of preview and approve)" --> A
    A -- "ACR credential (service token or workload token)" --> O
    D -- "device_code and user_code (opaque)" --> A
    A -- "device credential, 30 days (opaque)" --> D
    WK -. "private key, read per request" .-> W
    AK -. "public keys by kid, with retire_after" .-> A
    PK -. "public keys that verify the user access token" .-> P
```

## Token rows

| Token                                                      | Signer                                                       | Verifier                                                                  | Audience                                                  | Lifetime                                                                          | Source                                                                                                                                                                                                                                                   |
| ---------------------------------------------------------- | ------------------------------------------------------------ | ------------------------------------------------------------------------- | --------------------------------------------------------- | --------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Web session cookie (sealed Auth.js session)                | web, sealed with `AUTH_SECRET` or `NEXTAUTH_SECRET`          | web                                                                       | none                                                      | no `maxAge` is set in the web auth code, so the Auth.js default applies (30 days) | web `src/lib/auth.ts:86-87` (secret); `src/lib/acr/ops.ts:209` ("sealed session"); no session `maxAge` in `src/lib/auth.ts` (only the handoff and SSO ticket seals set one, :717,:738)                                                                   |
| Refresh credential (`dhr1_` prefix, opaque, single use)    | auth service                                                 | auth service                                                              | none                                                      | 7 days                                                                            | ops `internal/auth/session/credential.go:18-19`; `internal/auth/session/store.go:55`; web `src/lib/auth.ts:249-258` (held in the sealed session, rotated on refresh, renews the user access token; never sent to the PDP, Ops reads or ACR on this path) |
| User access token (EdDSA, `at+jwt`)                        | auth service                                                 | PDP (as `principal_reference`) and Ops API (status and entitlement reads) | `dev-health-api`                                          | at most 60 minutes (setting: 5 to 60)                                             | ops `internal/auth/authapi/issuer_eddsa.go:24`, `authorize_reference.go:91-131`, `contracts/access_token.go:14,18`, `authconfig/config.go:108-110`; web `src/lib/acr/ops.ts:128,171,241`, `src/lib/acr/grantScopes.ts:79,174`                            |
| Service-account token (Kubernetes, projected into the pod) | Kubernetes cluster                                           | auth service, by a TokenReview at the exchange route                      | set by the deployment                                     | set by the deployment                                                             | web `src/lib/auth/workload/token.ts:6-10`; ops `internal/auth/authconfig/workload.go:30-42`                                                                                                                                                              |
| Workload token (`workload+jwt`)                            | auth service, by exchange of the service-account token       | PDP route `/v1/authorize`, for admitted callers only                      | `auth-service`                                            | at most 300 s, cached and renewed at half life                                    | web `src/lib/auth/workload/token.ts:11-14,110,171`, `src/lib/auth/authorize/transport.ts:24`; ops `internal/auth/contracts/workload_token.go:13`, `internal/auth/authruntime/authorize.go:33-34`                                                         |
| Web assertion (EdDSA, `JWT`, with `kid`)                   | web, one Ed25519 key file                                    | ACR                                                                       | `ACR_WEB_ASSERTION_AUDIENCE` (ACR compares)               | 30 s, with 5 s clock skew                                                         | web `src/lib/acr/assertion.ts:52-60`; acr `internal/auth/web_assertion.go:23-24,158,209-217`                                                                                                                                                             |
| ACR credential for Ops (service token, or workload token)  | Ops (service token, opaque) or auth service (workload token) | Ops API                                                                   | none (service token) or `dev-health-api` (workload token) | set by Ops (service token) or at most 300 s (workload token)                      | acr `docs/acr-api-workload-token.md:11-26`; `internal/entitlements/client.go:256-275,304-307`; `internal/api/organization_presence.go:47-56`; ops `internal/auth/contracts/workload_token.go:16`                                                         |
| Device code and user code                                  | ACR (opaque, not signed)                                     | ACR (stored as hashes)                                                    | none                                                      | 10 minutes                                                                        | acr `internal/storage/device_authorization.go:18`, `internal/auth/device_poll.go:109`; web `src/app/acr/device/page.tsx` (8-character user code pattern)                                                                                                 |
| Device credential (`fcacr_` prefix)                        | ACR (opaque, not signed)                                     | ACR on later calls                                                        | none                                                      | 30 days                                                                           | acr `internal/auth/types.go:23`, `internal/auth/device_flow.go:19`, `internal/auth/device_poll.go:174`                                                                                                                                                   |

## Key rows

| Key                                                                                                                    | Held by                                                      | Used for                                                                           | Source                                                                              |
| ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ | ---------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Web assertion signing key (Ed25519 private key file; `ACR_WEB_ASSERTION_KEY_FILE`, `kid` from `ACR_WEB_ASSERTION_KID`) | web                                                          | signs the web assertion; read on every request                                     | web `src/lib/acr/config.ts:104-105`, `src/lib/acr/assertion.ts:52-60`               |
| ACR web key set (JWKS file; `ACR_WEB_ASSERTION_JWKS_FILE`; each key has a `kid` and may have `retire_after`)           | ACR                                                          | verifies the web assertion; a key verifies only while now is before `retire_after` | acr `internal/config/web_assertion.go:25`; `internal/auth/web_assertion.go:184-203` |
| Auth service key set (published)                                                                                       | auth service signs; the PDP verifies with the published keys | user access token at the PDP (`principal_reference`)                               | ops `internal/auth/authapi/authorize_reference.go:73-74`                            |

## Setting rows

| Setting                                 | Where | Default         | Production on 2026-11-11 | Effect                                                                                                                      | Source                                                                                                                    |
| --------------------------------------- | ----- | --------------- | ------------------------ | --------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `ACR_SCOPES_FROM_GRANTS`                | web   | off             | on (`true`)              | with it on, the PDP decides the approval (the approver decision)                                                                        | web `src/lib/acr/grantScopes.ts:55-58`; production: deploy repository main `2e54677e3f81`, `values.prod.yaml:1726-1727`   |
| `ACR_OAUTH_ISSUER`                      | web   | unset           | unset                    | set: `sid` and `impersonating` are signed into the assertion and the first-party browser OAuth client is on; unset: neither | web `src/lib/acr/config.ts:160-161`; production: `values.prod.yaml` has no `ACR_OAUTH_ISSUER` (0 hits) at the same commit |
| `ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL` | ACR   | `service_token` | `workload`               | the credential ACR presents to Ops for the organization state read                                                          | acr `docs/acr-api-workload-token.md:11-26`; production: deploy repository main `2e54677e3f81`, `values.acr-prod.yaml:113` |

## Order of the calls (web, `ACR_SCOPES_FROM_GRANTS` on)

The Ops impersonation status read (`src/lib/acr/service.ts:206-218`, `src/lib/acr/ops.ts:241`), the
impersonation refusal (`service.ts:225-238`), the PDP decision (`service.ts:292`, `ops.ts:384`), the
entitlement read (`service.ts:255`, `ops.ts:196-199`), the assertion, the call to ACR. With the switch
off the PDP decision is skipped (`service.ts:276-300`).

## History (ids and dates; the public page has dates and words only)

- The web assertion: acr #24 (merged 2026-07-15) and web #785 (merged 2026-07-16), under CHAOS-2911.
- Device approval on it: web #806 (merged 2026-07-26, CHAOS-3096) and acr #52 (merged 2026-07-27).
- OAuth consent on it: acr #638 (merged 2026-09-22, CHAOS-6226); the consent approval is a web call
  with the assertion (acr `internal/auth/device_flow.go:274-278`).
- The Wave 9 epic (CHAOS-3282, created 2026-07-30) keeps the request-bound assertion for web
  context, evidence and approval calls.
- Replacement path: the browser calls ACR itself with an OAuth 2.1 token bound to user and session
  (CHAOS-12069 ruling D6525; CHAOS-12082 train). Planned, not built on 2026-11-11.
- The first-party browser OAuth client: web `src/lib/acr/config.ts:143-164`; acr browser-class
  credential `internal/auth/device_flow.go:20-22`. Off in production while web sets no
  `ACR_OAUTH_ISSUER`.
