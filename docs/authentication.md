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

## Rate limiting

The auth package exposes an attempt/failure limiter and includes a deterministic in-memory implementation for tests and local operation. Production shared rate-limit storage is owned by the platform observability/rate-limit work and must preserve the same pre-lookup attempt ceiling.
