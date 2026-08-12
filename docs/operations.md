# ACR developer and operator guide

This guide is for ACR service owners and the developers who run its
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

Use the Compose acceptance driver for a full local edge-TLS fixture. Its root
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

## Hosted deployment configuration

### TLS, origins, and web assertions

Production caller configuration must use immutable image digests. Internal
PostgreSQL, ClickHouse, and entitlement transports follow their explicit DSNs
and origins and may use ordinary private-network plaintext. When an internal
dependency explicitly selects TLS, configure its CA bundle as needed. The
hosted API's externally published origin remains configured by the caller's
TLS-terminating Gateway and is not an ACR API runtime default.

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

### Context Fabric projection worker (`acr-projector`, CHAOS-3753)

`cmd/acr-projector` is a dedicated binary, independently deployed and scaled
from `acr-api`, that runs `internal/contextfabric/projectionrun.Coordinator`
against every configured organization: it reads canonical Dev Health data
(`internal/contextfabric/devhealthsource`, ClickHouse + `acr.agent_episodes`)
and applies it to the graph backend. The graph backend is FalkorDB,
self-hosted (`internal/contextfabric/falkorgraph`,
[ADR 0009](adr/0009-context-fabric-falkordb-graph-adapter.md)), and needs
no external credential to run. See
[the design note](design/context-fabric-projection-worker.md) for the
projection worker's own full shape and open follow-ups (Team/Project
projection, org auto-discovery), and
[the FalkorDB adapter design note](design/context-fabric-falkordb-adapter.md)
for the backend's own shape.

It is compiled into the same image as `acr-api` (`Dockerfile`'s `acr-api`
target also contains `/usr/local/bin/acr-projector`, exactly like
`acr-migrate`) and run with its entrypoint overridden — Compose's
`acr-projector` service and Helm's `contextFabric.projector.enabled`
Deployment both do this directly; neither introduces a helper script.

**Two independent enable/disable levers, on purpose:**

- `contextFabric.projector.enabled` (Helm) / the Compose service itself —
  whether the workload exists at all.
- `ACR_CONTEXT_FABRIC_PROJECTION_ENABLED` (`contextFabric.projector.projectionEnabled`
  in Helm) — the running process's own master switch. When false, the
  coordinator loop never starts; `/healthz`/`/readyz` still serve, reporting
  `projection_enabled: false`. This is the rollback lever: flip it off
  (or scale the Deployment to zero) without touching `acr-api` or the graph
  backend.

Both Compose and Helm default `ACR_CONTEXT_FABRIC_PROJECTION_ENABLED` to
`false`. Reaching a healthy, running-but-disabled `acr-projector` is
therefore the expected out-of-the-box state — the same "an unset dependency
must never fail closed" posture as `falkorgraph.Configured`, because:

- the `falkordb` Compose service (ADR 0009) is profile-gated
  (`context-fabric-graph`), disabled unless an operator opts in, so local
  development and CI do not pay for a graph database by default;
- `ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS` (`contextFabric.projector.orgIds`)
  is an explicit allowlist, empty by default — see the design note for why
  this starts explicit rather than auto-discovered from `client_credentials`.

To actually run it: bring up the `falkordb` Compose service
(`docker compose --profile context-fabric-graph up`), set
`ACR_CONTEXT_FABRIC_PROJECTION_ENABLED=true`, supply
`ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS`, and point
`ACR_CONTEXT_FABRIC_FALKOR_ADDR` at it (e.g. `falkordb:6379`) — FalkorDB is
self-hosted and needs no external credential at all (ADR 0009);
`ACR_CONTEXT_FABRIC_FALKOR_PASSWORD` stays optional and empty by default,
matching FalkorDB's own no-auth default. Both `cmd/acr-projector` and
`cmd/acr-api`'s hosted runtime composition construct a `falkorgraph.Adapter`
from this same env contract; Helm's `contextFabric.falkor.*` values wire the
projector Deployment the same way. Reads (`internal/contextfabric.GraphReader`,
the investigation endpoint) are a completely independent enablement:
`ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED` (`config.GraphReadsEnabledEnvVar`),
wired by CHAOS-3755's hosted composition
(`internal/runtime/hosted.buildContextFabricInvestigator`). Set it to `true`
alongside the same graph backend configuration above to register `POST
/api/v1/context-fabric/investigations`; leave it `false` (the default) and
the route stays registered but returns 503 (`api.handleRuntimeUnavailable`)
for every request. Answering also needs a model provider, which is a third
independent enablement — see the next section.

**Single-flight per organization** (the CHAOS-3753 acceptance amendment):
the coordinator holds a PostgreSQL advisory lock
(`projectionrun.PostgresOrgLocker`, `pg_try_advisory_lock`) for an
organization's entire multi-source projection pass, so concurrent
`acr-projector` replicas — and overlapping ticks within one replica — can
never race two sources' writes for the same organization. `falkorgraph`'s
own merge is a single atomic `MERGE ... SET n += $attrs` statement under
FalkorDB's own row lock (ADR 0009), but the per-organization serialization
requirement is kept regardless: other batch-level ordering guarantees still
depend on it, not just single-node attribute-merge atomicity.

**Rebuild:**

```bash
acr-projector rebuild --org <organization-id>
```

Purges the organization's backend state (`ProjectionBackend.PurgeOrganization`)
then resets every configured source's checkpoint to the empty cursor, under
the same single-flight guard as an ordinary tick. `devhealthsource` treats an
empty cursor as "start a bounded, complete-enumeration snapshot" (see the
design note), so the next `serve` tick replays canonical state from scratch
exactly as it would for an organization that has never been projected. It
runs regardless of `ACR_CONTEXT_FABRIC_PROJECTION_ENABLED` (an operator
invoking it has already made that call) but still needs Postgres, ClickHouse,
and a configured graph backend to do anything.

Crash-resumable: a durable marker (`acr.context_fabric_projection_rebuild_markers`)
commits before the purge and clears only after every checkpoint is
confirmed reset. If `acr-projector` crashes mid-rebuild, ordinary `serve`
ticks detect the marker on their own and resume the same purge-reset-clear
sequence automatically -- no manual `rebuild --org` re-invocation is
required, and no tick ever runs incremental projection against a
purged-but-not-reset organization in the meantime.

The active adapter's own lifecycle contract (projection, idempotent replay,
retrieval, tombstones, watermark, purge, organization isolation) is proved
two ways. `internal/contextfabric/falkorgraph` (ADR 0009, the current
backend) proves deterministic helpers directly in `pure_test.go` and the
full live lifecycle — including cross-organization isolation, a stale
out-of-order tombstone being skipped, entity metadata surviving a
relationship-only write, and a write after purge re-bootstrapping correctly
— against a real FalkorDB container started per test via
`testcontainers-go`, **with no environment gate**:

```bash
go test -count=1 -run TestLiveFalkorDBContextFabricLifecycle ./internal/contextfabric/falkorgraph -v
```

This always runs, in ordinary CI included — FalkorDB needs no external
credential, so there is no environment gate to skip.
`cmd/acr-projector/runtime_falkordb_live_test.go` additionally proves the
real runtime-composition path end to end: `openRuntime` against a real
FalkorDB and PostgreSQL, one real `Coordinator.Tick`, the checkpoint
advancing in Postgres, nodes present via a raw `GRAPH.RO_QUERY`, and a
second `falkorgraph.Adapter` (built the way `acr-api`'s hosted composition
builds one) resolving the projected subject back out. `acr-projector`'s own
checkpoint store
(`internal/contextfabric/pgprojection`), org-lock, and coordinator
single-flight/failure-isolation/backoff behavior are proved against real
PostgreSQL (`testcontainers`) and fakes standing in for the graph backend
and canonical source, following the same pattern.

### Context Fabric model provider (BYO LLM, CHAOS-3770)

The investigation endpoint interprets the question and synthesises the
answer through `internal/contextfabric.ModelRuntime`. Hosted composition
builds it in `internal/runtime/hosted.newContextFabricModelRuntime` from
`internal/contextfabric/modelprovider`, which is the only place in this
repository that constructs a production `genkit.Genkit` instance.

**BYO LLM is the supported shape**, so the configuration surface names a
provider, not a vendor: a provider kind, a base URL, a model id, and a
credential. Pointing it at any OpenAI-compatible endpoint — a customer's
own OpenAI key, a corporate gateway, or a self-hosted vLLM/Ollama/llama.cpp
server — is a pure configuration change. No code change, no new plugin, no
new build.

| Variable | Default | Meaning |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_MODEL_API_KEY` / `_FILE` | *(unset)* | Bearer credential, `KEY`/`KEY_FILE` convention (`config.SecretValue`). Required unless a base URL is set. |
| `ACR_CONTEXT_FABRIC_MODEL_BASE_URL` | `https://api.openai.com/v1/` | OpenAI-compatible API root. Set this for BYO. |
| `ACR_CONTEXT_FABRIC_MODEL_PROVIDER` | `openai` | Plugin namespace, recorded verbatim as `ModelExecutionReceipt.Provider`. Give a BYO endpoint its own stable name so replay can tell receipts apart. |
| `ACR_CONTEXT_FABRIC_MODEL` | `gpt-5-nano` | Bare model id (no provider prefix). Ids containing `/`, e.g. `meta-llama/Llama-3.1-8B-Instruct`, are supported. |
| `ACR_CONTEXT_FABRIC_MODEL_FALLBACK` | *(unset)* | Second, stronger model on the same provider, tried when the primary call fails or returns output that does not validate. Unset by default (a fallback is a second billable call), but **effectively required in practice — set it to `gpt-5.6-luna` alongside the `gpt-5-nano` default.** See the measurements below. |
| `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT` | `45s` | Bounds one generation attempt (1s–2m). |
| `ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS` | `2` | Attempts `genkitruntime` makes per operation (1–3). |
| `ACR_CONTEXT_FABRIC_MODEL_MAX_TRANSPORT_RETRIES` | `2` | The OpenAI SDK's own retry loop *within* one attempt (0–5). Set `0` to make `genkitruntime` the single retry owner — the right choice for a local BYO server. |
| `ACR_CONTEXT_FABRIC_MODEL_ALLOW_INSECURE_BASE_URL` | `false` | Permits a plaintext `http://` base URL. Only for a loopback or private-network BYO server: the credential travels as a bearer token on every request. |

Two behaviours matter operationally, and they are opposites on purpose:

- **No provider configured is a supported state, not an error.**
  `modelprovider.Configured` returns false, the model runtime stays nil,
  and `RuntimeQuestionInterpreter`/`RuntimeAnswerSynthesizer` degrade every
  request to a clean 503 `upstream_unavailable` (`ErrModelUnavailable`).
  The route stays registered, authorized and audited, and the graph and
  canonical-fact layers stay real and live. This is the CHAOS-3755
  behaviour, and it is regression-tested
  (`hosted.TestNewContextFabricModelRuntime_keepsTheCleanFiveOhThreeWithoutACredential`).
- **A provider configured but mis-specified fails startup.** A bad URL, an
  out-of-band timeout, a conflicting `KEY`/`KEY_FILE` pair, or an
  unreadable secret file aborts composition with an error naming the
  variable. An operator who asked for a provider must find out at startup,
  not one 503 at a time.

Ambient `OPENAI_API_KEY` / `OPENAI_BASE_URL` are deliberately **not**
consulted, and cannot leak in: the OpenAI SDK seeds itself from them, so
`modelprovider` always passes the credential and base URL explicitly to
override that. Opting this service into a paid provider is an ACR
configuration decision, never something inherited from the process
environment.

Provider failures are classified into the `ErrModelRateLimited` /
`ErrModelOutput` / `ErrModelUnavailable` taxonomy that alerting keys off,
and the classification is proved against the real plugin and the real SDK
transport replaying recorded provider responses
(`modelprovider.TestNew_classifiesRecordedProviderFailures`): 429 and quota
exhaustion map to `ErrModelRateLimited`; 401, 403 and 5xx map to
`ErrModelUnavailable`; output that violates the response schema, or is not
JSON at all, maps to `ErrModelOutput`. The classified error carries only a
class and a fixed message — no provider response body, prompt fragment, or
endpoint ever travels into logs, receipts, or telemetry built from it.

**`ACR_REQUEST_TIMEOUT` must be raised before enabling investigations.** It
defaults to **15s**, and it bounds the whole HTTP request. A real
investigation is two sequential model calls (interpret, then synthesise) and
was measured end-to-end through the endpoint at **55–80s** against
`gpt-5-nano`. Left at the default, every investigation returns 504 before
the model answers, regardless of how well the model is doing. Size it above
`ACR_CONTEXT_FABRIC_MODEL_TIMEOUT` × `ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS`
× 2 (the two operations), plus headroom — with the defaults (45s × 2 × 2)
that is a worst case of 180s. The timeout is global to the API, not
per-route, so raising it also loosens the bound on every other route; a
per-route timeout is the cleaner fix and is not implemented yet.

**Model choice matters, and `gpt-5-nano` alone is not enough.** Measured
live against `gpt-5-nano` (the CHAOS-3770 acceptance probes, both skipped
unless `ACR_TEST_MODEL_API_KEY` is set —
`go test ./internal/contextfabric/modelprovider -run Live` for the runtime
and `go test ./internal/api -run LiveEndpoint` for the endpoint):
interpretation passed ACR's validator on every run, but synthesis — the
strictest validator in the pipeline — did not. `gpt-5-nano` alone answered
**2 of 21** endpoint attempts; the richer the synthesis input (graph paths
plus several canonical facts), the worse it did, so the runtime-level rate of
roughly one in three overstates it for real traffic.

Every failure was a clean, correctly classified 502
`upstream_invalid_output`, never a wrong answer: value-level closure rejects
what it cannot bind to a canonical fact. So the failure mode is availability,
not correctness — but at 1 in 16 the endpoint is unusable.

**The fallback is required, not optional.** Invalid output is deliberately
**not** retried (`genkitruntime` fails closed on a schema-shaped failure
rather than re-rolling the same input); the fallback model is the only
mitigation, and `genkitruntime` invokes it on invalid output as well as on
transport failure. With `ACR_CONTEXT_FABRIC_MODEL_FALLBACK=gpt-5.6-luna` the
same endpoint answered **6 of 6**, at 55–80s per request. Set it in every
deployment expected to answer. Each fallback is a second billable call and is
recorded as `fallback_used` on the receipt; tracking that rate per model is
the `ModelReceiptSink` evaluator's job (CHAOS-3756), and a sustained high
rate is the signal to promote the fallback to primary.

### Helm

Render the private chart offline before a release promotion. The values schema
is `deploy/helm/acr/values.schema.json`; retain its existing-Secret and
immutable-image requirements rather than copying plaintext values into a
values file.

```bash
TEST_IMAGE_DIGEST=ghcr.io/full-chaos/dev-health-acr/acr-api@sha256:<64-hex-digest> \
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
When `config.requireBackingStores=true`, set `config.deviceVerificationUrl` to
the absolute Dev Health Web approval page (for example,
`https://health.example.com/acr/device`). The chart rejects a backed release
that omits this runtime-required value.

Development and test renders select the offline local entitlement provider when
`config.entitlement.url`, `credentials.entitlementToken`, and
`config.entitlementCaBundle` are all omitted. This is the default in
`values-development.yaml` and the local Kind Helm driver. Supplying a complete
URL and token Secret reference selects the remote provider instead. Staging and
production reject local or partial entitlement configuration before rendering;
their remote startup and request-time checks remain fail closed.

### Kustomize

The checked-in overlays are private caller-owned namespace templates, not
standalone defaults. Apply only through the lifecycle helper with a selected
immutable digest:

```bash
bash deploy/kubernetes/acr/scripts/apply.sh --overlay staging \
  --image ghcr.io/full-chaos/dev-health-acr/acr-api@sha256:<64-hex-digest>
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
  --image ghcr.io/full-chaos/dev-health-acr/acr-api@sha256:<64-hex-digest> \
  --apply
```

Promotion, publication, and release revocation are described in
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
| Migration Job fails | Stop the rollout; inspect sanitized Job logs, confirm the separate migration Secret and configured DSN, then run `acr-migrate status`. Do not apply the API Deployment or attempt schema rollback. |
| CA or TLS failure | For an externally exposed ACR origin or an explicitly TLS-enabled dependency, confirm the referenced CA Secret/file is a regular owner-only file and matches the server chain. Keep external origins HTTPS; private service transports follow their configured HTTP origins or DSNs. Exercise the local edge-TLS fixture instead of disabling edge verification. |
| `invalid_token` after rotation or revocation | Confirm the sidecar selected the intended credential source with `doctor --offline`; replace the revoked value through the credential lifecycle. Do not print the token. |
| ClickHouse probe or read failure | Treat evidence as unavailable or degraded, verify the read-only ClickHouse DSN and least-privilege role with the evidence owner, verify a CA only when that DSN explicitly enables TLS, and do not write to ClickHouse. |
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
loopback origin; no additional insecure-transport flag is required. The legacy
`ACR_API_ALLOW_INSECURE_LOOPBACK` variable remains accepted for compatibility. For a
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
bash scripts/clients/clean-room.sh --release-dir .tmp/context-fabric-release --clients opencode,claude-code,codex,cursor
bash scripts/docs/clean-room.sh --mode compose --compose ../compose.yml --overlay deploy/compose/acr.compose.yml
bash scripts/docs/clean-room.sh --mode helm --cluster "$ACR_KIND_CLUSTER"
bash scripts/docs/clean-room.sh --mode kustomize --cluster "$ACR_KUSTOMIZE_CLUSTER"
bash scripts/docs/clean-room.sh --mode mcp
```

The client clean-room consumes a verified Task18 `acr-mcp` archive and uses
temporary HOME/config roots. It exercises exact `acr-mcp serve` registration,
offline doctor, explicit context/evidence, update, uninstall, residue, and
unrelated-config preservation for the four bundled packages. Unix/Linux/macOS
are the exercised platforms. Cursor package/fixture validation is mandatory;
native Cursor is conditional and reports its installed/not-installed state.
Cursor Windows/NTFS lifecycle remains deferred to CHAOS-3058. No client guide
claims a production release, credential storage, CodeGraph initialization, or
default pre-plan/writeback.
