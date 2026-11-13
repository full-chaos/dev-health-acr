# acr-api workload token

## Overview

`acr-api` exchanges its deployment identity for short-lived workload tokens.
It uses these tokens to call the ops server and to publish credential lifecycle
events to auth-service. The two uses have separate switches.

## Credential

The Dev Health entitlement client sends a workload token for audience
`dev-health-api` on every call. This client calls health, entitlement, model
configuration, and principal access routes. The workload token is the one
credential: there is no other mode and no runtime fallback.

A remote entitlement origin (`ACR_DEV_HEALTH_ENTITLEMENT_URL`) requires the
workload client settings below. `ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL` may be
unset or `workload`; any other value, the old service token credential value
included, stops startup with a message that names the setting, never the value.
The token file setting of the removed service token credential is ignored: the
start goes on and logs one Warn line that names the setting. `acr-projector
purge` applies the same rules.

If no usable workload token exists, the client sends no request and fails
closed with an unavailable entitlement.

In the chart, `config.entitlement.url` requires `authWorkloadClient.enabled`.
`config.entitlement.credential` may be empty or `workload`; set it there, not
in `deployment.extraEnv`. The chart rejects both sources together.

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
short exchange outage. It never sends an expired token. Building the client
makes no exchange, but the start-up entitlement check sends one call, so
`acr-api` does not start without a working exchange. `/readyz` does not depend
on an exchange. An auth-service outage after start does not stop `acr-api`;
entitlement calls fail closed until a token can be exchanged again.

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
not publisher events. Its labels are `class` (`workload`, `other`) and
`outcome` (`sent`, `token_unavailable`, `other`). A call that cannot obtain a
token records `token_unavailable`. `acr-projector purge` sends the same
credential but does not record this counter.

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

## Rollback

The rollback is the image rollback. Run `helm rollback` of the acr release to
the previous revision, then roll forward again when the fault is fixed. The
rehearsal 143 -> 142 -> 143 passed on a private Compose project at the
production image pins: revoked credentials stayed revoked, a removed member
stayed refused, and `/readyz` returned 200 after each step.

Not rehearsed: `acr-projector` 143 -> 142, and a rollback while go-api is down.

To stop the publisher, unset `ACR_AUTH_PROJECTION_URL`. In the chart, clear
`authWorkloadClient.projectionURL`. Outbox writes continue. Unpublished rows
stay in the outbox and the publisher sends them when re-enabled. The reaper
also stops while the URL is unset.

## Source of facts

- Credential and labels: [entitlement configuration](../internal/config/entitlement.go), [request authorization](../internal/entitlements/credential.go), and [metric instruments](../internal/hostedmetrics/hostedmetrics.go).
- Workload client: [configuration](../internal/config/auth_workload.go) and [client construction](../internal/authworkload/authworkload.go). The pinned `github.com/full-chaos/dev-health-go/workloadtoken` dependency implements the token cache; [go.mod](../go.mod) pins its version.
- Publisher: [configuration](../internal/config/auth_projection.go), [HTTP client](../internal/authprojection/client.go), [delivery policy](../internal/authprojection/publisher.go), and [background loop](../internal/runtime/hosted/credential_outbox_publisher.go).
- Outbox writes: [transaction sink](../internal/storage/postgres/outbox.go), [credential writers](../internal/storage/postgres/credentials_audit.go), [browser refresh](../internal/storage/postgres/browser_refresh.go), [device redemption](../internal/storage/postgres/device_authorization_redeem.go), [device acknowledgement](../internal/storage/postgres/device_authorization_ack.go), and [lifecycle tick](../internal/storage/postgres/lifecycle_tick.go).
- Claims and retention: [publish transaction](../internal/storage/postgres/outbox_publish.go), [reaper statement](../internal/storage/postgres/outbox_reap.go), and [reaper loop](../internal/runtime/hosted/credential_outbox_reaper.go).
- Database privileges: [runtime grants](../internal/dbacl/runtime_grants.go).
- Chart settings: [values](../deploy/helm/acr/values.yaml), [configuration template](../deploy/helm/acr/templates/configmap.yaml), and [deployment template](../deploy/helm/acr/templates/deployment.yaml).
