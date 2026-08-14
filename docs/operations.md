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
from this same env contract; Helm's `contextFabric.falkor.*` values wire
both the projector and `acr-api` Deployments the same way (CHAOS-3774).
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
| `ACR_CONTEXT_FABRIC_EMBED_API_KEY` / `_FILE` | *(empty)* | Optional bearer credential. A loopback embedder needs none; the shape accommodates one so a hosted embedder is a configuration change only. |
| `ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR` | `0.55` | Absolute cosine similarity below which a neighbour is **dropped, not scored**. See the hazard note below. |
| `ACR_CONTEXT_FABRIC_EMBED_TIMEOUT` | `250ms` | Bounds one embeddings call. |
| `ACR_CONTEXT_FABRIC_EMBED_MAX_BATCH` | `64` | Texts per request at projection time. |
| `ACR_CONTEXT_FABRIC_EMBED_MAX_TEXT_RUNES` | `2000` | Runes of one node's search text that are embedded. |
| `ACR_CONTEXT_FABRIC_EMBED_MAX_TRANSPORT_RETRIES` | `0` | The SDK's own in-client retry loop. |
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
| `context_fabric: observation traversal degraded` | WARN | Observation-to-entity traversal failed for some candidates | Unrelated to vectors; listed because it shares the sink. |

**No log line in this subsystem carries an error's own text.** The bounded
`failure_class` is the only failure detail emitted, and that holds at every log
site — the observer, the coordinator's own lock, rebuild-marker and pair-failure
records, and the projector binary's lifecycle logs — not merely at the observer.
A sanitized log beside an unsanitized one provides no guarantee at all.

**Tick failures report a class, not an error string.** `failure_class` is one
of `canceled`, `checkpoint_conflict`, `organization_locked`,
`rebuild_required`, `dependency_unavailable`, `dependency_rate_limited`,
`invalid_result`, or `unclassified`. The underlying error's own text is never
logged, at any level: a source or checkpoint-store error is unbounded
dependency output, and a guarantee that held only at some log levels would make
leaking depend on deployment configuration. For dependency-specific detail,
read that dependency's own logs. `unclassified` is a real answer — it means a
failure arrived that this vocabulary does not yet name, which is itself the
signal that the vocabulary needs extending.

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
  configured — the standing recommendation above — this roughly DOUBLES
  the worst case per operation, not zero: primary attempts, then fallback
  attempts.
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
retries) and the standing fallback recommendation: `per_attempt` = 45 + 2×8
= 61s, `per_leg` = 2×61 = 122s, `per_operation` = 122×2 = 244s, `worst_case`
= 244×2 = **~490s**. Without a fallback configured, halve `per_operation`
to **~245s**. Size `ACR_REQUEST_TIMEOUT` above whichever applies to the
deployment's actual configuration, plus headroom, and treat the 8s
per-transport-retry term as a floor, not a ceiling, if the configured
provider is known to return long `Retry-After` values. The timeout is
global to the API, not per-route, so raising it also loosens the bound on
every other route; a per-route timeout is the cleaner fix and is not
implemented yet.

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

So the standing recommendation is unchanged for now — `gpt-5-nano` with the
`gpt-5.6-luna` fallback is the only combination measured to answer reliably —
but `gpt-5-mini` is the open question worth settling before treating that as
final, because a primary that answers on its own would remove the second
billable call the fallback costs. Re-run
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
