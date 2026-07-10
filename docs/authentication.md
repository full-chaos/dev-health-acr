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

## Lifecycle

Credential services support create, list, rotate, and revoke operations. Rotation defaults to immediate cutover and may request a bounded overlap of no more than 15 minutes. Expired or revoked credentials return the generic `invalid_token` contract response so callers cannot enumerate credential state.

Successful use updates last-used metadata outside downstream business transactions. Known-credential denials and successful use emit audit events. Unknown tokens cannot be tied to an organization and are recorded in structured security logs instead of the organization audit table.

## Sidecar loading

The local sidecar checks:

1. `ACR_API_TOKEN`
2. `ACR_API_TOKEN_FILE`

Token files must be owner-only on POSIX systems. The `doctor` command reports source and shape validity without revealing the token. OS keychain adapters can be added behind the same credential-source contract later.

## Rate limiting

The auth package exposes an attempt/failure limiter and includes a deterministic in-memory implementation for tests and local operation. Production shared rate-limit storage is owned by the platform observability/rate-limit work and must preserve the same pre-lookup attempt ceiling.
