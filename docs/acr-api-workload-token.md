# acr-api workload token

## Overview

`acr-api` can exchange its deployment identity for short-lived workload tokens.
It uses these tokens to call the ops server and to publish credential lifecycle
events to auth-service. The two uses have separate switches.

## Credential choice

`ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL` selects the credential for the Dev Health
entitlement client. This client calls health, entitlement, model configuration,
and principal access routes.

| Value | Credential |
| --- | --- |
| `service_token` (default) | Read the service credential from `ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE`. Example form: `svc_acr_<redacted>`. |
| `workload` | Request a workload token for audience `dev-health-api`. |

Use chart key `config.entitlement.credential`. An empty chart value keeps the
default. Set the value here, not in `deployment.extraEnv`. The chart rejects
both sources together.

Workload mode requires `ACR_DEV_HEALTH_ENTITLEMENT_URL` and the workload client
settings below. It never opens the service token file, even if the file remains
mounted. It never sends a `svc_acr_` credential. There is no runtime fallback.
If no usable workload token exists, the client sends no request and fails
closed with an unavailable entitlement. An invalid credential choice stops
startup.

The chart permits `credentials.entitlementToken.existingSecret` to remain set
in workload mode for rollback. If it is empty, the chart omits the token volume
and its init container.

## Workload client settings

| Setting | Purpose |
| --- | --- |
| `ACR_AUTH_WORKLOAD_EXCHANGE_URL` | Auth-service origin for the token exchange. |
| `ACR_AUTH_WORKLOAD_ISSUER` | Expected HTTPS token issuer URL. |
| `ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP` | Permit an HTTP exchange origin. Default: `false`. |
| `ACR_AUTH_WORKLOAD_SUBJECT_FILE` | Projected Kubernetes ServiceAccount token file. |
| `ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE` | Bootstrap credential file for Compose. |

Set the exchange URL, issuer, and exactly one subject or bootstrap file.
A partial or invalid configuration stops startup. The client reads the selected
file again for every exchange, so it picks up file rotation without a restart.

The chart uses `authWorkloadClient.enabled` (default `false`),
`authWorkloadClient.exchangeURL`, `authWorkloadClient.issuer`, and
`authWorkloadClient.allowPlainHTTP`. It sets the subject file path and projects
a ServiceAccount token with audience `dev-health-workload-exchange` and lifetime
600 s. This subject token differs from the exchanged workload token.

The client caches workload tokens within their usable lifetime, at most 300 s.
It refreshes before expiry and can use a still-usable cached token during a
short exchange outage. It never sends an expired token. It makes no exchange
at startup. Readiness does not depend on an exchange. An auth-service outage
does not stop `acr-api`.

## Credential lifecycle outbox and publisher

Credential writers append lifecycle events to
`acr.credential_lifecycle_outbox` in the same transaction as the credential
change. An outbox write failure rolls back that change. A background lifecycle
tick also records expiry and the first event for live credentials that predate
the stream. These writes do not depend on the publisher switch.

Set `ACR_AUTH_PROJECTION_URL` to the auth-service origin to enable the publisher.
Unset means off. The publisher requires the workload client above.
The chart key is `authWorkloadClient.projectionURL`; it requires
`authWorkloadClient.enabled=true`. The chart does not open egress to
auth-service. Configure that access in the deployment network policy.

Each enabled `acr-api` pod runs a tick every 30 s and attempts up to 100 events.
It posts one stored envelope per request to
`POST /internal/v1/acr/credential-events`. It requests audience `auth-service`
without a scope. The registry supplies the workload's `credentials.project`
action. Startup sends no request. An auth-service outage does not block
credential mutations, which continue to write outbox rows.

Each event uses one database transaction: claim, send, mark, commit.
The claim uses `FOR UPDATE SKIP LOCKED`. It selects the oldest eligible
unpublished event and excludes held aggregates. An aggregate is the event
history for one credential. No later revision can pass an earlier unpublished
revision, even across pods. A crash or failed commit after an acknowledgement
can resend the event. A duplicate receives `204`.

Token acquisition and the POST share a 5 s timeout. Each claim transaction sets
`statement_timeout` to 10 s and `idle_in_transaction_session_timeout` to 20 s.

| Answer | Publisher action |
| --- | --- |
| `204` (applied, duplicate, or stale) | Set `published_at` and commit. |
| `409`, reason `projection_retry` | Leave unpublished. Hold the aggregate and continue with other aggregates. |
| `409`, reason `projection_conflict` | Final outcome. Mark published and count `projection_conflict` separately. |
| `409`, reason `projection_refused` | Final outcome. Mark published and count `projection_refused` separately. |
| `400` | Leave unpublished. Count `schema`, hold the aggregate, and continue. |
| `401` or `403` | Leave unpublished. End the tick with `auth_refused`. Add no hold. |
| `429` or `503` | Leave unpublished. End the tick with `busy` and pause. Honor `Retry-After`, capped at 5 min. Use 30 s if no positive wait is supplied. |
| No usable workload token | Send no request. Leave unpublished. End the tick with `no_token`. Add no hold. |
| Transport failure or timeout | Leave unpublished. Hold the aggregate and end the tick with `no_answer`. |
| Any other answer, including an unknown `409` reason | Leave unpublished. Count `other`, hold the aggregate, and continue. |

For `429` and `503`, the client accepts `Retry-After` as seconds or an HTTP date.
If the header does not parse, it reads `retry_after_seconds` from the response.
Holds start at 30 s, double on repeated failures, and stop growing at 5 min.
Later revisions wait with the held aggregate. Holds stay in memory; a restart
retries them. The held set has a limit of 1,000 aggregates. A new hold beyond
that limit ends the tick with `held_full`. A database failure also ends the
tick; the next tick retries it.

Publisher logs carry counts: `applied`, `projection_retry`,
`projection_conflict`, `projection_refused`, `schema`, `other`, and `no_answer`.
Warnings carry `held` or `stopped_by`. The logs do not include event bodies,
aggregate identifiers, credentials, tokens, or response text.

Counter `acr_entitlement_credential_total` measures entitlement client calls,
not publisher events. Its labels are `class` (`service_token`, `workload`,
`other`) and `outcome` (`sent`, `token_unavailable`, `other`). A workload call
that cannot obtain a token records `token_unavailable`. `acr-projector purge`
uses the same credential choice but does not record this counter.

The runtime role has `SELECT` and `INSERT` on the outbox. When the publisher is
configured, it also needs `UPDATE (published_at)` and `DELETE`. It has no
table-level `UPDATE` grant. `grant-runtime-acl` and the Compose bootstrap grant
these extra privileges when `ACR_AUTH_PROJECTION_URL` is set and revoke them
when it is unset.

The reaper runs only while the publisher is configured. Every hour, it removes
up to 100 whole aggregates, oldest first. Every row must be published.
The latest publication must be more than seven days old. No matching row may
remain in `acr.client_credentials`. A terminal event is not required.
The reaper never removes individual revisions from a retained aggregate.
It logs deleted `aggregates` and `rows` counts. A failed run deletes nothing.

## Migration from `svc_acr_`

1. Prepare the server side first. Confirm that the ops server build accepts
   workload tokens. The ops server uses the workload registry to select the
   single live `acr-api` workload with organization `NULL` and audience
   `dev-health-api`. Configure its trusted issuer and the auth-service exchange.
   Configure the ACR workload client. Note the current `acr-api` image and
   chart revision: they are the rollback target (see Rollback).
2. Set `ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL=workload`, or set chart key
   `config.entitlement.credential=workload`. Roll out `acr-api`.
3. Watch `acr_entitlement_credential_total` across all `acr-api` instances.
   Wait until the call rate for `class=service_token` is zero while workload
   calls succeed. A cumulative counter does not decrease to zero. Check that
   `class=workload,outcome=token_unavailable` does not increase. Check the
   credential choice for any separate `acr-projector purge` runs too.
4. Stop mounting the service token file. In the chart, clear
   `credentials.entitlementToken.existingSecret` and roll out the change.
5. Ask the ops operator to revoke the old credentials. Revocation is final.

## Rollback

The rollback is the image rollback. Run `helm rollback` of the acr release to
the previous revision, then roll forward again when the fault is fixed. The
rehearsal 143 -> 142 -> 143 passed (row A1, fcdev RECORD-11241-1, sha256
`d955471080f1`, run `run-a1v3-1`): revoked credentials stayed revoked, a removed
member stayed refused, and `/readyz` returned 200 after each step.

Do not use the flag as a rollback. Setting
`ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL=service_token` (or chart key
`config.entitlement.credential=service_token`) against a current ops build
stops `acr-api` at start with exit 1 and the message
`initialize hosted runtime: initial entitlement readiness check: Dev Health
entitlement service unavailable`. Current ops builds refuse the class with a
uniform `401` on all four internal ACR routes, including the health route that
the start check calls. `acr` serves nothing until the flag is set back to
`workload` and `acr-api` is rolled out (rehearsal row A2: `/readyz` returned
200 at the first poll after that).

Not rehearsed: `acr-projector` 143 -> 142, and a rollback while go-api is down.
Whether the `service_token` mode should be removed from `acr-api` is open
(CHAOS-13548).

Never reactivate a revoked or expired credential. Rotation does not restore it.
Current ops builds have no command that mints a new `svc_acr_` credential. To go back, use an ops build that still accepts the class and a credential that is still valid.

To stop the publisher, unset `ACR_AUTH_PROJECTION_URL`. In the chart, clear
`authWorkloadClient.projectionURL`. Outbox writes continue. Unpublished rows
stay in the outbox and the publisher sends them when re-enabled. The reaper
also stops while the URL is unset.

## Source of facts

- Credential choice and labels: [entitlement configuration](../internal/config/entitlement.go), [request authorization](../internal/entitlements/credential.go), and [metric instruments](../internal/hostedmetrics/hostedmetrics.go).
- Workload client: [configuration](../internal/config/auth_workload.go) and [client construction](../internal/authworkload/authworkload.go). The pinned `github.com/full-chaos/dev-health-go/workloadtoken` dependency implements the token cache; [go.mod](../go.mod) pins its version.
- Publisher: [configuration](../internal/config/auth_projection.go), [HTTP client](../internal/authprojection/client.go), [delivery policy](../internal/authprojection/publisher.go), and [background loop](../internal/runtime/hosted/credential_outbox_publisher.go).
- Outbox writes: [transaction sink](../internal/storage/postgres/outbox.go), [credential writers](../internal/storage/postgres/credentials_audit.go), [browser refresh](../internal/storage/postgres/browser_refresh.go), [device redemption](../internal/storage/postgres/device_authorization_redeem.go), [device acknowledgement](../internal/storage/postgres/device_authorization_ack.go), and [lifecycle tick](../internal/storage/postgres/lifecycle_tick.go).
- Claims and retention: [publish transaction](../internal/storage/postgres/outbox_publish.go), [reaper statement](../internal/storage/postgres/outbox_reap.go), and [reaper loop](../internal/runtime/hosted/credential_outbox_reaper.go).
- Database privileges: [runtime grants](../internal/dbacl/runtime_grants.go).
- Chart settings: [values](../deploy/helm/acr/values.yaml), [configuration template](../deploy/helm/acr/templates/configmap.yaml), and [deployment template](../deploy/helm/acr/templates/deployment.yaml).
