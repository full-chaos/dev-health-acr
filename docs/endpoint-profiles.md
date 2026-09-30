# ACR endpoint auth profiles (CHAOS-3273 Wave 0)

Companion to [`authentication.md`](authentication.md) (client credential
shape, scopes, repository authorization, sidecar loading, rate limiting)
and [`adr/0002-auth-entitlements.md`](adr/0002-auth-entitlements.md) (why
credentials and entitlement are separate concerns, and why there is no
auth-service spinout). Extends both rather than duplicating them: read
`authentication.md` first. This doc is the per-surface inventory Guardrail
G-1 requires ("a route without a registered profile fails CI and may not
ship").

- Machine-readable inventory: [`../contracts/auth/v1/endpoint-profiles.acr.json`](../contracts/auth/v1/endpoint-profiles.acr.json)
- Discovery script: [`../ci/discover_acr_routes.go`](../ci/discover_acr_routes.go) (`go run ci/discover_acr_routes.go`)
- Shared schema (owned by ops, reused as-is): `contracts/auth/v1/endpoint-profile.schema.json`
- Closed credential-class vocabulary (owned by ops): `contracts/auth/v1/credential-classes.json`

## Coverage

21 route registrations in `internal/api/app.go`. Two are unauthenticated
probes (`/healthz`, `/readyz`); two are unauthenticated-by-design device-flow
entry points (`device_authorization`, `token`); four are the
unauthenticated-by-design OAuth authorization-server routes hosted MCP clients
log in through (`/.well-known/oauth-authorization-server`, `/authorize`,
`/token`, `/register`, registered only when OAuth is configured; see
`authentication.md`); the remaining 13 are behind
`Authenticator.MiddlewareFor` in one of its two shapes (including the OAuth
consent route `/authorize/consent`, web assertion only, like
`device_approval`), or the bearer-only `selfLifecycleHandler`.

## The single-dispatch guarantee (the CHAOS-3271-class answer for acr)

Unlike ops (separate `OrgIdMiddleware`/`ImpersonationMiddleware` layers
that can each independently observe a credential) and web (`proxy.ts`
running ahead of route.ts's own check), acr's `Authenticator.MiddlewareFor`
(`internal/auth/middleware.go:93-160`) is **one function that dispatches on
header presence**, not a chain multiple validators can each partially see:

1. `len(r.Header.Values(WebAssertionHeader)) > 0` → `authenticateWebAssertion`,
   which itself rejects if `Authorization` is *also* present
   (`internal/auth/web_assertion_middleware.go:11-16` — this is the
   orchestrator's cited positive pattern).
2. Otherwise → `extractBearer` + credential-store lookup by hash.

So for every route accepting both credential shapes, **there is exactly
one validator that can observe the credential** — `reachable_validators`
is non-empty only where this doc records the *duplicate, same-owner* check
in `deviceApprovalHandler` (see the JSON row), not a genuinely different
validator. This is the strongest posture of the three repos in this
inventory; nothing else to flag here as a cross-cutting finding.

One real asymmetry, not a defect: `POST /api/v1/agent-context/episodes`,
`PUT`/`DELETE /api/v1/context-fabric/model-config` set
`allowWebAssertions=false` — mutating episode writes and org model-config
writes require a bearer `fcacr_` credential; web's `AcrRuntimeClient` never
issues a `credential:issue`-shaped assertion for those permissions, so this
is confirmed-consistent with web's actual call sites, not a gap.

## `authverify` anchor — the `acr_workload_identity_exchange` subject-token validator

L0's `credential-classes.json` left this class with a stated gap: *"The
subject-token (k8s SA JWT) validator lives in
github.com/full-chaos/dev-health-go/authverify, a fourth repo outside this
lane's assigned read set."* CHAOS-3273's scope was widened by ruling to
include `dev-health-go`, READ-ONLY. This section closes that gap.

**Endpoint:** `POST /api/v1/oauth/token` (`internal/api/app.go:87`) —
`handleDeviceToken` (`internal/api/device_routes.go:69-79`) dispatches on
`Content-Type: application/x-www-form-urlencoded` to
`handleTokenExchange` (`internal/api/token_exchange_routes.go:27-81`), the
RFC 8693 grant.

**Pinned anchor: `acr`'s consumer pin is `dev-health-go v0.5.5`**
(`go.mod:5`). The dev-health-go checkout's working tree is neither
consumer's pin — anchored via
`git -C /Users/chris/projects/full-chaos/dev-health-go show v0.5.5:authverify/k8s_token_review.go`
(and `authverify/workload_exchange.go`), per the lane brief's pinned-tag
rule.

**What validates the subject token:**
`authverify.KubernetesTokenReviewValidator.Validate` (`authverify/k8s_token_review.go`
@ v0.5.5, function starts at the `func (v *KubernetesTokenReviewValidator) Validate`
declaration) validates **solely via a live call to the Kubernetes
TokenReview API** (`POST {APIServerURL}/apis/authentication.k8s.io/v1/tokenreviews`)
— it never decodes or trusts the subject JWT's own claims for
authentication. Specifically:

- **Issuer**: implicit — the Kubernetes API server itself is the authority;
  there is no separate issuer string check because TokenReview *is* the
  issuer's own live validation (including revocation, which a local JWT
  signature check could never see — see the type's own doc comment).
- **Audience**: `ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE` (acr env var, read in
  `internal/runtime/hosted/workload_token_exchange.go:64`), sent as
  `spec.audiences` in the TokenReview request and re-checked against
  `status.audiences` in the response
  (`slices.Contains(review.Status.Audiences, v.audience)`) — belt-and-braces:
  the API server is asked to confirm the audience, and the client re-checks
  the confirmation.
- **Namespace / ServiceAccount binding**: parsed from TokenReview's
  `status.user.username`, which Kubernetes always shapes as
  `system:serviceaccount:<namespace>:<name>`
  (`parseServiceAccountUsername`); `status.user.uid` is also required
  non-empty. Both come from the API server's response, never the request.
- **TrustDomain**: `ACR_WORKLOAD_TRUST_DOMAIN` (acr env var) — **NOT**
  derived from the token or the TokenReview response at all. A plain k8s
  ServiceAccount token carries no trust-domain claim (unlike full SPIFFE
  federation); this is a static, deployment-configured identifier for
  "this one cluster," per the type's own doc comment.
- Both `ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE` and `ACR_WORKLOAD_TRUST_DOMAIN`
  must be set for the grant to be enabled at all
  (`workloadTokenExchangeConfigured`); unset (the default) leaves
  `WorkloadTokenExchange` nil and the grant returns a clean 503 — it does
  **not** fail open (ADR 0007's "an unset deployment never fails closed"
  convention, per the code comment at `workload_token_exchange.go:39-44`).
- The subject token's own `exp` claim is read **without** signature
  verification (`unverifiedJWTExpiry`) only to cap the *issued* access
  token's TTL — the doc comment is explicit that this is never an identity
  or authorization claim, only an auxiliary timestamp, because TokenReview
  above is the sole authentication authority.

**How it maps to a workload principal:** `authverify.SubjectIdentity{TrustDomain,
Namespace, ServiceAccountName, ServiceAccountUID}` → acr's own
`storageGrantResolver.Resolve` (`internal/auth/workload_grant_resolver.go:24-49`)
looks up `storage.WorkloadBindingKey{TrustDomain, Namespace, ServiceAccountName,
ServiceAccountUID}` against acr's own `workload_bindings` store → returns
`authverify.WorkloadBinding{BindingID, OrgID, GrantedScopes: RoleScopes(binding.Role),
RepositoryScopes}` (role→scope policy is acr's own, not dev-health-go's) →
`serviceAccessTokenIssuer.Issue` (`internal/auth/workload_access_token_issuer.go:42-65`)
mints an ordinary `fcacr_` credential via the SAME `Service.Create` every
other issuance path uses, tagged
`CredentialIssuanceProvenanceWorkloadExchange` and bound to
`WorkloadBindingID`. On every SUBSEQUENT call, that credential validates
**exactly like `acr_client_credential`** — `internal/auth/middleware.go:139-142`
keys quotas on the stable `WorkloadBindingID` rather than the (roughly
10-minute-lived, per-exchange) `CredentialID`, so quota state survives
re-exchange.

**Divergence between v0.6.1 (ops's pin) and v0.5.5 (acr's pin):**
`git -C /Users/chris/projects/full-chaos/dev-health-go diff v0.5.5 v0.6.1 -- authverify/`
returns **zero lines of diff** — the entire `authverify` package is
byte-identical between the two pinned tags. No divergence to report for
this class. (Note, not independently verified in this pass: dev-health-go's
own doc comments reference a `query-api` consumer in ops's repo alongside
acr as `authverify` callers — if ops's own workload-identity endpoint
exists and pins a *third* version, that is outside this lane's ops-write
scope and is reported to auth-cp rather than investigated further here.)

## acr-mcp client-side credential handling

`internal/sidecar/credential.go::loadCredentialForLifecycleSession` — load
precedence, first match wins:

1. `ACR_API_TOKEN` env var (shape-validated: must be `auth.TokenPrefix`-prefixed;
   a configured-but-malformed value is a hard error, not a silent fall-through
   to a lower-precedence source — `loadFromEnvironment`'s `configured` bool).
2. OS keyring, if configured and not disabled (`ACR_API_TOKEN_KEYRING_DISABLED`),
   bounded by a 2s timeout (`keyringLookupTimeout`) so a hung secret-store
   backend cannot stall credential resolution — falls through to the file
   on an exact miss or unavailable keyring executable, but fails closed on
   any other lookup error.
3. Token file (`ACR_API_TOKEN_FILE` or a default path) — see
   `authentication.md`'s "Sidecar loading" section for the full file-based
   contract (already anchored by L0/L1's conventions; not re-anchored here).

`internal/sidecar/workload_credential_source.go` is the CLIENT side of the
RFC 8693 grant above: when acr-mcp itself runs as a Kubernetes workload, it
re-reads its own projected ServiceAccount JWT from `ACR_SUBJECT_TOKEN_FILE`
on every exchange (never cached — kubelet's in-place rotation is always
honored) and calls `ACR_TOKEN_ENDPOINT` (the same `POST /api/v1/oauth/token`
above) with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`,
caching the resulting access token with a 30s refresh margin
(`workloadRefreshMargin`).

## Schema-evolution finding — RESOLVED

This lane reported (independently re-derived from acr's side, and matching
web's report) that `endpoint-profile.schema.json`'s `service` field was a
closed 2-value enum naming only ops's two deployed apps, and was `required` —
so `endpoint-profiles.acr.json`, which sets `service: "dev-health-acr-api"` on
every row, could not validate against the schema at all. Reported, not
silently forked.

**Fixed in the ops repo that owns the schema.** The enum now carries all five
deployed apps (`dev-health-ops-api`, `dev-health-ops-billing-edge`,
`dev-health-web`, `dev-health-acr-api`, `dev-health-acr-mcp`) and is still
**closed on purpose** (G-26: an unknown service must fail rather than be
quietly accepted). This file validates as published, and the
`schema_deviation_note` key that carried the warning has been removed — it was
itself an extra top-level key that the schema's `additionalProperties: false`
rejects.

The schema has since also gained an optional `issued_credential` array, so a
surface that MINTS a credential can be recorded as an issuer rather than only
as an acceptor (guardrails G-12, G-61). The field is optional precisely so an
untraced row reads as "predates the field" rather than as a false "issues
nothing".

**Traced (lane auth-cp/L3):** three rows carry it now, each backfilled only
where the mint site was traced end to end, never inferred from a route's
name:

- `POST /api/v1/oauth/device_authorization` mints `acr_device_flow_code`
  (`DeviceFlowService.Start`, `internal/auth/device_flow.go:89-121`).
- `POST /api/v1/oauth/token` mints `acr_client_credential` on **two**
  independent grant paths dispatched by `Content-Type` (`handleDeviceToken`,
  `internal/api/device_routes.go:69-79`), both recorded as separate
  `issued_credential` entries on the same row: the device-code grant
  (`DeviceFlowService.redeem`, `internal/auth/device_poll.go:90-112`) and the
  RFC 8693 token-exchange grant (`serviceAccessTokenIssuer.Issue`,
  `internal/auth/workload_access_token_issuer.go:42-65`, reached via
  `authverify.WorkloadTokenExchangeService` — the acr-side issuer it wraps is
  wired in `internal/runtime/hosted/workload_token_exchange.go:93-97`, so the
  mint site itself is in-repo even though the exchange orchestration crosses
  into `dev-health-go`).
- `POST /api/v1/oauth/device_approval` (`handleDeviceApproval`) was traced
  and confirmed to mint **nothing**: `DeviceFlowService.Approve`
  (`internal/auth/device_flow.go:152-...`) only mutates the device
  authorization's approval state via `store.Approve`; the actual credential
  is minted later, on redemption at `POST /oauth/token` above. Recorded as
  `issued_credential: []` (assessed, not absent) — a false issuer in an
  issuer inventory does the same damage as a missing one.

## Not independently re-verified in this pass (flagged, not guessed)

- Whether `GET /api/v1/context-fabric/model-config`'s response body ever
  includes the sealed BYO-LLM credential material itself vs. metadata only
  (see the row's `gaps`).
- `RequireRepository`'s exact call site for `POST context-packets` beyond
  the shared `protectedRuntimeHandler` wrapper.
- `InvestigationResultStore.Get`'s org-scoping enforcement at the storage
  layer (documented as a binding precondition in `AGENTS.md`, not
  re-traced into `internal/contextfabric/pginvestigation` here).

## Row addressing: symbol, not line (CHAOS-7128, CHAOS-7245)

The acr gate (`ci/checkendpointprofiles`) matches a row to its route by
`source.file` + `method` + `route`, and every anchor -- `primary_validator`,
each `reachable_validators[]` and each `issued_credential[]` -- by a marker in
its `note`, found in the PARSED Go source as an AST node. `source.line` is
ignored by the gate after schema validation (the shared schema still requires
it). An anchor's `line` / `line_end` are hints for where the symbol was: the
gate checks only that `line` is a positive number and that `line_end` is not
before it, never the file's content at that line, so moving the symbol (an
unrelated edit above it) does not fail and needs no re-anchoring. Renaming or
removing the route fails as `PHANTOM ROW` + `UNOWNED SURFACE`; renaming the
marked symbol fails as `ANCHOR MARKER NOT FOUND`.

The marker is the source text that opens the anchored construct: either a
whole `func` header prefix (`func (a *App) protectedRuntimeHandler(`), or a
call that opens with its full callee (`a.protectedRuntimeHandler(limits.`).
It must be a prefix of the source of a function declaration or call expression
at that node's start. Text in a string literal, in a comment, in another
call's arguments, or a name that only shares a prefix (`mux.Handle` against
`mux.HandleFunc(`) is not an anchor: a wrapper removed with its text left in a
string fails as `ANCHOR MARKER NOT A CODE NODE`. When a note has backticks the
marker is the first backtick-quoted span and the rest is prose: `` `func (s *S)
Start(` `s.store.Create(` -- mints the device code ``. An anchor with no marker
fails as `MISSING ANCHOR MARKER`.

An `issued_credential` anchor carries two markers, both required: the
enclosing `func` declaration, then the call that mints the credential, which
must appear exactly once in that function's body, called by the function itself
(`ANCHOR CONTENT MISMATCH` if it is not there, or if the only call sits inside a
func literal nested in the body, which the function may never invoke;
`AMBIGUOUS ANCHOR MARKER` if it is there twice). A call in a nested literal
beside the function's own call is not counted as a second site.

Marker rule, one invariant per (file, marker) across all anchor kinds: no line
may hold two of its sites, and the number of sites in the file must equal the
number of distinct sites the rows declare for it (rows citing one shared
definition line are one site; a `func` declaration is always one site). More is
`AMBIGUOUS ANCHOR MARKER`; none or fewer is `ANCHOR MARKER NOT FOUND`.

What this does not prove (CHAOS-4780, reachability is out of scope for this
gate):

- A row anchored at a shared definition shows that the definition exists, not
  that a given route uses it. That covers the ten rows marked by
  `protectedRuntimeHandler`'s declaration, and the six rows marked by
  `authenticateWebAssertion`'s declaration (the reachable validator of
  `GET /api/v1/agent-context/capabilities`, `POST /api/v1/agent-context/context-packets`,
  `GET /api/v1/agent-context/evidence/{evidence_ref_id}`,
  `POST /api/v1/context-fabric/investigations`,
  `GET /api/v1/context-fabric/investigations/{result_id}` and
  `GET /api/v1/context-fabric/model-config`): removing the call to either from
  the code that dispatches through it leaves the declaration, and the gate green.
- A route's own-call marker (`a.protectedRuntimeHandler(...)` in its handler
  builder) is located in the file, not bound to the route's handler builder: the
  route fails if that call is removed or renamed, but moving the call to another
  function in the same file, one the route no longer uses, is not caught. Binding
  a marker to the handler builder needs a new row field, which is a schema and
  scope change; it is tracked as CHAOS-7280 (CHAOS-4780 class), not done here.
- A mint call in an `issued_credential` anchor is bound to its function, but
  whether that function is reached is not judged either.
