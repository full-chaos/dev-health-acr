# Private ACR developer and operator guide

This guide is for the private ACR service owners and the developers who run its
local MCP sidecar. ACR is a separate private hosted Go API (`acr-api`) plus a
host-local STDIO MCP binary (`acr-mcp`). It is not packaged or distributed by
`dev-health-ops`; Ops remains the evidence and entitlement source, and
External Push remains a separate source-fact ingestion API.

Read this guide with [service shell](service-shell.md),
[authentication](authentication.md), [MCP sidecar](mcp-sidecar.md),
[read API](read-api.md), [observability](observability.md), and
[container images](container-images.md). Evidence URLs are references only;
neither ACR nor its sidecar fetches them.

## Ownership and supported boundaries

| Concern | Owner / boundary |
| --- | --- |
| Engineering evidence and product entitlement | Dev Health Ops; ClickHouse is read-only to ACR |
| ACR credentials, packets, audits, and migrations | Private ACR hosted deployment and ACR PostgreSQL |
| Human inspection | `dev-health-web` |
| Agent integration | A locally installed `acr-mcp` process over STDIO |
| External fact ingestion | External Push; not the ACR API |

The deployment artifacts require caller-provided PostgreSQL, ClickHouse,
entitlement, Gateway, and Secrets. They do not provision those dependencies,
and no supported deployment runs `acr-mcp` in a container or Kubernetes Pod.

## Developer getting started

Run the offline contract and documentation gates before changing contracts,
deployment artifacts, or this guide:

```bash
bash scripts/docs/verify.sh
make contract-test
make verify
```

The stock API binary is intentionally not ready without the complete hosted
runtime bundle. It is useful for process and probe development, but not as a
production configuration:

```bash
ACR_ADDR=:8080 go run ./cmd/acr-api serve
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

Use the Compose acceptance driver for a full local TLS fixture. Its root
Compose input belongs to the caller's Dev Health checkout; the ACR overlay is
the checked-in file below. The helper creates ephemeral credentials and secret
files itself, and tears down only its isolated `acr-e2e-*` project:

```bash
bash scripts/docs/clean-room.sh --mode compose \
  --compose ../compose.yml \
  --overlay deploy/compose/acr.compose.yml
```

The Compose overlay requires an immutable `ACR_IMAGE` and the file-backed
inputs listed in `deploy/compose/acr.compose.yml`. Do not put secret values in
an environment file or command line. Compose routes the local API through its
TLS fixture; the `http://localhost` readiness probe is container-internal and
is not a production endpoint.

## Private deployment configuration

### TLS, origins, and web assertions

Production caller configuration must use TLS-verified origins and immutable
image digests. `ACR_DEV_HEALTH_ENTITLEMENT_URL` is an HTTPS origin outside a
loopback development fixture. The hosted API's externally published origin is
configured by the caller's Gateway and is not an ACR API runtime default.

Trusted web assertions are enabled only as a complete set:

```text
ACR_WEB_ASSERTION_ISSUER
ACR_WEB_ASSERTION_AUDIENCE
ACR_WEB_ASSERTION_JWKS_FILE
```

The JWKS is local public key material. Rotate by publishing the new key in the
JWKS, issuing assertions with its `kid`, observing successful verification,
then removing the retired key. Removing a key takes effect on the next
assertion because the file is reread. A process-local replay check is not a
global replay-prevention service.

### Secrets and credentials

Use pre-existing Secret references in Helm and Kustomize and owner-only regular
files for local `_FILE` inputs. ACR rejects direct and `_FILE` values supplied
together, symlinks, and files writable by group or others. Never commit,
print, or paste bearer values, DSNs, password values, evidence signing keys, or
entitlement tokens.

`agent_context_runtime` is an organization entitlement, not a credential. ACR
credentials use the `fcacr_` format and explicitly grant repository selectors
and `context:read`, `evidence:read`, or `episode:write` scopes. Create, rotate,
and revoke them through the authenticated credential-administration boundary;
the plaintext value is returned only at issuance. Rotation supports at most a
15-minute overlap. After revocation commits, new requests reject the token;
in-flight requests are not cancelled.

### Helm

Render the private chart offline before a release promotion. The values schema
is `deploy/helm/acr/values.schema.json`; retain its existing-Secret and
immutable-image requirements rather than copying plaintext values into a
values file.

```bash
TEST_IMAGE_DIGEST=registry.internal/dev-health/acr-api@sha256:<64-hex-digest> \
  bash scripts/deploy/test-helm.sh \
  --values deploy/helm/acr/values-development.yaml \
  --image "$TEST_IMAGE_DIGEST"
```

For a Kind fixture that the caller has already created, exercise the actual
Helm lifecycle. The driver verifies fixture ownership and does not create or
destroy the Kind cluster:

```bash
bash scripts/docs/clean-room.sh --mode helm --cluster "$ACR_KIND_CLUSTER"
```

Helm's migration Job runs before install and upgrade. It uses a separate
migration credential and fails closed: do not deploy the API when it fails.

### Kustomize

The checked-in overlays are private caller-owned namespace templates, not
standalone defaults. Apply only through the lifecycle helper with a selected
immutable digest:

```bash
bash deploy/kubernetes/acr/scripts/apply.sh --overlay staging \
  --image registry.internal/dev-health/acr-api@sha256:<64-hex-digest>
bash deploy/kubernetes/acr/scripts/wait.sh --overlay staging
```

Validate the equivalent Kind lifecycle with:

```bash
bash scripts/docs/clean-room.sh --mode kustomize --cluster "$ACR_KUSTOMIZE_CLUSTER"
```

The Kustomize apply helper runs `acr-migrate` before it applies the API
Deployment. It references existing runtime, migration, entitlement, CA, and
registry-pull Secrets. It does not create a database, Gateway, Gateway
controller, or MCP workload.

## Migrations, upgrades, and rollback

`acr-migrate` supports only `up` and `status`; it reads
`ACR_POSTGRES_MIGRATION_DSN` or `ACR_POSTGRES_MIGRATION_DSN_FILE`.
Migration history is ordered, contiguous, checksum-validated, and advisory-lock
protected. Make migrations additive while old and new API binaries coexist.

```bash
go run ./cmd/acr-migrate status
go run ./cmd/acr-migrate up
```

There is no supported schema rollback. If an application release must be
reverted, select a previously verified immutable API digest and use the
application-only rollback helper; it never changes migration history:

```bash
bash deploy/kubernetes/acr/scripts/rollback.sh --overlay staging \
  --image registry.internal/dev-health/acr-api@sha256:<64-hex-digest> --apply
```

Promotion and private release revocation are described in
[release policy](release-policy.md). Test the promoted digest, preserve its
provenance, and rehearse an application rollback before a production upgrade.

## Backup and restore

ACR PostgreSQL is operational state; ClickHouse evidence is read-only and is
not restored by ACR. The repository does not define a backup product, retention
period, replica topology, or recovery-point/recovery-time objective. The
platform operator must own encrypted PostgreSQL backups and a restore exercise.

Before a restore exercise, record the selected backup, target environment,
PostgreSQL role privileges, and current migration status. Restore into an
isolated target, run `acr-migrate status`, verify credential revocation and
repository authorization behavior, then cut over only under the platform
operator's approved procedure. Do not use a restore to erase migration history
or to roll schema backward.

## Observability and incident response

ACR supplies bounded in-process snapshots and structured logs; it does not
ship an HTTP, Prometheus, or OpenTelemetry exporter. The hosting service owns
exporter integration, retention, alert routing, and tenancy review. Use the
metric mapping and SLOs in [observability](observability.md). Never add bearer
values, DSNs, packet content, evidence URLs, transcripts, organization IDs, or
repository names as log fields or metric labels.

Troubleshoot with safe status, error classes, and deployment logs:

| Symptom | Safe response |
| --- | --- |
| Migration Job fails | Stop the rollout; inspect sanitized Job logs, confirm the separate migration Secret and TLS DSN, then run `acr-migrate status`. Do not apply the API Deployment or attempt schema rollback. |
| CA or TLS failure | Confirm the referenced CA Secret/file is a regular owner-only file and matches the server chain; keep production origins HTTPS. Exercise the local TLS fixture instead of disabling verification. |
| `invalid_token` after rotation or revocation | Confirm the sidecar selected the intended credential source with `doctor --offline`; replace the revoked value through the credential lifecycle. Do not print the token. |
| ClickHouse probe or read failure | Treat evidence as unavailable or degraded, verify the read-only ClickHouse DSN, CA, and least-privilege role with the evidence owner, and do not write to ClickHouse. |
| Entitlement denial | Verify `agent_context_runtime` separately from the credential's scope and repository selector. An entitlement does not grant a credential permission. |

The Compose acceptance helper includes migration, CA, revoked-credential, and
ClickHouse-read-denied failure scenarios. It is the reproducible local path;
do not target a shared environment for those failures.

## Sidecar setup and client examples

The sidecar is host-local and communicates over STDIO. Configure only the API
origin and one credential source, then inspect static state without network
access:

```bash
ACR_API_URL=https://acr.internal.example \
ACR_API_TOKEN=fcacr_redacted_example \
  go run ./cmd/acr-mcp doctor --offline
go run ./cmd/acr-mcp metadata
```

`ACR_API_URL` must be an origin. Plain HTTP is permitted only for an explicit
loopback test fixture with `ACR_API_ALLOW_INSECURE_LOOPBACK=true`. For a
corporate/private CA, use `ACR_API_CA_BUNDLE` on macOS or Linux with a
restricted regular file. See [MCP client examples](examples/mcp-clients/),
then run the clean-room STDIO handshake:

```bash
bash scripts/docs/clean-room.sh --mode mcp
```

`doctor --offline` checks configuration but may report incomplete configuration
with a successful process exit; inspect its JSON `status`. The MCP tool surface
is read-only by default (`context_for_task` and `source_evidence`). Episode
writeback remains disabled unless every local and server authorization gate is
enabled; it is not an External Push substitute.

### Optional existing CodeGraph index

The sidecar may consume an existing local CodeGraph index as supplemental,
untrusted evidence. ACR does not install CodeGraph or create, refresh, repair,
or store an index. Configure `ACR_LOCAL_INDEX_PROVIDER=disabled` for explicit
hosted-only behavior, leave it at `auto` for optional discovery, or set it to
`codegraph` to require the provider attempt. The detailed bounds for timeout,
items, tokens, serialized bytes, stale policy, and the optional executable are
in [MCP sidecar](mcp-sidecar.md#optional-local-codegraph-evidence).

Only the existing index's supported read-only JSON commands are used. Do not
operate `init`, `index`, or `sync` through ACR. Missing, stale, incompatible,
timed-out, or mismatched local state remains a local degradation; operators must
not treat it as a hosted outage. `doctor --offline` reports this safely without
hosted network traffic or paths/source/index payloads. CodeGraph's unavailable
indexed commit is reported as `indexed_commit_unknown`, never inferred from the
current workspace.

Fixture harnesses prove protocol and no-upload behavior. `mcp-codegraph-live.sh
--self-test` tests the live harness itself, not a supported installed CodeGraph
or an operator's index. The residual final-wave F2 risk is live mixed evidence:
it requires a supported installed CodeGraph and a pre-existing index, captures
pre/post hashes, and proves no mutation. Do not claim that evidence before F2.

Expected-failure fixture scenarios complete only after their semantic assertions
and emit `ACR_E2E_EXPECTED_FAILURE_VALIDATED` with distinct exit codes: local
timeout `41`, hosted unavailable `42`, incompatible version `43`, and
post-response process failure `44`. A generic nonzero exit is not a valid
expected-failure result. Canonical receipts are ignored local evidence, mode
`0600`, atomically published only after the source revision remains clean, and
record the harness/binary hashes, scenario verdict, and (for mixed MCP) rejected
writeback/session validity/zero hosted episode posts.

## Documentation acceptance drivers

The documentation verifier is offline: it checks local links, documented
commands and environment names, Helm schema/JWKS terminology, and unsafe
claims. Clean-room drivers run real fixtures when required infrastructure is
available and otherwise self-skip without claiming execution:

```bash
bash scripts/docs/verify.sh
bash scripts/docs/clean-room.sh --mode compose --compose ../compose.yml --overlay deploy/compose/acr.compose.yml
bash scripts/docs/clean-room.sh --mode helm --cluster "$ACR_KIND_CLUSTER"
bash scripts/docs/clean-room.sh --mode kustomize --cluster "$ACR_KUSTOMIZE_CLUSTER"
bash scripts/docs/clean-room.sh --mode mcp
```
