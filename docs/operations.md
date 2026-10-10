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
| Agent integration | A locally installed `acr-mcp` process over STDIO, or the opt-in hosted `acr-mcp` (stateless Streamable HTTP, Kubernetes only) |
| External fact ingestion | External Push; not the ACR API |

The deployment artifacts require caller-provided PostgreSQL, ClickHouse,
entitlement, Gateway, and Secrets. They do not provision those dependencies,
and `acr-mcp` runs on Kubernetes only as the opt-in hosted workload described in
`deploy/README.md`; there is no Compose service for it.

## Developer getting started

Run the offline contract and documentation gates before changing contracts,
deployment artifacts, or this guide:

```bash
bash scripts/docs/verify.sh
make contract-test
make verify
```

The stock API binary refuses to start at all without the complete hosted
runtime bundle, unless the explicit local-development opt-out is set. It is
useful for process and probe development this way, but not as a production
configuration:

```bash
# Backing stores are required by default in every environment now; probing
# the stock binary without one needs the explicit ACR_LOCAL_COMPOSITION_READY
# dev opt-out (development only) -- without it the process refuses to start.
# The listen address stays loopback-only by default regardless of this flag
# (they are independent knobs): do NOT set ACR_ADDR to a wildcard here, or
# this probe becomes reachable from every interface on the host.
ACR_LOCAL_COMPOSITION_READY=true go run ./cmd/acr-api serve
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

`make verify`'s `go test ./...` also starts real, ephemeral ClickHouse
containers via `testcontainers-go` for
`internal/contextfabric/devhealthfacts`/`devhealthsource`'s own integration
tests, with no environment gate; `cmd/acr-api`'s hosted fixture runs the
same way but only under `make hosted-integration`
(`ACR_HOSTED_INTEGRATION=1`), CI's own separate job. The image tag and the
minimum accepted `SELECT version()` floor are one constant,
`internal/chfixture` (CHAOS-4549, ruled 2026-08-29 "26"): CI runs against
the same major.minor line as prod (26.7+; prod floats on `latest`,
CHAOS-4519) instead of an old engine two majors behind. The portability
contract that old floor used to enforce as a side effect — every
`JOIN ... ON` clause is a plain column-equality conjunction, never `OR`, a
function-predicate, or a bare literal — is enforced statically instead, by
`internal/chfixture.JoinONViolations` and
`TestChaos4549AllJoinOnClausesArePortable` in both `devhealthfacts` and
`devhealthsource`.

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
assertion because the file is reread. Used assertion ids live in PostgreSQL
(`acr.web_assertion_replays`), so every acr-api pod refuses a replay; the
runtime role needs SELECT, INSERT and DELETE on it (`grant-runtime-acl`).

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

When a token is lost, an operator revokes the credential without the bearer:
`acr-api credentials revoke --org-id <org> --credential-id <id> [--reason <text>] [--actor <id>]`
(runtime DSN from `ACR_POSTGRES_DSN`; find the id with `credentials list --org-id <org> --json`).
It uses the same storage path as the self-revoke route, so the token is
rejected at once. The audit event records actor type `operator` (actor id
defaults to `operator`) and the reason. An unknown id exits non-zero with
`not found`; an already-revoked id exits non-zero with `already revoked`.

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

**Readiness during a cold organization's first projection.** `/readyz` on
`acr-projector` is backend health plus checkpoint/graph agreement: the
`postgres`, `clickhouse` and `falkordb` checks, and `projection_liveness`,
which reports not-ready only when a durable checkpoint carries a backend
watermark that the graph no longer holds. Drain progress is never a
readiness input, and a tick that runs out of time does not stop the process
(`drain_yield_reason` is a log field; the next tick resumes). A large first
projection therefore stays Ready while it drains.
`TestLivenessCheckStaysLiveWhileColdOrgFirstProjectionDrains` pins this. Probe
values of record (`deploy/helm/acr/templates/projector-deployment.yaml`):
readiness and liveness `periodSeconds: 10`, `timeoutSeconds: 3`,
`failureThreshold: 3`; startup probe on `/readyz`, `periodSeconds: 5`,
`failureThreshold: 30`. The checks run in series inside one probe request, so
a slow graph read can exhaust the 3 s timeout; if a projector log shows a
`/readyz` 503 during a first projection, report it with the log line.

Both Compose and Helm default `ACR_CONTEXT_FABRIC_PROJECTION_ENABLED` to
`false`. Reaching a healthy, running-but-disabled `acr-projector` is
therefore the expected out-of-the-box state — the same "an unset dependency
must never fail closed" posture as `falkorgraph.Configured`, because:

- the `falkordb` Compose service (ADR 0009) is profile-gated
  (`context-fabric-graph`), disabled unless an operator opts in, so local
  development and CI do not pay for a graph database by default;
- `ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS` (`contextFabric.projector.orgIds`)
  is a static allowlist, empty by default, and organization auto-discovery
  (below) is off by default — so an out-of-the-box projector has no
  organizations to project and says so.

**Organization auto-discovery (`ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY`,
`contextFabric.projector.orgDiscovery`, default `false`).** With it on, the
projector re-reads the eligible organization set from ClickHouse at the
start of every poll and projects the **union** of that set and the static
allowlist, so a newly onboarded tenant gets a graph without a redeploy.
With it on, an empty `ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS` is accepted;
with it off, enabling projection without an allowlist is still refused at
startup.

Discovering an organization id is not the same as giving it a graph. An
organization is admitted only if it owns at least one `repos` row and its
newest canonical activity (`repos.last_synced`, `work_items.updated_at`,
`git_pull_requests.last_synced`) falls inside
`ACR_CONTEXT_FABRIC_PROJECTOR_ORG_ACTIVITY_WINDOW`
(`contextFabric.projector.orgActivityWindow`, default `720h`). `0s`
disables the activity condition; a negative value is refused at startup.
`ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY_DENY_IDS`
(`contextFabric.projector.orgDiscoveryDenyIds`) excludes named
organizations outright. All three filter the DISCOVERED set only —
`ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS` is an always-include allowlist and
is never filtered, so an organization you pin by hand is projected even if
it is dormant or denied.

What to watch once it is on:

- `context_fabric: projection organization discovery` (Info, one per tick)
  carries `org_discovery_outcome=succeeded`, `orgs_static`,
  `orgs_discovered`, `orgs_effective`, `orgs_new`, `orgs_skipped` and a
  per-reason count (`orgs_skipped_no_repo`, `orgs_skipped_inactive`,
  `orgs_skipped_denied`) present on every line, at zero when a reason did
  not fire. A tenant appearing shows up as `orgs_new` > 0 on exactly one
  tick.
- `context_fabric: projection organization skipped` (Info) names each
  excluded organization with its `org_skip_reason`. It is capped at 20 per
  tick — `skipped_truncated: true` on the summary line says the cap bit —
  but the window ROTATES, so every excluded organization is named within
  `ceil(skipped/20)` ticks (about a minute at the default poll interval).
  The per-reason counts are never capped.
- `org_discovery_outcome=failed` on a deployment with more than 10,000
  organizations means the eligibility read came back saturated and the set
  could not be known to be complete. The projector keeps its last-known
  set and adopts nothing new until that is resolved — a capacity decision,
  not a limit to raise silently.
- A tenant you expected and do not see: check `orgs_skipped_inactive`
  first. Both data conditions are reversible with no operator action — one
  new row inside the window admits the organization on the very next tick.
- `context_fabric: projection organization discovery failed` (Warn) carries
  the same fields plus `failure_class`. `org_discovery_outcome=failed`
  means the last-known set was retained and the tick went ahead normally —
  discovery never shrinks the served set on a failed read.
  `failed_no_prior_set` means discovery has never yet succeeded in this
  process, so only the static allowlist is being served.
- `acr-projector rebuild --org` / `rollback --org` refresh discovery once
  before admitting the organization, so a discovered tenant can be rebuilt
  by hand without being added to the allowlist first.

To actually run it: bring up the `falkordb` Compose service
(`docker compose --profile context-fabric-graph up`), set
`ACR_CONTEXT_FABRIC_PROJECTION_ENABLED=true`, supply
`ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS` (or set
`ACR_CONTEXT_FABRIC_PROJECTOR_ORG_DISCOVERY=true`), and point
`ACR_CONTEXT_FABRIC_FALKOR_ADDR` at it (e.g. `falkordb:6379`) — FalkorDB is
self-hosted and needs no external credential at all (ADR 0009);
`ACR_CONTEXT_FABRIC_FALKOR_PASSWORD` stays optional and empty by default,
matching FalkorDB's own no-auth default. Both `cmd/acr-projector` and
`cmd/acr-api`'s hosted runtime composition construct a `falkorgraph.Adapter`
from this same env contract; Helm's `contextFabric.falkor.*` values wire
both the projector and `acr-api` Deployments the same way (CHAOS-3774).

**Local Compose bring-up (CHAOS-4192): always pass `--env-file ops/.env`
to every `docker compose` command that starts or recreates `acr-projector`
or `acr-api`.** Without it, any `${VAR:-}`-style substitution in a local
`compose.override.yml` (e.g. `ACR_CONTEXT_FABRIC_EMBED_API_KEY:
${OPENAI_API_KEY:-}`) resolves to the DEFAULT (blank) instead of the real
value `ops/.env` carries -- even though `ops/.env` itself has always had
a real value, a shell that started `acr-projector` without `--env-file`
substituted blank silently. The observed incident: a full graph rebuild
ran to completion with the embedder CONFIGURED (base URL set) but the
credential blank, and the only symptom was `embedded:0` on every
projection batch -- every organization's existing vectors cleared,
one batch at a time, with no error and no failed health check. This class
is now also guarded in code (`internal/contextfabric/embedprovider`'s
`ACR_CONTEXT_FABRIC_EMBED_ALLOW_NO_CREDENTIAL`, CHAOS-4192): a configured
embedder with a blank credential now fails loudly at startup instead of
silently degrading, but a correct `--env-file` is still what makes the
credential resolve to the real value in the first place, and this class
also affects `acr-api`'s `ACR_CONTEXT_FABRIC_MODEL_API_KEY` the same way
(no code-level guard added there in this pass).

**CHAOS-4147 item 3 / CHAOS-4259: a TRANSIENT embed failure (a network
blip, a rate limit, one bad tick) no longer clears vectors on the first
try.** `embedProjectionBatch` bounds a short retry
(`ACR_CONTEXT_FABRIC_FALKOR_EMBED_FAILURE_MAX_RETRIES`, default `2`, with
`ACR_CONTEXT_FABRIC_FALKOR_EMBED_FAILURE_RETRY_BACKOFF` between attempts,
default `200ms`) before falling back to the existing clear-and-degrade
behavior -- unchanged for a PERSISTENT failure (an auth/4xx error, or this
package's own response-shape/dimension/model-identity checks), which is
never retried, because an identical request gets an identical answer.
Separately, a SUSTAINED run of
`ACR_CONTEXT_FABRIC_FALKOR_EMBED_FAILURE_ESCALATE_AFTER` (default `5`)
consecutive failing batches for one organization now escalates to an
ERROR-level `RecordVectorProjectionEmbedFailuresEscalated` signal, distinct
from the routine per-batch WARN a single cleared batch already emits --
so an outage like the one above is loud well before an operator would
otherwise notice only by scrolling WARN lines. All three are ACTIVE by
default in every real deployment (`ConfigFromEnv` applies the `2`/`200ms`/`5`
defaults above whenever the corresponding env var is unset) -- unset does
NOT mean disabled. Only a `Config` literal built directly, bypassing
`ConfigFromEnv` entirely (the shape most existing unit tests use), gets the
zero value and stays byte-identical to pre-CHAOS-4259 behavior (no retry,
no escalation); no shipped composition root does this.

If you maintain the workspace root's own `compose.override.yml` (untracked
local config, outside every repo, so it carries no durable comment of its
own): its `falkordb` service's volume must mount at the SAME path the
image's `FALKORDB_DATA_PATH` writes to -- see `deploy/compose/acr.compose.yml`'s
own `acr_falkordb_data` volume comment (CHAOS-4055) for the exact path and
copy it verbatim, do not shorten or guess it. Mounting at any other path
(a plausible-looking `/data`, for instance) is a **silent persistence
no-op**: the vendored image's `run.sh` entrypoint never writes there, so
`docker compose down`/recreate loses every organization's graph with no
error at any point, discovered only when a later read comes back empty or
a rebuild that "shouldn't have been necessary" turns out to be the only
way to recover. This doc is the durable record of that requirement since
the override file itself is not committed anywhere.

Reads (`internal/contextfabric.GraphReader`,
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

**Build-aside-and-swap (CHAOS-3898 S2a-2), opt-in:** the description above
is the LEGACY path — `PurgeOrganization` in place, then an incremental
replay over subsequent ticks with the graph briefly empty. Setting
`ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED=true` (on **both** `acr-api`
and `acr-projector` — the two processes must agree on whether an
organization's graph key resolves through the epoch pointer) switches
`rebuild` to build the replacement graph ASIDE, at a freshly allocated
epoch, while the CURRENT graph keeps serving unchanged and complete
throughout; the new epoch only becomes visible to readers once every
required source reports completion and a single atomic pointer flip
happens. The old epoch is retained for a grace window (rollback insurance),
then retired (`GRAPH.DELETE` + its own checkpoint set) once every reader's
cache lease and enforced request deadline have provably drained. Related
env vars (all optional; unset uses the documented default, `ConfigFromEnv`
refuses a garbage value loudly):

| Variable | Default | Meaning |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED` | `false` | Master switch. `false` (both binaries) is byte-identical to pre-CHAOS-3898 behavior. |
| `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_LEASE` | `1m` | KeyResolver's per-organization cache TTL (bounded at 10m). |
| `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_REQUEST_DEADLINE` | `30s` | The enforced per-investigation-request deadline the drain-bound argument depends on — keep this **>=** `ACR_REQUEST_TIMEOUT` (`acr-api`'s own actual enforced request deadline; `acr-projector` cannot read it directly, being a separate process). |
| `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_GRACE_WINDOW` | `24h` | How long a flipped-away epoch is retained before it becomes eligible for retirement. |

```bash
acr-projector rebuild --org <organization-id>    # begin (or resume) a build-aside epoch
acr-projector rollback --org <organization-id>    # restore the PREVIOUS epoch during its grace window
```

`rollback` is refused outside the grace window (the prior epoch has already
been retired, or none is open) — it is not a general-purpose "undo",
only the specific insurance window a flip opens.

**A build this binary can never activate is aborted.** A build-aside epoch
that holds data recorded under a source version other than the running
binary's (a deploy that bumps a source version while a build is open, or a
source the binary no longer configures) is refused at the flip. The projector
then aborts that build on the same tick: the organization goes back to
`serving` its unchanged active epoch, ordinary ticks resume there, and the
refused epoch is queued for retirement (reason `build_aborted`; its graph key
and checkpoint set are deleted after the same drain bound as any retired
epoch). Look for the Warn `context_fabric: build aborted because the epoch
activation guard refused its epoch` (organization, refused epoch, source,
recorded and current source version) and the `context_fabric: graph epoch
build aborted` signal. Nothing rebuilds on its own: run
`acr-projector rebuild --org <organization-id>` to build again under the
running binary. A guard that could not read the epoch's checkpoint rows does
not abort; that build stays open and is retried on the next tick.

**A rebuild is REQUIRED after deploying CHAOS-3781** (`devhealthsource`
`ClickHouseSourceVersion` v3 → v4). Every producer now emits a valid-time
window (`valid_from` / `valid_to`) derived from its source row's own
interval columns, and the graph read side admits by that window when a
question asks about a past time.

An organization projected before this deploy holds nodes and edges with NO
window at all. The read side admits an unbounded element at *every*
requested time, so an un-rebuilt graph answers a historical question as
though everything in it had always been true. The version bump makes this
impossible to miss rather than silent: `ProjectionWorker.RunOnce` refuses
every tick with `ErrProjectionSourceVersionChanged` until the rebuild runs,
so the graph is structurally untouched in the meantime — a *fresh*
investigation during that window reads exactly the same pre-rebuild graph
it would have before, and no historical answer is fabricated.

Run `acr-projector rebuild --org <organization-id>` for every projected
organization after deploying. Until then, historical questions still
answer, but only from canonical fact sources; the graph half contributes
whatever unbounded elements it holds, disclosed in the answer's coverage as
`context-fabric:graph-validity-windows`.

**A rebuild is likewise REQUIRED after deploying CHAOS-3833 phase 2**
(`ClickHouseSourceVersion` v4 → v5, `TeamsProjectsSourceVersion` v1 → v2,
embed composition tag `t1` → `t2`). The producers emit the embed-text v2
fields (ticket-key aliases, PR body heads, joined PR titles, pipeline
names, tags, project keys) and the per-kind search-text templates compose
them; an already-projected graph holds text — and vectors — no current
recomposition could reproduce. Three independent fences make the window
safe until the rebuild runs: `ErrProjectionSourceVersionChanged` refuses
every projection tick, the tagged identity stamp fails every stored
vector closed to lexical retrieval, and the persisted
`embed_retrieval_identity` reuse dimension stops stored answers from
being reused across the change (see the two-phase rollout gate above —
phase 1 must be fully drained BEFORE this deploy). Run
`acr-projector rebuild --org <organization-id>` for every projected
organization; the rebuild reprojects and re-embeds in one pass.

**A rebuild is likewise REQUIRED after deploying CHAOS-3835**
(embed composition tag `t2` → `t3`, the T5 id-only skip decision —
`isPureIdentifierSubject`, `id_only.go`). **This deploy does NOT bump
`ClickHouseSourceVersion` or `TeamsProjectsSourceVersion`** — unlike every
rebuild above, it changes no producer field; T5's decision reads fields
the graph already has (`pipeline_name`, `branch`, and the aliases/previous
names `retrievalHandles` composes), only whether a row gets EMBEDDED at
all. That means `ErrProjectionSourceVersionChanged` does **not** fire this
time: ordinary `serve` ticks keep running normally, and the ONLY fence
protecting correctness is the tagged identity stamp — a `t2`-tagged vector
fails `verifyStoredEmbedderIdentity` under a `t3`-configured adapter, so a
vector-enabled organization degrades to lexical-only retrieval on this
deploy, immediately and safely (never a stale or mismatched vector
served).

The operational consequence: that degradation is **not
self-healing**. Every rebuild above eventually resolves itself because its
`SourceVersion` fence forces a full reproject on the next tick regardless
of operator action; this one has no such backstop. `devhealthsource`'s
incremental cursor only revisits a row whose CANONICAL SOURCE changed
since the last sync — a `ci_pipeline_run` row that is already fully
synced and stays unchanged is never handed to `collectEmbedTargets` again
by an ordinary tick, so its stale `t2` vector and identity stamp are never
refreshed on their own. Left un-rebuilt, a vector-enabled organization
stays lexical-only **indefinitely**, not just until the next tick. Run
`acr-projector rebuild --org <organization-id>` for every vector-enabled
organization after this deploy; the persisted `embed_retrieval_identity`
reuse dimension stops stored answers from being reused across the tag
change in the meantime, the same mechanism as CHAOS-3833 phase 2 above.

Expect the rebuild to report **fewer embedded subjects than before**, plus
a nonzero id-only skip count (`RecordVectorProjection`'s `skippedIDOnly`
field, surfaced at Info when nonzero) for organizations with
`ci_pipeline_run` rows. This is the T5 population the skip targets — rows
whose `pipeline_name`/`branch`/aliases carry no content beyond a bare
identifier, roughly 22% of that kind's live corpus per the embed-text spec
v2 measurement — correctly excluded from embedding, not evidence of lost
data: those rows keep their ordinary lexical `search_text` and stay fully
reachable by exact and fulltext retrieval; only the (previously noisy)
vector is withheld.

**This SAME CHAOS-3835 rebuild is also REQUIRED for CHAOS-3834's
calibrated `efRuntime=200` to take effect.** HNSW `efRuntime` is read
ONLY from the vector index's `CREATE OPTIONS` clause (verified live
against the pinned FalkorDB module — there is no per-query knob at all):
`ensureVectorIndex` applies a calibrated `RetrievalPolicy.EfRuntime` only
when it CREATES a brand-new index, never against one that already exists.
An organization whose index was built before CHAOS-3834's calibrated
table entry shipped **keeps server-default ANN behavior (`efRuntime=10`)
even after `RetrievalPolicyVersion` bumps to `rp3` and every stored
answer for that identity has been invalidated** — the version bump and
the `t2`→`t3` composition change reach that organization immediately, but
the ANN search breadth does not. CHAOS-3834 and CHAOS-3835 deploy
together and share this ONE rebuild vehicle (CHAOS-3833's `t1`→`t2`
rebuild above carries the same efRuntime pickup too; this is not specific
to the `t2`→`t3` step) — no separate `acr-projector rebuild --org`
invocation is needed for efRuntime alone. `falkorgraph`'s bootstrap also
reports a best-effort telemetry signal (`GraphTelemetry.RecordVectorIndexEfRuntimeMismatch`,
once per organization graph, at the next bootstrap after this deploy)
when an existing OPERATIONAL index's actual `efRuntime` (read back via
FalkorDB's `db.indexes()` introspection, which already exposes it)
disagrees with the calibrated policy — a diagnostic signal through
whatever sink `Config.Telemetry` is configured with, not a substitute for
running the rebuild.

**The calibrated retrieval-policy table entry auto-applies with no
opt-in flag.** `retrievalPolicyTable`'s shipped
`openai/text-embedding-3-large#t3:r2000:b0:pnone#d3072` entry (tau=0.30,
K unchanged, efRuntime=200) applies automatically the moment a
deployment's provider, model, composition tag, and dimension match it
exactly — the exact-identity pinning IS the safety mechanism: any other
deployment shape falls back to the conservative, env-configured default
by construction (see `retrievalPolicyTable`'s doc comment), and an
explicit `ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR` still wins over the
table's tau per-knob. The entry's constants are chris-ratified for the
CHAOS-3834 T4 measurement program; the no-match/false-friend controls
its precision actually depends on (hybrid ranking + corroboration) are
the sequencing gate recorded on CHAOS-3834, tracked operationally rather
than encoded as a second flag. **This key is `t3`-scoped** — it
INHERITS the original t2-measured tau/efRuntime values by an explicit
decision recorded on CHAOS-3834 at CHAOS-3835 integration (T5's id-only
skip changes no embedded text for any subject still embedded; it only
removes whole subjects from the population), not a re-measurement. The
pre-inheritance t2 key was dropped entirely — see
`calibratedIdentityText3Large`'s doc comment in `retrieval_policy.go` for
the full inheritance rationale and its validation plan (the post-rebuild
oracle re-measure).

**That post-rebuild re-measure ran 2026-08-17 (CHAOS-3834, lane-t4) and
CONFIRMED the inheritance**: `CalibrateFromReport` at `TargetRecall=0.90`
against a real t3-tagged post-rebuild oracle report recommended
tau=0.2821294944901428, a 0.0179 delta from the shipped 0.30 — inside the
±0.02 confirmation band — with K staying at "unchanged". No table edit, no
`RetrievalPolicyVersion` bump. See `calibratedIdentityText3Large`'s doc
comment for the full result, provenance caveat, and negative-gate note.

**A rebuild is likewise REQUIRED after deploying CHAOS-3916**
(`ClickHouseSourceVersion` v5 → v6, `TeamsProjectsSourceVersion` v2 → v3).
Unlike CHAOS-3833/3835 above this is not a text/embedding change — it
closes CHAOS-3898's own gap: `queryProjects` (`teams_projects.go`) and
`queryWorkItemProjects` (`teams_projects_edges.go`, widened further by
CHAOS-4108) mint `project.v2:<provider>:<id>`/`work_item.v2:...` canonical
ids via `identity.Derive` without either producer's own `SourceVersion`
ever having been bumped for it, so an already-projected organization keeps
mixed pre-`.v2` and post-`.v2` identities across ordinary incremental
ticks — exactly the "mixed-format edges, duplicate nodes" class
`ErrProjectionSourceVersionChanged` exists to fence. Both bumps are
sequenced WITH or AFTER `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED`, so a
flag-on rebuild resolves via the build-aside-and-swap path above, not the
legacy in-place purge. This is a code+config change only for now: it
merges without touching prod deploy config or triggering any rebuild — the
prod cutover (which organizations, when) is a separate, chris-owned
decision. Run `acr-projector rebuild --org <organization-id>` for every
affected organization once that cutover is ruled.

**A rebuild is likewise REQUIRED after deploying CHAOS-7119**
(`TeamsProjectsSourceVersion` v12 → v13). The repository → team
`OWNED_BY_TEAM` edge (`queryRepositoryTeams`, `teams_projects_edges.go`) now
also projects `team_repo_ownership` rows whose `repo_id` is NULL, resolved by
(provider, case-insensitive repository name) against `repos` with the same
rule the fact reads use (`internal/contextfabric/ownershipresolve`, a port of
ops teamscope `RepoCondition`, CHAOS-7073 K11). Those rows were omitted
before, and their `updated_at` does not move on deploy, so an organization
caught up under a v12 checkpoint would never re-read them: every tick is
refused with `ErrProjectionSourceVersionChanged` (`rebuild_required`, graph
untouched) until the rebuild runs. After deploying, run for every projected
organization:

```bash
acr-projector rebuild --org <organization-id>
```

Venues: the bigboy venue's rebuild is part of the venue build (run it per
projected organization as above after the image with v13 is live); the prod
rebuild is chris's (batched with the prod roll, not per changeset). After
the rebuild the steady state is rebuild-free: a new ownership row, or a
`repos` row that arrives later with a newer `last_synced`, reaches the graph
on the ordinary incremental tick. Not changed by this deploy: a non-NULL
`repo_id` with no `repos` row still projects to the orphaned-repository
sentinel (the fact reads drop it; default pending a ruling), and the team
node's `authorization_repositories` list still carried raw
`repo_full_name` values at v13 (CHAOS-7130 changes that at v15, below).

**A rebuild is likewise REQUIRED after deploying CHAOS-7130**
(`TeamsProjectsSourceVersion` v14 → v15). The team node's
`authorization_repositories` list is now DERIVED FROM the repository → team
edge's own rows (`repositoryTeamsGroupedSQL`: per (provider, resolved
repository, team, source) latest assertion, the `ownershipresolve` rule,
canonical `repos.repo` slug), aggregated per team and listing a repository when
any stream's latest assertion is open, so list and open edges cannot disagree.
Ghost, glob and wrong-provider names leave the list; a team left with no
resolved open ownership is denied (no-ownership sentinel). The "currently
owned" arm (`valid_from <= now`) now applies to the edge too: future-dated
assertions are no longer projected as edges. Already-projected team nodes keep
the old raw list, and old future-dated edges remain, until a full rebuild.

**A rebuild is likewise REQUIRED after the ownership edges drop their start**
(`TeamsProjectsSourceVersion` v20 → v21). Project → team and repository → team
`OWNED_BY_TEAM` edges no longer project `team_*_ownership.valid_from` as the
edge start: that column is the sync stamp of the assertion, not the start of
ownership. Edges projected before v21 carry the stamp and are excluded from a
graph read of a window that ended before it, until one
`acr-projector rebuild --org <organization-id>` per organization. The
checkpoint refuses the incremental tick with
`ErrProjectionSourceVersionChanged` until then.

**A rebuild is REQUIRED after deploying the issue <> pull request link edge**
(`ClickHouseSourceVersion` v7 → v8). `queryIssuePullRequestLinks` projects the
ops link table of record `work_graph_issue_pr` as `LINKS_PULL_REQUEST`
(work item → pull request, carrying `link_provenance` and
`link_provenance_rank`). The shared cursor has already passed those rows in
every projected organization, so incremental catch-up never reads them. Every
projected organization needs one `acr-projector rebuild --org <id>`; until it
runs, `ErrProjectionSourceVersionChanged` refuses the incremental tick. The
tree walk reads this edge (and no longer `RELATES_TO`), so a project,
repository or team walk over an organization that has not been rebuilt finds
no link.

**Projection cursor space (CHAOS-7263).** The shared projection cursor
(`devhealthsource`) is positioned on each row's INGEST time (the `last_synced`
/ `computed_at` stamp ops writes when it normalizes the row), not on the row's
provider-stamped `updated_at`, so a row that lands after the cursor passed but
carries an old provider timestamp (a backfill, a newly connected project) is no
longer skipped. The exposed `ObservedAt` on entities and edges stays the
provider time. No operator action is required at deploy: a cursor saved before
this change carries no position space and is read as a reset, i.e. ONE idempotent
full re-read per organization and source (a one-time read/write spike), which
also recovers rows the old cursor skipped. A trailing overlap window
(`ACR_CONTEXT_FABRIC_PROJECTOR_OVERLAP`, default `15m`, must be `> 0`) is
walked once the source is caught up, so a row whose ingest stamp landed just
behind the cursor is still projected, without moving the cursor position. Such
a window batch's next cursor keeps the position and carries an acknowledgment
of the batch; its rows count as emitted only once the worker has persisted
that cursor, i.e. after the graph applied the batch, so a failed apply
re-emits the same batch on the retry. The walk runs
in passes of ordinary 200-row pages (the ClickHouse client's `max_result_rows`
is 1,000 and a larger read would fail the tick), at most 5 pages per call; a
pass that does not reach the window's end stops at the last fully read row and
resumes there on the next tick, so depth never drops a row, it only delays it.
Every pass ends: its upper edge is the cursor position (the frontier) at the
moment the pass started. Rows after that edge belong to the ordinary paged
read, so a writer that lands rows faster than the walk reads them cannot keep
a pass open; rows that landed behind the walk are found by the next pass. A
tick that ends with a pass open logs one INFO line, `devhealthsource overlap
window pass continues on the next tick` (`window_low`, `window_high`,
`resume_after`, `pass_age_seconds`, `pass_pages`, `pass_rows`,
`remaining_span_seconds`, `remaining_rows_estimate`, and the digests of the
two row keys, `resume_after_key_digest` and `window_high_key_digest`: on
rows that share one stamp only the key moves; the estimate is linear and is
`-1` when there is none, before the pass has moved or while the walk stands
on the edge's own stamp), and a pass that spanned several ticks
logs `devhealthsource overlap window pass ended`. A pass older than one
overlap logs the same fields at WARN, `devhealthsource overlap window pass is
older than the lateness the window absorbs`: late rows then wait longer than
they were late. The tick freshness summary carries both readings:
`orgs_window_pass_open` counts organizations that ended the tick with a pass
open (the ordinary state when the window holds more rows than one tick
walks), and `orgs_window_behind` is a bucket: an organization whose open pass
is older than one overlap is counted there and NOT in `orgs_ok`. A sustained
ingest above 1,000 rows per tick per source makes each pass longer than the
one before; that is the state `orgs_window_behind` reports.
A source that is NOT caught up (a rebuild from zero, or a projector that was
down) walks every table with one cursor, in stamp order, and a tick stops at
its drain budget. Such a tick is not a healthy one: the pair that ended with
work left is logged (`context_fabric: projection pair ended the tick with work
left`: `cursor_at`, `lag_seconds`, `pass_edge`, `remaining_to_edge_seconds`;
`-1` and empty mean the source could not say), its organization is counted in
the bucket `orgs_catching_up` and NOT in `orgs_ok`, `tick_complete` is false,
and the summary carries `sources_catching_up`, `catching_up_sources` and
`catch_up_lag_seconds_max`. Work is left when the drain spent its budget with
a further batch waiting, or when the source says its last read stopped with
rows beyond it (a page cut at its row limit, or a read that ended without a
batch); the second reading also holds for a graph build in progress and with
extra draining disabled (`ACR_CONTEXT_FABRIC_PROJECTION_DRAIN_BATCH_BUDGET` below
zero, one attempt per tick). A
stretch of paged work is a catch-up pass with a fixed edge (the source clock
when it started); it ends when the cursor reaches the edge or the source is
caught up, and logs `devhealthsource catch-up pass ended`. The first batch of
a from-zero walk also carries every table the from-zero read returned whole
(at most 150 rows; `devhealthsource from-zero read emits its complete tables
with the first batch` names them, and names the tables over that size that it
left to the walk): a repository, a team or a project of such a table has its
node on the first tick, although its row is rewritten by every sync and so
is the last one the walk reaches. A fact table over 150 rows gets no row early.
A dimension table over 150 rows (repositories; teams and projects) is read
whole before the fact walk: batches of keyset pages of that table alone, up to
the batch bounds (`devhealthsource from-zero read emits dimension tables before
the fact walk`, one line per batch, with `phase_complete`, `batch_rows` and
`phase_rows`). The position of that read is in the checkpoint cursor, so a
batch the backend did not apply is read again and a restart reads on after the
last applied batch; the fact position stays at zero and the pair counts as
catching up. `devhealthsource dimension phase ended: every dimension table was
read whole` is logged by the call that starts the fact walk, and
`tables_left_truncated` of the walk's first line then names no dimension
table. A position that does not fit the cursor (a row key far over the
identity bound) stops the phase with a WARN (`devhealthsource dimension phase
stopped before every dimension table was read whole`); the walk then reads
those tables as before. The walk emits those rows again when it
gets there. A table that does not fit the batch, or that makes the batch
invalid beside the page (WARN, `fallback=batch_invalid_with_tables`), is left
to the walk.
One tick walks one pass at most (plus a new pass when a pass resumed from an
earlier tick completes); a page that reaches the upper edge, or on which
every table reports its end, closes the pass, so an idle tick over a window
smaller than one page costs one extra statement per table. After a full pass the window's lower edge moves to (pass start - 2 x overlap),
so a row that lands more than about 2 x overlap after its own ingest stamp is
not re-read (a rebuild recovers it), and a quiet organization's window closes
by itself. After a projector restart the first caught-up ticks re-emit the
window once (idempotent). Two tables still key on their old column until ops
adds an ingest stamp: `team_project_ownership` and the
`project_membership_presence` view (cursor-unsound for late-stamped rows, the
same hazard as before).

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
| `ACR_CONTEXT_FABRIC_MODEL` | `gpt-5.6-luna` | Bare model id (no provider prefix). Ids containing `/`, e.g. `meta-llama/Llama-3.1-8B-Instruct`, are supported. Default changed from `gpt-5-nano` to `gpt-5.6-luna` in CHAOS-3855 — see the measurements below. |
| `ACR_CONTEXT_FABRIC_MODEL_FALLBACK` | *(unset)* | Second, stronger model on the same provider, tried when the primary call fails or returns output that does not validate. Unset by default (a fallback is a second billable call). As of CHAOS-3855, no fallback is recommended for the default `gpt-5.6-luna` primary — see the measurements below. |
| `ACR_CONTEXT_FABRIC_MODEL_PHRASING` | *(unset)* | Distinct model for the SECOND bounded offer-phrasing call (CHAOS-4171 PR2, `internal/contextfabric/chaos4171_offer_phrasing.go`), which rewrites each structure offer's presentation-facing wording under a closed-vocabulary guard. Unset shares the primary `ACR_CONTEXT_FABRIC_MODEL`; set to route phrasing to a smaller/cheaper model. |
| `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT` | `45s` | Bounds one generation attempt (`Config.validate`, 1s–2m, `internal/contextfabric/modelprovider/config.go`). A TUNING variable: it applies only once a provider is identified by one of `ACR_CONTEXT_FABRIC_MODEL_PROVIDER`/`_BASE_URL`/`ACR_CONTEXT_FABRIC_MODEL`/`_FALLBACK`/`_PHRASING`/`_API_KEY`. Setting it alone, with none of those, is ignored (a startup WARN names it) rather than opting the process into provider validation -- `modelprovider.Configured` does NOT treat a tuning-only variable as an opt-in signal (CHAOS-4986, superseding the CHAOS-3770 F5 decision for this subset; the 2026-09-03 prod crash loop this replaces set only this variable). If a deployment does set it alongside a provider identity variable, raising it changes the worst-case math in the retry-topology section below -- recompute `per_attempt`/`per_leg`/`worst_case` before also touching `ACR_REQUEST_TIMEOUT`; this change does not. |
| `ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS` | `2` | Attempts `genkitruntime` makes per operation (1–3). A TUNING variable; see `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT` above -- alone, with no provider identity variable set, it is ignored (logged) rather than opting in. |
| `ACR_CONTEXT_FABRIC_MODEL_MAX_TRANSPORT_RETRIES` | `2` | The OpenAI SDK's own retry loop *within* one attempt (0–5). Set `0` to make `genkitruntime` the single retry owner — the right choice for a local BYO server. A TUNING variable; see `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT` above -- alone, with no provider identity variable set, it is ignored (logged) rather than opting in. |
| `ACR_CONTEXT_FABRIC_MODEL_ALLOW_INSECURE_BASE_URL` | `false` | Permits a plaintext `http://` base URL. Only for a loopback or private-network BYO server: the credential travels as a bearer token on every request. A TUNING variable; see `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT` above -- alone, with no provider identity variable set, it is ignored (logged) rather than opting in. |

### Per-organization BYO LLM (CHAOS-3775)

The variables above configure the **deployment-default** provider. An
organization may additionally configure its own provider/base URL/model/
fallback/credential through `PUT /api/v1/context-fabric/model-config`
(`context:admin` scope; org derived from the authenticated principal, never
from the request body). Per-request resolution: an organization with a
stored configuration gets its own runtime; an organization with none falls
through to the deployment default; an organization whose stored credential
is broken (401/403 from its provider) gets a 503 scoped to that
organization only — it never silently falls back to the deployment
credential (explicit prohibition; see the TRD). A configuration change
takes effect on the organization's next request, no restart needed
(`internal/contextfabric/modelruntimeresolver` caches a constructed runtime
keyed by the config's `generation` -- a monotonic Postgres-sequence column,
not `updated_at`; two upserts landing in the same clock tick would be
indistinguishable under a timestamp key -- and rebuilds when it changes).

The stored credential is sealed with AES-256-GCM
(`internal/contextfabric/modelconfigcrypto`) under a deployment master key
set exactly like `ACR_EVIDENCE_ID_KEYS`/`ACR_EVIDENCE_ID_ACTIVE_KID`:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_KEYS` / `_FILE` | *(unset)* | Comma-separated `KID=base64key` pairs; each key must decode to exactly 32 bytes (AES-256). Required before any organization can save a BYO LLM configuration. |
| `ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_ACTIVE_KID` / `_FILE` | *(unset)* | Which configured key id new writes are sealed under. Rotate by adding a new key, repointing this at it, and letting organizations re-save over time — ciphertext sealed under a retired key id still decrypts as long as that key id stays configured. |

Reading a configuration back never returns the credential: the response
carries `credential_masked` only (last 4 characters, e.g. `********wxyz`).

### Answer reuse (CHAOS-3782)

| Variable | Default | Meaning |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE` | *(unset — disabled)* | Staleness window (1m–24h when set). A stored investigation result older than this is never reused, regardless of whether every other reuse condition holds. Leaving this unset disables answer reuse entirely: every Investigate call runs fresh, exactly as if `pginvestigation.WithAnswerReuse` were never passed. |

**What a reuse hit is rechecked against, and when it degrades.** Before a
stored answer is served, every subject it names is re-resolved and every
evidence reference it would serve is re-proved against a fresh graph
discovery for the CURRENT caller. The rule the recheck enforces is *never
serve a reference the caller can no longer see*, and it covers the whole
payload, not only the answer's own citations -- a reused result is served
whole.

The outcome is one of three, and they are distinguishable in telemetry:

- **`hit`** -- everything the stored payload would serve is still visible.
  Served unchanged, zero model calls.
- **`hit_degraded`** -- some references could not be proved visible, but
  none of them was one of the answer's own citations. The unprovable
  references (and any item they left without evidence) are REMOVED before
  the answer is served, `coverage.partial` becomes true, and the narrowing
  is disclosed both as a structured coverage detail
  (`reuse_auxiliary_refs_stripped`, carrying the count) and as a
  `degraded_reasons[]` string. **Read this as a narrowed answer, not as a
  clean cache hit** -- a reuse rate that folds the two together hides how
  much a caller is losing.
- **a miss** -- reuse did not happen and the call ran fresh.
  `miss_evidence_containment` now means specifically that one of the
  answer's own CITATIONS is no longer visible;
  `miss_recheck_unavailable` means the fresh discovery failed, so nothing
  was proved either way; `miss_hint_not_committed` means the request carried a subject hint the stored answer did not commit; `miss_degrade_invalid` means a narrowing was
  possible in principle but the stripped payload would not have satisfied
  the result contract, so it was refused rather than served malformed.

The `context fabric answer reuse containment` log line carries the
measurement behind all of this per attempt -- references demanded, proved
visible, and missing, plus what a degrade removed. A low reuse rate is
diagnosed from those counts, not from the outcome label alone: "demanded
far more than was visible" and "authorization genuinely narrowed" are
different problems that the label cannot separate.

**Retrieval discriminators (CHAOS-3833).** Every reuse-participating row
additionally persists two conjunctive equality dimensions (migration `0014`):
the **embed retrieval identity** (`<provider>/<model>#<composition tag>`, or
the literal `none` when no embedder is configured) and the **retrieval policy
version** (`rp1`; bumped in code whenever tau/K/HNSW retrieval defaults
change). Both are computed from the running binary's configuration at save
and lookup time, so a deploy that changes embed-text semantics or retrieval
policy stops matching stored answers **atomically with the deploy** — no
epoch bump or operator action required for the reuse side. Pre-`0014` rows
hold NULL in both columns and never match.

**Two-phase rollout gate — REQUIRED for any embed-semantic change.** The
fail-closed property above holds **per replica**, not per fleet: the
migration framework deliberately tolerates an older binary against a newer
schema during a rolling deploy, and a pre-`0014` binary runs the
predicate-less lookup — the new columns are simply never compared — so an
undrained old replica happily reuses pre-change answers after the migration
lands. Therefore any change to embed-text composition or semantic embed
configuration (Layer B/C: templates, the composition tag, rune cap, body
gate, prefix selector) must roll out in two phases:

1. **Phase 1 — persistence and enforcement, unchanged semantics.** Deploy
   the binary that persists and compares `embed_retrieval_identity` /
   `retrieval_policy_version` (with migration `0014` applied), with NO
   semantic change active. **Drain the fleet completely** — verify no
   predicate-less replica still serves traffic before proceeding. This
   drain step is load-bearing, not ceremonial: it is the only thing that
   closes the old-replica reuse window.
2. **Phase 2 — semantic activation.** Deploy the composition/config change.
   Every replica now computes a moved discriminator, so stored pre-change
   answers stop matching fleet-wide at the moment of the deploy, and the
   node-side identity stamp independently fails vectors closed to lexical
   until the prescribed `acr-projector rebuild --org` runs per organization.

**Graph-epoch binding (CHAOS-3898 §2.1/§2.3).** Every reuse-participating row
additionally persists the organization's active graph-lifecycle epoch
(migration `0021`, `graph_epoch`) — the epoch `contextfabric.
ResolvedGraphBinding` resolved once, before the graph reads that produced the
row, via the same per-org build-aside-and-swap pointer `acr-projector`'s
lifecycle machinery serves reads from (`OrgEpochResolver`, `docs/design/`
`context-fabric-*` lifecycle design). This is a THIRD, structurally distinct
dimension from the staleness-window/rebuild-invalidation-epoch pair above: it
does not require operator action or a two-phase rollout, because it moves
automatically and only as a real consequence of a per-org build/flip
(§3.1) — never as a deploy-time configuration change the way embed-text
semantics or retrieval policy do. A pre-`0021` row holds NULL and never
matches (same fail-closed convention as every other reuse column). Every
organization defaults to epoch 0 (the legacy, unsuffixed graph key) until
its first migration to the build-aside-and-swap pointer, so this dimension
is inert for the whole fleet until that lands.

### Vector and semantic retrieval (CHAOS-3778)

Vector retrieval is **opt-in and off by default**. It is enabled by setting a
base URL; with none set, ACR never constructs an embedder, never creates a
vector index, never writes an embedding, and the lexical retrieval path is
byte-for-byte what it was before.

| Variable | Default | Meaning |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_EMBED_BASE_URL` | *(unset — disabled)* | OpenAI-compatible API root, e.g. `http://localhost:1234/v1/`. **Setting this is what enables vector retrieval.** There is no default: no vendor endpoint is ever implied. |
| `ACR_CONTEXT_FABRIC_EMBED_PROVIDER` | *(required when enabled)* | A stable name for the endpoint, recorded verbatim in the embedder identity so a rebuild can tell vectors apart. Never checked for a specific vendor. |
| `ACR_CONTEXT_FABRIC_EMBED_MODEL` | *(required when enabled)* | Bare embedding model id. |
| `ACR_CONTEXT_FABRIC_EMBED_DIMENSION` | *(required when enabled)* | Vector width. Must match what the server returns **and** what the graph's vector index was built with — see the rebuild note below. |
| `ACR_CONTEXT_FABRIC_EMBED_API_KEY` / `_FILE` | *(empty)* | Bearer credential. A loopback embedder (LM Studio/Ollama/TEI) genuinely needs none; the shape accommodates one so a hosted embedder is a configuration change only. **CHAOS-4192: a blank value here now fails startup loudly** when `ACR_CONTEXT_FABRIC_EMBED_BASE_URL` is set, unless `ACR_CONTEXT_FABRIC_EMBED_ALLOW_NO_CREDENTIAL` (below) is also set — see the incident note above. |
| `ACR_CONTEXT_FABRIC_EMBED_ALLOW_NO_CREDENTIAL` | `false` | CHAOS-4192: explicit opt-in declaring this endpoint genuinely needs no credential (the loopback LM Studio/Ollama/TEI shape). Without it, a configured base URL with a blank `ACR_CONTEXT_FABRIC_EMBED_API_KEY` fails startup instead of silently constructing a broken embedder — **never inferred from the base URL's shape**, matching `ACR_CONTEXT_FABRIC_EMBED_ALLOW_INSECURE_BASE_URL`'s posture. |
| `ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR` | `0.55` | Absolute cosine similarity below which a neighbour is **dropped, not scored**. See the hazard note below. |
| `ACR_CONTEXT_FABRIC_EMBED_TIMEOUT` | `250ms` | Bounds one READ-path embeddings call (a single query text). |
| `ACR_CONTEXT_FABRIC_EMBED_BATCH_TIMEOUT` | `5s` | CHAOS-3828: bounds one WRITE/PROJECTION-path embeddings call (up to `ACR_CONTEXT_FABRIC_EMBED_MAX_BATCH` texts), independently of the read-side timeout above. Before this existed, the projection path reused the 250ms read-side default for a 64-text request — timing out on every call against anything but a very fast warm local model, and clearing every batch's vectors as a result (indistinguishable from a genuine embedder outage). |
| `ACR_CONTEXT_FABRIC_EMBED_MAX_BATCH` | `64` | Texts per request at projection time. |
| `ACR_CONTEXT_FABRIC_EMBED_MAX_TEXT_RUNES` | `2000` | Runes of one node's search text that are embedded. **Semantic, immutable-per-corpus** (CHAOS-3833): the value is a component of the composition tag, so changing it fails every stored vector closed and requires the paired rebuild. Validation floor is 2,000 — the largest complete per-kind template — so the lexical and vector arms always index byte-identical text for templated kinds; a lower value fails startup loudly. |
| `ACR_CONTEXT_FABRIC_EMBED_MAX_TRANSPORT_RETRIES` | `0` | The SDK's own in-client retry loop. |
| `ACR_CONTEXT_FABRIC_EMBED_PROVIDER_LOCALITY` | *(unset — `remote`)* | Where embedded text ends up: `local` (same trust zone) or `remote` (the provider is a NEW reader of the text, outside the graph's authorization scope). **An explicit declaration, never inferred from URL shape** — a loopback URL can front a tunnel, a non-loopback URL can be a same-host container. Unset means `remote`, so free-text bodies stay off until an operator affirmatively declares the endpoint local. Any other value fails startup. **Semantic when it changes the effective body gate** — pair the change with a rebuild. |
| `ACR_CONTEXT_FABRIC_EMBED_INCLUDE_BODIES` | *(unset — follows locality)* | Whether free-text body heads (PR body, incident description) join the composed search text — BOTH retrieval arms, which always index identical text. Set explicitly to override the locality default; `true` with a remote locality is the recorded tenant opt-in for transmitting body text to a remote provider (pair it with the provider's data-usage/retention statement in your deployment docs). **Semantic, immutable-per-corpus**: a flip moves the composition tag and requires the paired rebuild. |
| `ACR_CONTEXT_FABRIC_EMBED_PREFIX_FAMILY` | *(unset — `none`)* | Task-prefix pair some embedding models are trained to require (CHAOS-3836): closed vocabulary, `none` or `nomic`. `nomic` prepends `search_document: ` / `search_query: ` to the text **transmitted** to the model on the write and read paths respectively — the stored search text both retrieval arms share is never prefixed. **Explicit configuration, never inferred from the model id**; an unrecognized value fails startup. **Semantic, immutable-per-corpus**: the family is the `p…` component of the composition tag, so changing it fails stored vectors closed and requires the paired rebuild. |
| `ACR_CONTEXT_FABRIC_EMBED_EXPECT_RESPONSE_MODEL` | *(unset)* | The model id the **server reports**, when it legitimately differs from the id sent. This *retargets* the serving-model check; it cannot disable it. Leave unset unless a provider is known to rename its own id. |
| `ACR_CONTEXT_FABRIC_EMBED_ALLOW_INSECURE_BASE_URL` | `false` | Permits a plaintext `http://` base URL. Required for a loopback embedder. **Never set this for a base URL that leaves the trust boundary** — the credential travels as a bearer token. |

Both `acr-api` and `acr-projector` read these. **Configure them identically for
both.** A projector writing vectors the reader never queries is wasted work; a
reader querying an index the projector never fills is silently degraded
retrieval.

**Similarity floor — the honest-no-match guard.** A nearest-neighbour query
always returns *k* rows when *k* rows exist; it has no notion of "nothing is
close enough". Without an absolute floor, a question about a subject that does
not exist comes back with *k* confident-looking neighbours. Lowering this value
toward 0 progressively disables that guard and is the single most dangerous
change available in this table. The default suits a general-purpose sentence
embedder; retune it against the ambiguity corpus when changing embedder, not by
feel.

**Changing the embedder or its dimension requires a rebuild.** Vectors are
projection artifacts stamped with the embedder identity and dimension that
produced them. If the configured dimension stops matching the organization's
existing vector index, ACR **disables vector retrieval for that organization**
and answers lexically — it does not fail the request, and it does not silently
drop and recreate the index. Recovery is the existing
`acr-projector rebuild --org <org>`, which resets every source checkpoint and
bumps the rebuild epoch (which also invalidates answer reuse). Until that runs,
the organization is simply back to the pre-CHAOS-3778 lexical behaviour.

**Load exactly one embedding model on the embedder — this is a hard operational
requirement, not a recommendation.** An OpenAI-compatible server is not obliged
to honour the request's `model` field, and at least one in active use does not:
LM Studio with more than one embedding model loaded silently ignores `model` and
serves whichever it prefers, with no error. This was reproduced repeatedly,
returning 768-dimension nomic vectors for requests explicitly naming a
1024-dimension qwen3-embedding model.

ACR defends against this by verifying the `model` the response reports against
the configured model on **every** embeddings call, and failing closed on any
mismatch — including a response that does not say which model served it, which
is treated as a mismatch rather than as a pass. A failure here degrades to
lexical-only retrieval (read side) and persists no vector (write side).

Do not rely on that check as a substitute for the operational requirement. It
protects against ingesting wrong vectors; it does not make a
multiple-model-loaded embedder usable. If the check is firing, the symptom is
that vector retrieval silently stops contributing — verify with the embedder's
own `GET /v1/models` and unload the extra models.

The dimension check does **not** cover this on its own: it only catches a
substitution when the widths differ. Two same-width models (embeddinggemma at
768 and nomic at 768) would otherwise produce silent mixed-vector corruption — a
graph holding vectors from two models whose similarities are meaningless against
each other, every node stamped with the identity of the model that was *asked
for*. Nothing downstream can detect that, and no rebuild fixes it without first
fixing the server.

**Degradation is visible in the answer, not only in logs.** When vector
retrieval drops out for a request — an embed timeout, an unreachable embedder,
a wrong serving model, or a fence mismatch — that investigation's result reports
`coverage.partial` and carries a fixed limitation stating that one retrieval
mechanism was unavailable. The signal is request-scoped: it describes that
answer, not the organization's recent health.

The limitation names no mechanism, provider, model, or error text. It is
answer-facing prose, and every cause has the same consequence for a reader —
retrieval saw less than it should have. The operator-facing detail is in
telemetry (`RecordVectorRetrievalDegraded`), which is where you diagnose *which*
of the causes above fired.

**One cause is NOT a fault: a historical question (CHAOS-3781).** A question
with an as-of or range time axis deliberately skips the vector mechanism,
because a k-NN index cannot honour a validity window — `db.idx.vector.queryNodes`
returns the top-k by distance and any temporal predicate is a post-filter over
that k, which would under-report and would break the truncation guarantee. The
answer carries the same limitation, because the reader's situation is the same:
fewer candidates were considered.

Telemetry separates the two, and this distinction is the one to reach for first
when the limitation appears:

| signal | level | meaning | action |
| --- | --- | --- | --- |
| `RecordVectorRetrievalDegraded` | Warn | the mechanism BROKE — embed failure or timeout, wrong serving model, fence mismatch | diagnose the embedder |
| `RecordVectorRetrievalSuppressed` | Info | the mechanism was WITHHELD from a historical question, by design | none; expected with historical traffic |

They are separate signals rather than one signal with a reason, because folding
them together would make a healthy system serving historical questions
indistinguishable from an embedder outage. A rising `Suppressed` count tracks
historical query volume; a rising `Degraded` count is the one that warrants
attention.

**What it does *not* report, deliberately: nodes that simply have no vector.**
A projection batch whose embedding step failed clears the affected nodes'
vectors, so those nodes are absent from vector search until a later batch or a
rebuild re-embeds them. A subsequent query runs the vector mechanism
successfully over the remaining corpus and reports **no** degradation — which is
correct, and worth stating because it looks like a gap.

The distinction the marker draws is *mechanism availability*, not *corpus
completeness*:

- A vector-less node is a **data gap**. It is still fully reachable
  lexically — both retrieval paths index the same `search_text` — so the subject
  has not disappeared, it is merely findable one way instead of two. This is the
  same class as a subject the projection has not caught up to yet, which the
  answer has never claimed to report.
- A degraded mechanism means the query **could not run one of its retrieval
  strategies at all**, so every subject in the organization was searched one way
  short.

Reporting data gaps through this marker would make it fire on essentially every
answer during any backlog, which would train readers to ignore it — and a
partial-coverage signal that is always on carries no information.

### Observing vector retrieval

Everything listed here is wired and emitted. Nothing else about vector
retrieval is currently observable — if a signal is not in this table, it does
not exist.

All signals go through `log/slog` (`falkorgraph.SlogTelemetry`, supplied by both
`acr-api` and `acr-projector`). They carry organization IDs, counts, and
durations only — never text, vectors, model output, or provider response bodies.

| Signal | Level | Emitted when | What it tells you |
| --- | --- | --- | --- |
| `context_fabric: vector retrieval unavailable for a request` | WARN | A query could not run the vector mechanism (embed failure/timeout, wrong serving model, fence mismatch) | The read path degraded to lexical for that request. Sustained = the embedder or the fence needs attention. |
| `context_fabric: projection batch cleared stale vectors` | WARN | A projection batch cleared vectors it had invalidated (`embedded`, `cleared` counts) | **This is the mass-clear signal.** A sustained nonzero `cleared` count means a growing fraction of the corpus is vectorless. |
| `context_fabric: projection batch embedded nodes` | DEBUG | Every healthy batch (`embedded`, `cleared` counts) | Steady-state progress; raise to DEBUG to measure re-embedding throughput. |
| `context_fabric: projection tick failed; checkpoint held for replay` | ERROR | A projection tick failed, including one that failed to keep vector state reconcilable | The checkpoint is deliberately held. Sustained = an organization is stalled. Carries a bounded `failure_class`, never the underlying error text — see below. |
| `context_fabric: projection graph count check finished` | INFO | One graph-vs-source count check of one organization ran (default every 10 minutes per organization) | Carries `org_id_hash`, `instance` (random per projector process; replicas count independently) and `pass` (the organization's check sequence within that instance), `outcome` (`completed`, `cancelled` by timeout or shutdown, or `failed` when a count read failed), `sources_checked`, `kinds_compared`, `gap_count` (confirmed `graph_below_source` gaps), `error_count` (failed count reads) and `duration_ms`. No line for an interval period means the check did not run. |
| `context_fabric: observation traversal degraded` | WARN | Observation-to-entity traversal failed for some candidates | Unrelated to vectors; listed because it shares the sink. |

**No log line in this subsystem carries an error's own text.** The bounded
`failure_class` is the only failure detail emitted, and that holds at every log
site — the observer, the coordinator's own lock, rebuild-marker and pair-failure
records, and the projector binary's lifecycle logs — not merely at the observer.
A sanitized log beside an unsanitized one provides no guarantee at all.

**Tick failures report a class, not an error string.** `failure_class` is one
of `canceled`, `checkpoint_conflict`, `organization_locked`,
`rebuild_required`, `dependency_unavailable`, `dependency_rate_limited`,
`query_budget_exceeded`, `invalid_result`, or `unclassified`.
`query_budget_exceeded` (CHAOS-3848) is a canonical-source query that
exceeded its own configured read budget (e.g. ClickHouse
`max_bytes_to_read`/`max_result_rows` -- `ACR_CLICKHOUSE_MAX_BYTES_TO_READ`).
Unlike `dependency_unavailable`, it is a PERMANENT condition for the current
query shape and data volume: identical retry against unchanged data fails
the identical way, so it is distinguished from a transient dependency
outage rather than folded into it. The underlying error's own text is never
logged, at any level: a source or checkpoint-store error is unbounded
dependency output, and a guarantee that held only at some log levels would make
leaking depend on deployment configuration. For dependency-specific detail,
read that dependency's own logs. `unclassified` is a real answer — it means a
failure arrived that this vocabulary does not yet name, which is itself the
signal that the vocabulary needs extending.

**ClickHouse read budget (acr-api).** `ACR_CLICKHOUSE_MAX_BYTES_TO_READ`
defaults to 256 MiB per statement. The bytes a statement reads come from the
table state (every version row of the organization's `work_unit_investments`,
plus `work_unit_membership` when a membership run is recorded), never from the
server's cache state, and grow with the organization's work units. The
repository and team investment mix reads the membership scope once per
(organization, run id) and remembers it for at most five minutes; the investment
table is read once per request. Every reader statement logs
`devhealthfacts.read_stats` at Info (`reader`, `read_rows`, `read_bytes`,
`elapsed_ms`, `outcome` = `ok`, `budget_exceeded` or `error`); alert on
`read_bytes` approaching the limit rather than on the refusal. A statement over
the limit is refused with `devhealthfacts: <action> exceeded the read budget
(limit N bytes; the server had read R rows and B bytes); narrow the window or
the subject` (the measured part appears when the server reported progress
before refusing; a row-limit refusal names a row limit, not the byte cap) and
logs `devhealthfacts.read_budget_exceeded` at Warn with `reason`, `reader` and
`clickhouse_code`; the numbers are on the matching `read_stats` line.

**Known limitation — no backlog ratio.** These signals report *events*, not a
*proportion*. Summing `cleared` against `embedded` over time approximates how
much of a corpus is vectorless, but nothing computes "N% of this organization's
nodes currently have no vector" or raises an alert when that fraction makes
vector retrieval effectively useless. Concretely: after a mass clear followed by
embedder recovery, queries succeed over the *remaining* vectorized nodes and
correctly report no mechanism degradation, while the missing fraction is visible
only by reading the accumulated `cleared` counts. That detector is deliberately
out of scope for CHAOS-3778 and is filed as follow-up work; until it exists,
watch the `cleared` counts and the projection tick failures.

**Degradation is expected and safe.** An embedder that is unreachable, cold, or
slow degrades the request to lexical-only rather than failing it; a cold local
model was measured at 9.3 s against 10–17 ms warm, which is exactly why the
per-call timeout is small and the failure is open. The same applies on the
write side: a projection batch whose embedding call fails still commits its
canonical projection and advances its checkpoint. A node without a vector is
invisible to vector search and fully reachable lexically — degraded retrieval,
never lost data — and the next rebuild re-embeds it.

Answer reuse is **opt-in**: an operator turns it on by setting a window.
This is deliberate, not merely conservative-by-default -- reuse changes
what a request can be served from (a prior turn's stored answer, not a
fresh graph/fact read), and that is a deployment decision, not something
composing the investigator should silently switch on. Once opted in, the
six-condition policy (TRD §19.7.3) fails closed on its own -- an
unauthorized subject, a changed watermark, a stale generation time, or a
version mismatch all fall through to an ordinary fresh investigation,
never a wrong answer.

**D15 hazard, restated (TRD §19.2/§19.7.3):** the projection cursor is
event-time based, so a backfilled or corrected source row does not
advance `backend_watermark` and is not re-observed until a full rebuild.
Watermark equality alone cannot prove a stored answer is still accurate.
This env var is the first of the two independent bounds that cover that
gap -- set it conservatively for your deployment's plausible backfill lag.
The second bound is rebuild invalidation: `acr-projector`'s
`ReuseInvalidator` hook (`projectionrun.Coordinator`, wired in
`cmd/acr-projector/runtime.go`) marks every stored result for an
organization unreusable the moment that organization's rebuild completes,
independent of whether the rebuild happened to produce a different
watermark string.

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

Separately, Genkit's own tracing records a generation's full request and
response (including the system prompt and the model's answer) as span
attributes on every call, and would export them to an HTTP telemetry
server if the ambient `GENKIT_TELEMETRY_SERVER` environment variable were
ever set in this process — a variable ACR does not set but also does not
control. `modelprovider.New` preempts this before constructing the Genkit
instance, registering its own no-op OpenTelemetry tracer provider so
Genkit's lazy telemetry wiring never activates and never consults that
variable, regardless of the ambient environment
(`modelprovider.TestNew_neverExportsPromptContentToGenkitTelemetry`).

The same construction point closes three related paths: Genkit's action
metrics also embed a failed generation's raw error text (which can carry a
provider response body) as a metric attribute on the global
`otel.Meter("genkit")`, so `modelprovider.New` registers a no-op
`MeterProvider` alongside the no-op tracer provider
(`modelprovider.TestNew_neverExportsErrorContentToGenkitMetrics`); `New`
also fails composition outright if the ambient `GENKIT_ENV` variable is
set to `dev`, which would start Genkit's local reflection server and its
`handleNotify` endpoint — a second, independent way to register a
telemetry exporter at runtime
(`modelprovider.TestNew_rejectsGenkitDevEnvironment`); and Genkit's Debug-level logging of generation content was checked
empirically, not assumed safe. Two of its paths are structurally
unreachable or content-free on ACR's own call path: the one closure that
logs full input/output (`DefineGenerateAction`) is registered as the
`"generate"` action and is reachable only through Genkit's dev-only
reflection/action-dispatch callers, never through the direct SDK call
chain `genkitruntime` uses; and the schema-mismatch log line that IS on
that path never echoes the offending content, even at Debug level — its
message is `encoding/json`'s own generic parse error
(`modelprovider.TestGenkitDebugLoggingNeverCarriesGenerationContentOnACRsPath`).
A third path is real: `core/action.go`'s `Action.Run` wraps every action,
including the model action itself, and its deferred Debug log records the
raw generation error verbatim — which, for a genuine provider transport
failure, is the OpenAI SDK's own error carrying the raw response body
(confirmed by capturing the leak live before the fix). Rather than relying
on ACR's log level (a property of composition, not of the SDK), this is
sanitized at its source: the OpenAI-compatible client's own transport,
which `modelprovider` already owns, replaces any non-2xx response body
with a fixed, status-only shape via `option.WithMiddleware` before the SDK
ever constructs an error from it — so every consumer of that error,
present or future, sees sanitized text unconditionally, regardless of log
level or call path
(`modelprovider.TestActionRunDebugLoggingNeverCarriesProviderResponseBody`).
The replacement preserves the HTTP status only, which is enough for every
existing status-based classification (the SDK's own retry decision, and
`classifyModelError`'s rate-limit detection) to behave identically
(`modelprovider.TestSanitizedProviderErrorStillClassifiesIdenticallyThroughRetryableAndTaxonomy`).
Both the tracer- and meter-provider registrations are last-writer-wins
against any later `otel.Set{Tracer,Meter}Provider` call anywhere in the
process; see `suppressGenkitTelemetryExport`'s doc comment in
`internal/contextfabric/modelprovider/provider.go` for why that is a
durable guarantee for this codebase specifically, and what a future change
adding real OpenTelemetry export to ACR must account for.

**`ACR_REQUEST_TIMEOUT` must be raised before enabling investigations.** It
defaults to **15s**, and it bounds the whole HTTP request. A real
investigation is two sequential model calls (interpret, then synthesise) and
was measured end-to-end through the endpoint at **45–95s** against
`gpt-5-nano`. Left at the default, every investigation returns 504 before
the model answers, regardless of how well the model is doing.

**The 180s figure this section previously gave was wrong** (CHAOS-3770 F6):
it counted only `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT` × `ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS`
× 2 operations, and ignored three factors the actual retry topology has:

- **The fallback leg runs its own full retry loop, synchronously, after the
  primary's is exhausted.** `genkitruntime.Runtime.InterpretQuestion`/
  `SynthesizeAnswer` invoke `Config.Fallback` (built with the SAME
  `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT`/`_MAX_ATTEMPTS` as the primary — see
  `modelprovider.runtimeConfig`) only after every primary attempt has
  failed, and wait for it in-line before returning. With a fallback
  configured (opt-in; not the default since CHAOS-3855 — see below) this
  roughly DOUBLES the worst case per operation, not zero: primary
  attempts, then fallback attempts.
- **`ACR_CONTEXT_FABRIC_MODEL_MAX_TRANSPORT_RETRIES` adds real wall-clock
  time beyond `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT`, not inside it.** The
  OpenAI SDK's own retry loop
  (`openai-go/internal/requestconfig.RequestConfig.Execute`) sleeps
  between retries with a plain `time.Sleep`, which is NOT bounded by the
  request's context deadline — so a transport retry's backoff can run
  past the moment `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT` would otherwise have
  fired. Ordinarily that backoff is capped at 8s per retry (exponential,
  jittered), but the SDK honors a provider's `Retry-After` header up to
  just under 60s per retry when present, so a provider that returns long
  `Retry-After` values can push this considerably higher than the 8s
  figure below.
- **Genuinely transient failures repeat this at every one of
  `ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS` genkitruntime-level attempts**,
  each of which gets its own fresh `ACR_CONTEXT_FABRIC_MODEL_TIMEOUT`
  budget plus its own transport-retry backoff on top.

The honest worst case, per leg (primary or fallback) per operation:

```text
per_attempt  = ACR_CONTEXT_FABRIC_MODEL_TIMEOUT + ACR_CONTEXT_FABRIC_MODEL_MAX_TRANSPORT_RETRIES × 8s
per_leg      = ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS × per_attempt
per_operation = per_leg × (2 if a fallback is configured, else 1)
worst_case    = per_operation × 2   # interpret, then synthesize, sequentially
```

With the documented defaults (`45s` timeout, `2` attempts, `2` transport
retries): `per_attempt` = 45 + 2×8 = 61s, `per_leg` = 2×61 = 122s. As of
CHAOS-3855 the default deployment has no fallback configured, so
`per_operation` = 122, `worst_case` = 122×2 = **~245s**. If an operator
opts a fallback back in, `per_operation` doubles to 244 and `worst_case`
doubles to **~490s**. Size `ACR_REQUEST_TIMEOUT` above whichever applies to the
deployment's actual configuration, plus headroom, and treat the 8s
per-transport-retry term as a floor, not a ceiling, if the configured
provider is known to return long `Retry-After` values. The timeout is
global to the API, not per-route, so raising it also loosens the bound on
every other route; a per-route timeout is the cleaner fix and is not
implemented yet.

**`ACR_WRITE_TIMEOUT` must be raised alongside it (CHAOS-4330).** This is
`http.Server.WriteTimeout` — the wire-level deadline for writing the
response — a completely different budget from `ACR_REQUEST_TIMEOUT`
above, which only bounds the application-level handler. Raising
`ACR_REQUEST_TIMEOUT` without also raising `ACR_WRITE_TIMEOUT` to match
lets a real, legitimately-slow investigation finish its work and start
writing the response, only to have the connection forcibly closed
mid-write once the (still-default, 20s) write deadline fires — and the
handler, which measures its own processing rather than the wire, still
logs a successful `request completed ... status 200` while every actual
client saw a dropped connection. `Validate()` now rejects `ACR_WRITE_TIMEOUT`
set less than 5s above `ACR_REQUEST_TIMEOUT`, so this can no longer reach
production silently — but size it with real headroom (the ask-dev
Workbench's own launch script uses 500s/490s, a 10s margin) rather than
the bare minimum the validator accepts.

**CHAOS-3855 update: the production default is now `gpt-5.6-luna` alone, no
fallback.** The CHAOS-3742 five-arm generative trial measured `gpt-5.6-luna`
alone as equal-or-better than the `gpt-5-nano` + `gpt-5.6-luna` fallback chain
described below (1/30 strict vs. 0/30 strict) at roughly 2.4x fewer tokens
per corpus run (82K vs. 193K); `gpt-5-nano` was the weakest interpreter and
the dominant source of fact-parameter rejections. The CHAOS-3770 measurements
and the "fallback is required" conclusion immediately below predate that
trial and are kept for historical context, not as the current recommendation.

**Migrating off the pre-CHAOS-3855 recommendation.** A deployment still
carrying the old recommendation sets only
`ACR_CONTEXT_FABRIC_MODEL_FALLBACK=gpt-5.6-luna` and leaves
`ACR_CONTEXT_FABRIC_MODEL` unset. Before CHAOS-3855 that resolved to
`gpt-5-nano` primary / `gpt-5.6-luna` fallback; after CHAOS-3855,
`ACR_CONTEXT_FABRIC_MODEL` defaults to `gpt-5.6-luna` too, so the same
environment now names an identical primary and fallback. `Config.validate`
fails loud on that (by design — a silent dedupe would hide a
misconfiguration), so the deployment fails to start with a `must name a
different model` error until the operator **unsets
`ACR_CONTEXT_FABRIC_MODEL_FALLBACK`** (the new default primary needs no
fallback) or points it at a genuinely different model.

**Model choice matters, and `gpt-5-nano` alone is not enough.** Measured
live against `gpt-5-nano` (the CHAOS-3770 acceptance probes, both skipped
unless `ACR_TEST_MODEL_API_KEY` is set —
`go test ./internal/contextfabric/modelprovider -run Live` for the runtime
and `go test ./internal/api -run LiveEndpoint` for the endpoint):
interpretation passed ACR's validator on every run, but synthesis — the
strictest validator in the pipeline — did not. `gpt-5-nano` alone answered
**2 of 33** endpoint attempts; the richer the synthesis input (graph paths
plus several canonical facts), the worse it did, so the runtime-level rate of
roughly one in three overstates it for real traffic.

Every failure was a clean, correctly classified rejection, never a wrong
answer: value-level closure rejects what it cannot bind to a canonical fact.
So the failure mode is availability, not correctness — but at 2 in 33 the
endpoint is unusable. As of CHAOS-3784, a synthesis-side claim-binding
rejection like this one is `422 synthesis_rejected` (no `violated_bound`,
since claim-binding is a business rule, not a length/count bound); a
provider/schema-level failure keeps the pre-existing `502
upstream_invalid_output` code (unchanged). Earlier measurements in this
section predate that split and describe what was then a single opaque `502
upstream_invalid_output` for both.

**The fallback is required, not optional.** Invalid output is deliberately
**not** retried (`genkitruntime` fails closed on a schema-shaped failure
rather than re-rolling the same input); the fallback model is the only
mitigation, and `genkitruntime` invokes it on invalid output as well as on
transport failure. With `ACR_CONTEXT_FABRIC_MODEL_FALLBACK=gpt-5.6-luna` the
same endpoint answered **16 of 17**, at 45–95s per request. Set it in every
deployment expected to answer. Each fallback is a second billable call and is
recorded as `fallback_used` on the receipt; tracking that rate per model is
the `ModelReceiptSink` evaluator's job (CHAOS-3756), and a sustained high
rate is the signal to promote the fallback to primary.

The fallback raises the answer rate; it does not make it 1.0. One of those
17 attempts still failed, so a caller must treat `422 interpretation_rejected`,
`422 synthesis_rejected`, and `502 upstream_invalid_output` as expected,
retryable outcomes even with the fallback configured — do not build a
client that assumes an investigation always returns an answer on the first
call.

#### Measured model matrix

| Configuration | Usable answers | Typical latency | Dominant failure mode |
| --- | --- | --- | --- |
| `gpt-5-nano` alone | 2 / 33 | 52–95s | Synthesis value-level closure — a driver cites no claimed fact restating a canonical value |
| `gpt-5-nano` + `gpt-5.6-luna` fallback | 16 / 17 | 45–95s | One residual synthesis rejection |
| `gpt-5-mini` alone | 4 / 12 | 17–88s | **5 of 8 failures were the omitted 256-character `requested_judgment` cap, since fixed**; the other 3 were synthesis closure, a finding bounds violation, and one provider `INTERNAL` |

Read the `gpt-5-mini` row with care: it was measured on interpretation prompt
v3, which did not state the `requested_judgment` limit the validator enforces.
Mini writes a longer judgment than nano and was rejected for it on 5 of 12
attempts before reaching synthesis at all — a prompt omission, not a model
weakness. Interpretation v4 and synthesis v6 now state every bound in
`contracts/v1.ContextFabricModelFacingBounds` — the model-facing subset of
`ContextFabricInvestigationResult.Validate()`'s bounds (excluding
`direct_judgment`/`current_state`/`deterministic_answer`, which ACR
server-composes and truncates to fit rather than ever validating the
model's own text against). `genkitruntime.TestPromptsStateEveryModelFacingBound`
proves every one of that registry's entries is both stated in the prompt
and pinned to the validator's exact limit;
`genkitruntime.TestModelFacingBoundRegistryIsFullyCovered` proves the test
itself cannot silently omit a registry entry, which is what let the
top-level `strongest_pressures`/`drivers`/`remaining_work`/`readiness_gaps`/
`conflicts`/`limitations`/`evidence_ref_ids` collection caps go untested
(and `warnings` go entirely unstated in the prompt) even after v5. So
mini's true rate on current prompts is **unmeasured and expected to be
materially better than 4 of 12**. The nano and fallback rows were also
measured on v3.

The standing recommendation above (`gpt-5-nano` with the `gpt-5.6-luna`
fallback) is superseded as of CHAOS-3855: the CHAOS-3742 five-arm trial found
`gpt-5.6-luna` alone, with no fallback, an equal-or-better and materially
cheaper production configuration — see the callout above. `gpt-5-mini`
remains an open question worth settling against `gpt-5.6-luna` alone rather
than against the retired nano/fallback pairing. Re-run
`go test ./internal/api -run LiveEndpoint` with `ACR_TEST_MODEL=gpt-5-mini`
on the current prompts to settle it.

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
registry-pull Secrets. It does not create a database, Gateway, or Gateway
controller, and the plain overlays render no MCP workload; the hosted
`acr-mcp` is the opt-in `components/acr-mcp` Component described in
`deploy/README.md`.

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

### Upgrading an existing projector Deployment to `strategy: Recreate`

The chart renders the projector with `strategy: {type: Recreate}`. A
Deployment created before that change carries the API-defaulted
`spec.strategy.rollingUpdate` block, and Helm 4 applies server-side. The
upgrade then fails with:

```text
Deployment.apps "<release>-projector" is invalid: spec.strategy.rollingUpdate: Forbidden: may not be specified when strategy `type` is 'Recreate'
```

Server-side apply cannot clear a defaulted field: rendering
`rollingUpdate: null` was tried on Kubernetes v1.36 with the `helm` field
manager and is rejected the same way, and a server-side dry-run reports the
same error, so it does not give early warning either. Remove the block once,
before the first upgrade that carries `Recreate`:

```sh
kubectl -n <namespace> patch deployment <release>-projector --type json \
  -p '[{"op":"remove","path":"/spec/strategy/rollingUpdate"}]'
```

Check first with
`kubectl -n <namespace> get deployment <release>-projector -o jsonpath='{.spec.strategy}'`.
Patch only when the result carries a `rollingUpdate` key. On a fresh install
or a Deployment that is already `Recreate` the result is `{"type":"Recreate"}`
and the patch fails with `remove operation does not apply: doc is missing
path`; skip it there. After the patch the result must be
`{"type":"Recreate"}`.

### Projector rebuild on Kubernetes: one-off Job (method of record)

`kubectl exec deploy/dev-health-acr -- acr-projector rebuild` does NOT work
on the hosted chart: the acr-api pod has no projector configmap (config load
fails) and the image is distroless (no shell). Run the rebuild as a one-off
Job built from the projector Deployment's pod template: same image, same
`envFrom` (projector configmap) and secrets by reference, same labels and
ServiceAccount, `restartPolicy: Never`, `backoffLimit: 0`. Method of record
since the 2026-09-29 production v13 rebuild.

Steps (namespace `dev-health`):

1. Scale the projector to 0 (a rebuild needs the per-organization
   single-flight lock; a running projector gives "locked by another
   projector"): `kubectl -n dev-health scale deploy/dev-health-acr-projector --replicas=0`
2. Apply the Job (shape below).
3. Read its log: `kubectl -n dev-health logs job/acr-projector-rebuild-v13`.
   Success is exit 0 and `projection organization rebuilt` (no purged count
   is logged). Job `Complete 1/1` took 4 s in production.
4. Delete the Job: `kubectl -n dev-health delete job/acr-projector-rebuild-v13`.
5. Scale the projector to 1, then read back the projector log until
   `orgs_rebuild_required=0`, `orgs_ok` equals `orgs_configured`, and
   `tick_complete` is true. While the rebuild is still walking the history,
   `orgs_catching_up` is 1, `tick_complete` is false and
   `catch_up_lag_seconds_max` falls from tick to tick. (`orgs_window_pass_open`
   above zero is normal after a rebuild; `orgs_window_behind` above zero is
   not.)

Job shape (secrets by reference only; take the image digest and env from the
live projector Deployment, do not copy values into a ticket or a doc):

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: acr-projector-rebuild-v13     # name by source version
  namespace: dev-health
  labels: { app.kubernetes.io/name: acr, app.kubernetes.io/instance: dev-health-acr, app.kubernetes.io/component: projector }
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 86400
  template:
    metadata:
      labels: { app.kubernetes.io/name: acr, app.kubernetes.io/instance: dev-health-acr, app.kubernetes.io/component: projector }
    spec:
      restartPolicy: Never
      serviceAccountName: dev-health-acr
      automountServiceAccountToken: false
      imagePullSecrets: [{ name: ghcr-pull }]
      securityContext: { runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, fsGroup: 65532, seccompProfile: { type: RuntimeDefault } }
      volumes: [{ name: tmp, emptyDir: {} }]
      containers:
        - name: acr-projector
          image: <same digest as the projector Deployment>
          command: ["/usr/local/bin/acr-projector"]
          args: ["rebuild", "--org", "<organization-id>"]
          envFrom: [{ configMapRef: { name: dev-health-acr-projector-config } }]
          # env: copy the projector Deployment's env block. Secrets stay
          # secretKeyRef (ACR_POSTGRES_DSN, ACR_CLICKHOUSE_DSN from
          # acr-runtime; ACR_CONTEXT_FABRIC_EMBED_API_KEY from
          # acr-model-provider), never literal values.
          volumeMounts: [{ name: tmp, mountPath: /tmp }]
          securityContext: { allowPrivilegeEscalation: false, privileged: false, readOnlyRootFilesystem: true, capabilities: { drop: [ALL] } }
          resources: { requests: { cpu: 100m, memory: 256Mi }, limits: { cpu: "1", memory: 1Gi } }
```

Duration: the Job itself is seconds (it purges and resets checkpoints). The
replay after scale-up is the long part: production converged in about 75 min
(rebuild 12:37Z, converged about 13:52Z); the venue rebuild with teams took
17 min 35 s. With `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED=false` the
rebuild is the legacy in-place purge: graph reads are empty until replay
ends. Warn users first.

Older builds (before CHAOS-7171, acr #705, per-source drain budget since
`97162d12`): one drain budget per tick was shared by all sources, so a large
ClickHouse backlog starved `dev_health_teams_projects` to one page per tick
after a rebuild. Workaround on those builds only: set
`ACR_CONTEXT_FABRIC_PROJECTION_DRAIN_BATCH_BUDGET` to a NEGATIVE value (`-1`;
`0` means the default 500) on the projector, then REMOVE the override once
the stream is drained and verify the projector env has no such variable.

### CHAOS-3916 production graph cutover runbook

This is the chris-owned cutover CHAOS-3916 itself deferred ("a separate,
chris-owned decision" -- see the CHAOS-3916 paragraph above): enabling
`ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED` in production for the first
time, on an image that already carries `ClickHouseSourceVersion` v6,
`TeamsProjectsSourceVersion` v6 (CHAOS-4193's presence-view read swap
required a full rebuild under v4; CHAOS-4109's transition-history intervals
required v5; CHAOS-4109's duplicate-ADD-continuation fast-follow requires
this current v6 -- each one a full rebuild, not an incremental catch-up
under the prior version), and the CHAOS-4108 join-arm fix. Every
pre-flight below was learned the hard way during CHAOS-3916's local/trial
rehearsal (see CHAOS-4147) -- skipping one reproduces that incident at
production scale.

**Mandatory pre-flights, in order, before triggering anything:**

1. **`ACR_CONTEXT_FABRIC_EMBED_API_KEY` is present AND non-blank in
   `acr-projector`'s actual resolved environment -- the RUNNING container's
   own view, not a reference or a point-in-time Secret read.** Neither
   `kubectl get pod -o jsonpath` (shows the env var's `secretKeyRef`
   NAME/KEY, never the resolved value) nor reading the Secret object
   directly (its CURRENT content, which the running container may not
   actually have -- a rotation after the Pod last started leaves the
   container's real environment stale until it restarts) proves what the
   process actually has. The image is distroless (no `printenv`/`env`
   binary to `kubectl exec`), so use `kubectl debug -it <pod>
   --image=busybox --target=acr-projector -- sh -c "cat
   /proc/1/environ | tr '\0' '\n' | grep -E
   '^ACR_CONTEXT_FABRIC_EMBED_API_KEY=.+$'"` -- anchored and requiring at
   least one character after `=`; an unanchored `grep
   ACR_CONTEXT_FABRIC_EMBED_API_KEY` matches the line even when the value
   is blank (`ACR_CONTEXT_FABRIC_EMBED_API_KEY=` with nothing after it),
   which is exactly the failure mode this pre-flight exists to catch
   (an ephemeral debug container sharing the target's process namespace) to
   read the value the running process actually resolved at its own start
   time. This is the CHAOS-4147 trap: `embedprovider.Configured()` checks
   only `ACR_CONTEXT_FABRIC_EMBED_BASE_URL` -- a set base URL with a
   blank/absent key constructs a fully "configured", silently-broken
   embedder that destroys every vector it touches on its first failed
   batch. Do not infer from "the Secret exists" or "the Pod spec
   references a Secret"; confirm the key material actually resolved into
   the RUNNING container, right now.
2. **The lifecycle flag is ON in BOTH `acr-api` and `acr-projector`,
   set the same way.** `contextFabric.falkor.*` and the lifecycle env var
   must agree across both Deployments (`values.yaml`'s own comment: they
   resolve an organization's graph key through the same epoch pointer once
   either has it on). Check both rendered manifests, not just one.
3. **The lifecycle flag must be ON for EVERY reader of an organization --
   not just `acr-api`/`acr-projector` -- BEFORE that organization's first
   flip, and this is IRREVERSIBLE once the flip's grace window sweeps
   (CHAOS-4147, second finding).** A flag-off reader (any harness, script,
   or tool constructing its own falkorgraph client with a nil
   `EpochResolver`) resolves the bare, unsuffixed epoch-0 key
   (`graphKeyForEpoch`'s epoch<=0 branch is byte-identical to the
   pre-CHAOS-3898 legacy key). The FIRST flip an organization ever does
   starts that epoch-0 key's grace clock; once grace expires, the retire
   executor's periodic sweep (`tryBeginRetire`/`DrainingRetirements`)
   issues a real `GRAPH.DELETE` against it. **The sweep only runs while an
   `acr-projector serve` tick loop is alive** -- if nothing has been
   running, an already-expired retirement sits dormant, durably recorded
   (`acr.context_fabric_graph_epoch_retirements`, `state=draining`) but
   not yet executed, and fires on the NEXT process to tick, arbitrarily
   later and for reasons unrelated to whatever that process is actually
   doing. A flag-off reader querying the now-deleted key does not get a
   clean "not found": FalkorDB/RedisGraph auto-vivifies an empty,
   index-less graph on any query against a nonexistent key, so the reader
   observes "key exists, index_absent" -- indistinguishable from a
   fresh/never-bootstrapped organization, not from "this reader's target
   was permanently retired." Confirm every reader that will touch this
   organization -- including CI harnesses -- resolves epochs through the
   lifecycle-aware path before the first flip, not after.
4. **The deployed image digest actually contains ClickHouse v6/TeamsProjects
   v6 + CHAOS-4108.** Confirm the digest being promoted was built from a
   commit at or after `TeamsProjectsSourceVersion =
   "devhealthsource.teams_projects.v6"` and `ClickHouseSourceVersion =
   "devhealthsource.clickhouse.v6"`
   (`internal/contextfabric/devhealthsource/{teams_projects,clickhouse}.go`)
   and after PR #216 (CHAOS-4108's join-arm fix). An older digest re-creates
   exactly the mixed-format-edge hazard this ticket exists to close.
5. **No CLI lever exists to supersede a completed-but-bad epoch during its
   grace window (CHAOS-4147).** `acr-projector rebuild --org` refuses
   (silently, as a benign no-op) once a prior rebuild has already flipped
   for that organization -- `rollback --org` is the only lever inside the
   grace window, and it reverts to the PRIOR epoch's data (pre-CHAOS-4108
   here). Get pre-flights 1-4 right BEFORE triggering `rebuild --org`; there
   is no clean in-window do-over if the embedder credential turns out to be
   wrong.
6. **Post-rebuild verification is BY EXERCISE, never by inference**, for
   every organization rebuilt:
   - `Subject.embedding IS NOT NULL` count is nonzero on the active epoch
     key, and any gap from total node count is accounted for by the
     documented `embedKindSkipped`/`isPureIdentifierSubject` skip
     populations (CHAOS-3833/CHAOS-3835) -- not left unexplained.
   - Run one real `CALL db.idx.vector.queryNodes('Subject', 'embedding',
     k, <a live node's own embedding>)` KNN query and confirm a
     near-zero self-match distance plus semantically coherent neighbors.
     An operational index with zero embeddings still answers a KNN query
     with garbage; only an exercised query catches that.
   - Spot-check the census/project-anchor predicate (or an equivalent
     project-scoped read) against a known project pair to confirm the
     CHAOS-4108 join-arm fix produced the expected edge counts.
   - Spot-check at least one ordinary, non-project-scoped read (e.g. a
     work item lookup) for coherent, well-formed output, and check for
     duplicate `canonical_id`s org-wide -- the CHAOS-3916 ticket's own
     failure mode ("mixed-format edges, duplicate nodes").

**Rollback posture:** the build-aside-and-swap design retains the prior
epoch through its grace window (default 24h, `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_GRACE_WINDOW`)
specifically so `acr-projector rollback --org <id>` can restore it if a
flip should not have happened. Outside the grace window, or for an
organization never before rebuilt (a first-time cutover, epoch 0 -> 1, no
prior epoch to roll back to), the only rollback is disabling the lifecycle
flag application-wide, which reverts read/write to the legacy in-place
path and leaves the just-built epoch orphaned until a future decision.

### CHAOS-4076: NetworkPolicy apiserver egress and in-release FalkorDB PodSecurity

Two chart-level constraints found during the CHAOS-4034 live P6 auth
validation run, both ratified (chris, 2026-08-23) rather than fixed, and
recorded here so a future deployer does not re-open either as a bug.

**NetworkPolicy apiserver egress is a documented posture, not a proven
fix.** `workloadTokenExchange.enabled=true` renders a port-only egress
rule for the Kubernetes apiserver (`templates/networkpolicy.yaml`, gated
by `networkPolicy.egress.kubernetesAPIPort`) with no destination
restriction (no `ipBlock`/`namespaceSelector` naming
`kubernetes.default.svc` -- standard NetworkPolicy cannot name a Service
that way at all; only `ipBlock`/`podSelector`/`namespaceSelector` on
pods/CIDRs). This SHAPE (port-only, no `ipBlock`) is the accepted,
ratified posture for local/dev scope (chris, 2026-08-23) -- the chart does
not know the cluster's real apiserver ClusterIP at author time, so a
tighter rule is not generically possible.

**This is a documented bypass SHAPE, not a proven working fix -- do not
read "the rule renders" as "TokenReview is reachable."** The CHAOS-4034
live P6 A/B/A run found this exact rule PRESENT and RFC 8693 `TokenReview`
STILL timed out on that kind+Calico cluster; only deleting the ENTIRE
NetworkPolicy (not just this rule) restored a 200. Root cause is
unresolved -- plausibly a Calico/kind ClusterIP DNAT interaction specific
to that fixture, not ruled either way. Verify apiserver reachability for
TokenReview on the actual target cluster/CNI before relying on this rule;
do not assume it from the rendered manifest alone. A scale/multi-tenant
deployment that needs the apiserver egress destination-restricted (an
`ipBlock` pinned to the cluster's real apiserver Service IP, once that is
known and stable per environment) is a chart change -- file a new ticket
for it; do not narrow this rule silently, since an environment whose
apiserver IP does not match a hand-pinned `ipBlock` fails closed with no
obvious cause.

**The in-release FalkorDB workload (`contextFabric.falkordb.enabled`)
requires a `baseline` (or looser) PodSecurity Admission namespace.** The
vendored image runs `redis-server` as root with no `USER` directive; a
`restricted`-PSA namespace rejects the StatefulSet's Pod outright (0/1,
`runAsNonRoot` violation). Label the target namespace explicitly before
enabling:

```bash
kubectl label namespace <ns> pod-security.kubernetes.io/enforce=baseline --overwrite
```

This relaxation is namespace-wide -- it does not scope to the FalkorDB
Pod alone -- so prefer a dedicated namespace for the in-release FalkorDB
workload over relaxing PSA on a shared namespace that also runs
acr-api/acr-projector under the `restricted` contract those Deployments
otherwise use. An externally-provisioned FalkorDB (the default posture --
`falkordb.enabled: false`, `contextFabric.falkor.addr` pointed elsewhere)
has no PSA implication for this chart's own namespace. See
`values.yaml`'s `contextFabric.falkordb` comment for the container
security context this workload does apply (dropped capabilities, no
privilege escalation, `RuntimeDefault` seccomp) despite running as root.

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

ACR supplies bounded in-process snapshots and structured logs, and can export
its own service telemetry over OTLP (below). The hosting service owns the
collector, retention, alert routing, and tenancy review. Use the metric
mapping and SLOs in [observability](observability.md). Never add bearer
values, DSNs, packet content, evidence URLs, transcripts, organization IDs, or
repository names as log fields or metric labels.

### OpenTelemetry export

`acr-api`, `acr-mcp` (http transport only) and `acr-projector` export over
OTLP/gRPC when `OTEL_ENABLED=true`:

| Variable | Meaning |
| --- | --- |
| `OTEL_ENABLED` | `true`/`false` (unset = false). Any other value, or `true` without an endpoint, fails startup. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector, e.g. `http://collector:4317` (`http://` = plaintext gRPC). Required when enabled, and it must name a host: a hostless `http://:4317` is refused at startup rather than exporting to the pod's own loopback. Other standard `OTEL_EXPORTER_OTLP_*` variables apply. |
| `OTEL_SERVICE_NAME` | `service.name`; defaults to the binary name. |
| `OTEL_RESOURCE_ATTRIBUTES` | Extra resource attributes. `service.version` is always the build commit. |

What is exported: one server span per HTTP request, named by the matched
route (never the raw path; `/healthz`, `/readyz`, `/livez` are not traced),
the `http.server.*` metrics, one `projection tick` span per projector tick,
and every structured log line the process writes, at the same level and with
the same fields, correlated to the active span. The STDIO transport of
`acr-mcp` never exports. Each process writes one `acr otel export` line at
start with `state` `enabled` or `disabled`, so a dark backend can be told
apart from a process that was never told to send — at Info, like every other
decision line this service emits, so a deployment running at
`config.logLevel` `warn` or `error` suppresses it together with the
per-request lines (spans and metrics still export).

What is never exported: Genkit's spans, metrics and log lines (they carry model
prompt and response content). The exporter's providers are passed explicitly
to the HTTP handler, the log handler and the tick tracer; nothing installs a
global OpenTelemetry provider or `slog.SetDefault`, so Genkit -- which reads
only the globals, which `modelprovider` keeps exporter-less -- has no path to
the collector. `internal/otelexport`'s `TestNoGlobalTelemetryInstallOutsideModelProvider`
fails on any new global install, and `modelprovider`'s
`TestGenkitTelemetryNeverExported` runs a real generation inside an exported
request and asserts no Genkit span, metric or content reaches the export.

The Helm chart sets these from `otel.enabled`, `otel.endpoint` and
`otel.serviceNames` (off by default); a release whose endpoint is blank,
schemeless or hostless, or whose service name is blank, is refused at render
time. The api and mcp NetworkPolicies gain collector egress when export is on;
no policy selects the projector pods, so its egress is unrestricted — if one
is ever added it must carry the collector port too.

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
is read-only by default (`context_for_task`, `source_evidence`, and, when the
hosted API advertises them, `investigate_question` and `investigation_result`). Episode
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
