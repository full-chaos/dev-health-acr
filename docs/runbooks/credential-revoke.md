# Runbook: revoke an ACR credential

Use this runbook when an ACR credential (`fcacr_`) is lost, leaked or retired.
It does not cover the `acr-api` workload credential; for that, see
[workload credential rotation](workload-rotation.md).

Revocation is terminal. A revoked credential is never accepted again. To give the
caller access again, issue a new credential.

## Before you start

- The runtime database DSN in `ACR_POSTGRES_DSN` (the same input `acr-api` uses).
- The organization id.
- The credential id. Find it with
  `acr-api credentials list --org-id <org> --json`. The list holds no token
  values.

You do not need the lost token.

## Steps

1. Look up the credential id:

   ```sh
   acr-api credentials list --org-id <org> --json
   ```

2. Revoke it:

   ```sh
   acr-api credentials revoke --org-id <org> --credential-id <id> [--reason <text>] [--actor <id>]
   ```

   The command uses the same storage path as the self-revoke route. The audit
   event records actor type `operator`, the actor id (default `operator`) and
   the reason.

3. Check the result:

   - A request that presents the token is refused with the uniform
     `invalid_token` answer (HTTP 401).
   - `credentials list` shows the credential as revoked.
   - The audit event is present with actor type `operator`.

## Errors

| Answer | Meaning |
|---|---|
| `not found` (non-zero exit) | The id is not a credential of that organization. |
| `already revoked` (non-zero exit) | The credential was revoked before. Nothing changed. |

## What to expect

`acr` keeps no credential cache. It reads the committed credential row on every
request, so a refusal does not wait for any propagation. A request that was
authenticated before the revoke transaction committed may finish with its
established principal; revocation does not cancel an in-flight handler.

The revoke is also published to the identity service (the ACP projection) through
an outbox. The projection shows the credential as revoked after the publisher's
next timer tick. The refusal in `acr` does not depend on that publication.

### Measured in a drill on 2026-11-12

A private Compose project ran the real `go-api`, `auth-service` and `acr-api`
with test credentials only. These numbers were measured in that drill. They are
not a promise and not a service level.

| Check | Measured |
|---|---|
| Revoke command, start to return | 1.8 s and 1.9 s in two runs |
| First refusal after the command returned | 0.08 s, with no accepted request after it |
| Projection shows the credential as revoked | 26 s after the revoke (set by the publisher timer) |
| Refusal while `auth-service` was down | still refused; the projection caught up afterwards |
| Refusal after an `acr-api` restart | still refused |

### Named limit

A credential that belongs to an organization the identity service does not know
is refused by the projection with a final `409 projection_refused`. The outbox
row is marked published and is never projected. `acr` still refuses the
credential, because the refusal there does not depend on the projection.

## Related

- [Credential lifecycle](../authentication.md#lifecycle)
- [Secrets and credentials](../operations.md#secrets-and-credentials)
