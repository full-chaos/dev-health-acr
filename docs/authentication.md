# ACR client credentials and repository authorization

ACR product entitlement and ACR API authentication are separate controls.

- `agent_context_runtime` answers whether an organization purchased or was granted the product.
- An `fcacr_` credential identifies a machine client and grants explicit repository and operation scopes.
- A Dev Health self-hosted license key is never accepted as an ACR bearer credential.

## Token form

```text
fcacr_<256-bit URL-safe random secret>
```

Only the SHA-256 digest is stored. The plaintext token is returned once during creation or rotation. Logs, audit metadata, list operations, and diagnostics expose only a short human-recognizable prefix.

## Permissions

```text
context:read
evidence:read
episode:write
```

Permissions are independent. `episode:write` does not imply either read permission. Credentials default to the two read scopes only.

## Repository scopes

Supported selectors:

```text
full-chaos/dev-health-acr  # exact repository
full-chaos/*               # all repositories under one owner
*                          # all repositories authorized to the organization
```

The service rechecks repository authorization on every packet, evidence, snapshot, and episode operation. An opaque packet or evidence ID never bypasses this check.

Interactive device login defaults to the singleton `*` selector. This means
"all repositories belonging to the authenticated organization," not a
cross-organization grant: the credential remains bound to its server-derived
`OrgID`, and every store query remains independently organization-scoped. A
current interactive web approval always issues `repository_scopes: ["*"]`.
Exact repository lists remain supported by the protocol and service for
explicit clients and a future limited-grant UX; the current web UI does not
offer that selection. Device authorization repository hints, the MCP process
working directory, local Git discovery, and the current analytics repository
catalog are display or request context only; none narrows or expands
authorization.

## Lifecycle

Credential services support create, list, rotate, and revoke operations. Rotation defaults to immediate cutover and may request a bounded overlap of no more than 15 minutes. Expired or revoked credentials return the generic `invalid_token` contract response so callers cannot enumerate credential state.

Authorization is evaluated when middleware looks up the credential. A request authenticated before a revocation transaction commits may finish with its established principal; revocation does not cancel in-flight handlers. Every credential lookup that starts after the commit rejects the revoked token. This is the standard request-boundary model rather than continuous authorization during handler execution.

Successful use updates last-used metadata outside downstream business transactions and emits an audit event. Rejected token lookups cannot safely disclose or attribute credential state, so they are recorded in structured security logs instead of the organization audit table.

## Sidecar loading

The local sidecar resolves a credential with a fixed precedence:

1. `ACR_API_TOKEN`
2. The explicit or default OS keyring entry (macOS/Linux)
3. `ACR_API_TOKEN_FILE`, defaulting to `~/.acr/token` (macOS/Linux)

Ordinary macOS/Linux setup sets `ACR_API_URL` (and an optional CA bundle), then
runs `acr-mcp login`. Login persists to the default keyring or restricted
fallback file, and later MCP processes discover that source automatically.
MCP registration should contain neither a credential nor a credential-file
path; `ACR_API_TOKEN_FILE` is an advanced location override. Windows is the
documented exception because secure login persistence is unavailable there:
the client must inherit `ACR_API_TOKEN` from its launching shell.

Token files must be owner-only on POSIX systems, and their parent directory must
deny group and world write access for removal as well as for writing. The
`doctor` command reports source and shape validity without revealing the token.

Precedence answers "which credential wins", which is the wrong question for
`logout`. Logout enumerates **every** configured location and revokes each
distinct value before removing anything, because an exported `ACR_API_TOKEN` over
a keyring entry over a token file is three separate credentials: revoking only
the winner leaves the others live on the server while their local copies are
deleted. Enumeration fails closed -- an unreadable keyring or token file stops the
whole operation rather than deleting around a location that may hold a live
credential. `invalid_token` on an established credential means it is already
inactive and does not block cleanup; on a credential this client just had issued
it stays a failure, because a token the server minted seconds ago and now refuses
is evidence the client cannot tell.

`acr-mcp logout --local` is the explicit offline exception. It skips hosted URL,
TLS, and remote revocation entirely, then applies the same fail-closed enumeration
and snapshot-bound local purge. It reports that the remotely issued credentials
may remain active. Use it when the hosted service is unavailable and local
credential erasure is more important than proving server-side revocation; revoke
the credentials through an authenticated administrative surface after service is
restored. An exported `ACR_API_TOKEN` still requires `unset ACR_API_TOKEN` in the
parent shell.

`login` preflights `CredentialPersistenceSupported` before starting a device
authorization, so a platform without secure persistence never causes the server
to mint a one-time credential that has nowhere to live.

Plain `login` is idempotent. If a shape-valid credential already exists, the
sidecar verifies that exact captured credential against the hosted capabilities
endpoint while holding the lifecycle lock. A valid credential returns success
without starting another device authorization. A typed `invalid_token` response
proves that credential is inactive, so the sidecar removes its captured local
material and automatically starts a fresh device flow. If local cleanup fails,
the sidecar retains the unresolved location, reports the required operator
action, and does not start a replacement flow.

Every ambiguous verification result retains the existing credential and stops:
network or TLS failure, an untyped authorization response, a server 426 or
feature error, or any other API/validation error besides typed `invalid_token`
is not proof that the server invalidated the credential. A syntactically valid
capabilities response proves authentication acceptance even if later MCP
compatibility gates would reject tools. This prevents a transient outage or
protocol problem from destroying the only local copy or issuing an unnecessary
replacement.

`login --refresh` preserves the current credential's repository scopes. It does
not silently widen an older exact-snapshot credential to the new interactive
default. To replace an exact credential with an organization-wide credential,
run `acr-mcp logout` (which revokes remotely before removing local material),
then run `acr-mcp login` and approve the new request in the web UI.

## OAuth login for hosted MCP clients

acr-api is an OAuth 2.1 authorization server for the hosted MCP endpoint when
`ACR_OAUTH_ISSUER` (the acr-api public origin) and `ACR_OAUTH_RESOURCES` (the
hosted MCP URLs, comma separated) are set. It needs `ACR_OAUTH_CONSENT_URL`
(the web consent page, e.g. `https://www.example.com/acr/authorize`), the web
approval surface (`ACR_WEB_ASSERTION_*`) and the hosted runtime; startup fails
without them.
`ACR_OAUTH_CLIENT_METADATA_DOCUMENTS` (default `true`) accepts HTTPS client ID
metadata documents; they are fetched only from public addresses, with no
redirects, a 5-second timeout and a 5 KiB limit.

Routes: `GET /.well-known/oauth-authorization-server`, `GET /authorize`,
`POST /authorize/consent`, `POST /token`, `POST /register`.

- Only the authorization-code grant with PKCE `S256`. No implicit grant, no
  refresh token, no client secrets.
- Consent happens on the web consent page, with nothing to type. `/authorize`
  verifies the client, the exact registered redirect URI, PKCE, the resource
  and the scope, starts a device authorization whose raw device and user codes
  are discarded (neither the device grant nor the typed-code approval page can
  use it), and answers `302` to `ACR_OAUTH_CONSENT_URL?handle=<handle>`. The
  handle is 256-bit, stored as SHA-256, and lives as long as the device
  authorization (10 minutes). A request that fails verification never reaches
  the consent page: an unverified client or redirect URI gets an error page,
  anything else the OAuth error redirect.
- The web signs the user in (and returns to the same consent URL), then calls
  `POST /authorize/consent` for the signed-in user with a web assertion
  (`credential:issue`, exactly one `X-ACR-Web-Assertion`, no `Authorization`;
  the browser that holds the handle cannot call it). The JSON body is
  `{"action":"preview"|"approve"|"deny","handle":...}`, plus
  `repository_scopes` on approve. `preview` returns the client name (and
  whether the client named itself), the redirect origin, the resource, the
  scopes and the expiry. `approve` approves the device authorization with the
  assertion's org and the chosen repositories (the same rules as the typed-code
  approval), issues the one authorization code and returns
  `{"redirect_url": "<redirect_uri>?code=...&state=...&iss=..."}`; `deny`
  returns the same with `error=access_denied`. The web sends the browser to
  `redirect_url`. A request is decided once: a later read or decision is `409
  already_completed`, with one exception: if the approval was recorded but
  the code could not be attached (a `503`), the same user may approve again
  with the same repositories to finish; an expired request is `410 expired`; an unknown handle
  or a decision the approver may not make is `400 invalid_request` and leaves
  the request undecided.
- The authorization code is 256-bit, stored as SHA-256, valid 2 minutes and
  single use. The token endpoint consumes it before any other check, so a
  refused exchange (wrong verifier, redirect URI, client or resource) spends it.
- The issued credential is an ordinary `fcacr_` credential (30 days, live
  revocation). It carries a resource binding (`client_credentials.resource`,
  migration 0040), never on the wire. The authenticator accepts a bound
  credential only when the request carries `X-ACR-Resource` with exactly that
  value; the hosted MCP endpoint sets that header from its own configuration.
  A credential without a binding (operator, device, workload) is unaffected.
- Each OAuth request writes one `acr-api oauth step` line (step, outcome,
  client kind, status). Steps: `register`, `authorize` (ok = sent to the
  consent page), `consent_preview`, `consent` (ok = approved, `access_denied`
  = denied, `unauthenticated` = no valid web assertion; every consent request
  writes its line, including those refused before the handler), `token`. Codes, handles, verifiers, client IDs, redirect URIs,
  state and tokens are never logged.

The runtime database role needs `SELECT, INSERT, UPDATE` on
`acr.oauth_clients` and `acr.oauth_authorization_requests`
(`deploy/compose/acr-db-init.sh runtime-acl`).

## Rate limiting

The auth package exposes an attempt/failure limiter and includes a deterministic in-memory implementation for tests and local operation. Production shared rate-limit storage is owned by the platform observability/rate-limit work and must preserve the same pre-lookup attempt ceiling.
