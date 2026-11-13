# Runbook: rotate the `acr-api` workload credential

The `acr-api` workload credential lets `acr-api` exchange for short-lived workload
tokens at the identity service. This runbook rotates it with a bounded overlap,
then revokes the previous value.

This runbook covers Docker Compose. The ACR Helm chart has no bootstrap path:
on Kubernetes `acr-api` uses the projected service-account token, so there is no
bootstrap file to rotate there.

## Before you start

- The credential file path, `/run/workload/acr-api.bootstrap` in the Compose
  overlay (`deploy/compose/acr-auth-workload.compose.yml`).
- A rotation window before the credential's 720 h expiry.
- The commands below are the ones in
  [acr-api workload credential on Compose](../operations.md#acr-api-workload-credential-on-compose-overlay);
  replace `...` with the same Compose file arguments given there.

`acr-api` reads the file again for every exchange, so a rotation needs no restart.
A workload token lives at most 300 s.

## Steps

1. Rotate into a new file next to the live one. The live file is not touched and
   the previous credential stays valid for the overlap:

   ```sh
   ... --profile auth run --rm acr-workload-bootstrap workloads bootstrap-rotate \
     -workload acr-api -out /run/workload/acr-api.bootstrap.next -overlap 1h -apply
   ```

   Keep the `previous credential=<id>` line it prints. It holds no value.

2. Inside the overlap, deliver the new file over the live one as its owner, in
   one rename:

   ```sh
   ... --profile auth run --rm --user 65532:65532 --entrypoint sh acr-workload-volume -c \
     'mv -f /run/workload/acr-api.bootstrap.next /run/workload/acr-api.bootstrap'
   ```

   `acr-api` presents the new credential at its next exchange.

3. After the overlap, revoke the previous credential:

   ```sh
   ... run --rm acr-workload-bootstrap workloads bootstrap-revoke \
     -credential <previous id> -reason <reason> -apply
   ```

4. Check: `acr-api` keeps exchanging, the previous value is refused, and the
   service stays ready.

If the rotate command fails, the live file still holds the previous credential
and `acr-api` keeps running on it.

Do not remove the live file before the new one is delivered. A rename keeps one
valid file in place at every moment.

## What to expect

- During the overlap, requests that need an exchange keep succeeding.
- After the overlap the previous value is refused as `expired`. After the revoke
  it is refused as `revoked`.
- Putting the previous file back is refused too.

### Measured in a drill on 2026-11-10

An isolated Compose stack ran the real `auth-service` and `acr-api` with test
credentials, with a 10-minute overlap and constant load. These numbers were
measured in that drill. They are not a promise and not a service level.

| Check | Measured |
|---|---|
| Non-200 answers during the overlap | none |
| First exchange with the new credential after delivery | 13 s and 41 s in two runs |
| Previous value after the overlap | refused `expired`, about 630 s after the rotation, then `revoked` after the revoke |
| Previous file put back | refused |
| New file put back after that | recovered within 6 s to 17 s |

### Named limits

- Compose only, as stated above.
- The drill read the effect (no non-200 answer, no failed entitlement lookup),
  not the `acr_entitlement_credential_total` counter, because that stack exports
  the counter over OTLP only and had no collector.

## Related

- [Revoke an ACR credential](credential-revoke.md)
- [Workload token exchange](../acr-api-workload-token.md)
