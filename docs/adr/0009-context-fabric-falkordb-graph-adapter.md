# ADR 0009: Use self-hosted FalkorDB behind ACR graph ports

- Status: Accepted
- Date: 2026-08-12
- Decision owners: Context Fabric / ACR (Chris, final backend call)
- Implements: CHAOS-3752 (supersedes [ADR 0007](0007-context-fabric-zep-graph-adapter.md))
- Design record: [docs/design/context-fabric-falkordb-adapter.md](../design/context-fabric-falkordb-adapter.md)

## Context

ADR 0007 selected Zep v3 through its official Go SDK. That SDK's wire
protocol is Zep Cloud-only; there is no supportable self-hosted server for
it, making a Zep Cloud account and API key an external, environment-owned
dependency this repository could not provision. CHAOS-3752 was re-scoped
twice against real infrastructure, not assumption:

1. **Local self-hosted Graphiti (the OSS FastAPI server), zep-go/v3
   pointed at it.** Proven live: every `zep-go` `Graph.*` call
   (`/graph`, `/graph-batch`, `/graph/search`, `/graph/add-fact-triple`, ...)
   returns 404 against the real Graphiti server, which serves a
   completely disjoint route set (`/entity-node`, `/search`,
   `/episodes/{group_id}`, ...). Confirmed on both the client and the
   server's own access log. The self-hosted Graphiti server's write path
   also requires a working OpenAI-compatible embedding credential to
   ingest anything beyond a bare entity node, and the vanilla server
   exposes no endpoint to create an explicit, deterministic relationship
   at all (only LLM-driven episode extraction) -- a poor fit for this
   domain's deterministic-identity, idempotent-replay requirements.
2. **A Postgres-only backend was designed, then rejected** ("too slow,
   we over-index on Postgres" -- Chris) before implementation began.
3. **FalkorDB, self-hosted, was the final call**, SSPL licensing risk
   explicitly accepted (consumed as a deployment dependency -- a container
   image -- not as linked code, the same posture this repository already
   takes with Postgres and ClickHouse).

The selected integration must not reintroduce a Python sidecar or expose
graph-native contracts to Workbench, MCP, agents, or Ask Dev -- unchanged
from ADR 0007.

## Decision

ACR will use FalkorDB, self-hosted, through the official Go client used only
as a compact-protocol result codec, never through its high-level API:

```text
github.com/FalkorDB/falkordb-go/v2 v2.1.0
```

The client is confined to `internal/contextfabric/falkorgraph`. The same
ACR-owned interfaces ADR 0007 established remain the stable boundary,
unchanged:

- `ProjectionBackend` for canonical projection and lifecycle writes;
- `GraphReader` for subject, cohort, relationship, temporal, and driver
  discovery;
- ACR-owned readiness, watermark, purge, and rebuild composition;
- consumer-neutral Context Fabric request and result contracts.

No `falkordb-go` request or response type is allowed in public Context
Fabric contracts, matching ADR 0007's boundary rule for `zep-go`.

**Client boundary, and why it is stricter than ADR 0007's:** the pinned
`falkordb-go/v2` client has no `context.Context` support anywhere in its
high-level API (every call runs through a package-level
`context.Background()`), its `ToString` helper panics on several common Go
scalar types, `CallProcedure` silently returns empty results instead of an
error when called with arguments, and `GraphSchema` has no mutex despite
being mutated during result parsing -- all independently verified against
the pinned version. `falkorgraph` therefore never calls the client's
`Graph.Query`/`Graph.ROQuery`/`Graph.CallProcedure` methods; it calls
`db.Conn.Do(ctx, "GRAPH.QUERY"/"GRAPH.RO_QUERY", ...)` directly (a real
`go-redis` call that does accept a context) and reuses only the client's
exported, I/O-free `QueryResultNew` decoder and `BuildParamsHeader` helper.
A `safeParams` shim rejects any Go value outside the nine types
`BuildParamsHeader`/`ToString` support without panicking, before it ever
reaches the client.

## Graph identity and tenancy

**One FalkorDB graph key per organization**, keyed by the same
server-derived, caller-opaque scheme ADR 0007 already used for Zep's graph
ID (`prefix + "-" + hex(sha256("context-fabric-graph\x00" + orgID))[:16]`) --
the two backends agree on tenancy identity by construction. This was proven
necessary, not just convenient: a live variable-length-traversal probe
against a *shared* graph with an `org` property predicate showed the
traversal routing through an intermediate node belonging to a *different*
organization with no direct edge between the two same-org endpoints --
tenant isolation in a shared graph depends on remembering an
`all(n IN nodes(p) WHERE n.org = $org)` guard on every single traversal, an
unenforceable per-query obligation whose failure mode is a silent
cross-tenant leak. One graph per org makes that class of bug structurally
impossible: a query is issued against the org's own graph key, and there is
nothing else in it.

Node identity within one org's graph is `(org_id, subject_kind,
canonical_id)` -- `org_id` kept as a node property even though the graph key
already scopes it, as defense in depth against a restore/replay landing
data in the wrong graph key, mirroring the intent (not the literal
mechanism) of ADR 0007's `verifiedNodeSubject` check. Relationship identity
is the caller-owned `relationship_id` directly; no synthetic UUID derivation
is needed the way Zep's UUID-typed API required (`deterministicUUID` is
still used, and shared with `zepgraph` via `internal/contextfabric/graphrank`,
for result identifiers such as receipt/path/driver IDs, which are
genuinely synthetic).

Nodes carry a generic `:Subject` label plus a kind-specific label (e.g.
`:Project`) simultaneously -- bootstrap only ever needs one constraint on
`:Subject`, not one per `SubjectKind`, while kind-scoped reads can still
filter by label. Edges carry a single generic `:Relates` Cypher type with
the semantic relation name in a `relation_type` property, for the identical
reason: a unique constraint on a `RELATIONSHIP` type is per-type, and this
domain has an open-ended set of relation names.

## Projection semantics

Canonical entities, relationships, content, and episodes are projected via
`MERGE ... SET n += $attrs`. Cypher's `SET n += $map` only ever touches keys
present in `$map` -- verified live -- which replaces ADR 0007's
`mergedSubjectAttributes` (a two-round-trip read-then-merge in Go, with the
documented no-compare-and-swap race) outright: a relationship/content/
episode-sourced write's `$attrs` never carries `aliases`/`previous_names`/
provider IDs/properties, so it cannot clobber what an entity-sourced write
already set, with no read, no COALESCE trick, and no separate round trip.
This closes the specific race ADR 0007 flagged as unfixable at the adapter
level for the attribute-merge case (single-statement server-side merge
under the row's own lock); the CHAOS-3753 projection worker's per-organization
serialization requirement is unchanged and still required for other
batch-level ordering reasons, not relaxed as a side effect of this change.

Temporal fields (`observed_at`, `valid_from`, `valid_to`) are stored as a
`(string, _ns int64)` pair on every node and edge: the RFC3339Nano string
for read-back fidelity and byte-parity with `zepgraph`'s attributes, and an
indexed `int64` epoch-nanosecond value that every comparison and `ORDER BY`
uses. **Comparisons never use the RFC3339Nano string form.** This is not
cosmetic: `time.Format(time.RFC3339Nano)` trims trailing zeros, so a
whole-second timestamp and a sub-second timestamp render at different
string lengths, and lexicographic comparison silently gives the wrong
answer for a mixed set (verified live: a `WHERE t.s > '...00Z'` range filter
missed a matching `...00.5Z` row). This is a standing rule for any future
timestamp-comparison code in this backend, not a one-off fix for the fields
that happened to need it first.

Tombstones are a single `DELETE ... WHERE observed_at_ns IS NULL OR
observed_at_ns <= $effective_ns` (nodes) or the edge equivalent -- one
atomic statement, replacing ADR 0007's `deleteNodeIfNotNewer`/
`deleteEdgeIfNotNewer` read-then-decide round trip. A stale, out-of-order
tombstone matches zero rows by construction.

Projection batches are idempotent through deterministic identities and
FalkorDB's own unique constraints (verified: a duplicate `(org_id,
subject_kind, canonical_id)` or `relationship_id` is rejected). Checkpoints
advance only after a durable backend receipt, unchanged from ADR 0007.
**Concurrency precondition unchanged from ADR 0007:** the CHAOS-3753
projection worker must still serialize `ApplyProjectionBatch` calls per
organization.

## Retrieval semantics

FalkorDB has no semantic/embedding retrieval; ADR 0007's Zep hybrid-search
call is replaced by RediSearch full-text search over a maintained
`search_text` property (label + aliases + previous names for subjects),
which does produce real, varying relevance scores (verified live), not a
boolean match. This is a deliberate, disclosed capability reduction to
lexical/fuzzy matching, not a silent one -- the workload this backend was
selected for is bounded upsert + shallow traversal + lexical search, per
the backend survey. Question text is OR-tokenized before querying: space is
AND by RediSearch default, and a multi-word question passed through
unmodified would almost always match nothing.

Bounded relationship traversal (1-2 hops from a resolved subject) is
implemented as iterative neighbor-edge expansion rather than a single native
Cypher `[*1..2]` path query, to keep result decoding simple against the
pinned client's compact-protocol parsing; this is an implementation detail
that can change without affecting the adapter's external contract.

**No second-hop fetch-and-verify machinery.** ADR 0007's `zepgraph` needs
`verifiedNodeSubject`/`fetchSecondHop` and the `second_hop_node_*`
degraded-reason vocabulary because Zep's `GetNode` is a UUID-only lookup
with no per-call organization scoping, so an edge endpoint not already in
the first-hop search results has to be fetched separately and proved to
belong to the caller's organization. With one graph per organization, every
lookup this adapter makes is already structurally scoped to the calling
principal's own graph key -- there is no second-hop concept to fake, and the
shared ranking package (`internal/contextfabric/graphrank`, extracted from
`zepgraph` as CHAOS-3752 prep so both backends share one implementation of
the backend-neutral ranking/ambiguity/evidence-budget logic) deliberately
does not carry this machinery: `graphrank.AdmitEdges` takes
already-endpoint-resolved edges and owns only the pure admission decision.

## Failure and network behavior

- The client uses an injected `go-redis` connection with a bounded
  per-request timeout (`Config.RequestTimeout`, `ReadTimeout`/`WriteTimeout`/
  `DialTimeout` on the underlying `redis.Options`).
- FalkorDB's own `TIMEOUT` query argument bounds reads only -- verified
  live, a runaway write is not cut off by it. The client-side context/
  timeout is the only bound on a runaway write.
- FalkorDB's errors are untyped, string-only responses (no typed error
  hierarchy for `GRAPH.*` commands). `classifyFalkorError` maps the
  specific verified error text patterns this adapter can produce
  (`"Query timed out"`, `"unique constraint violation"`, `"already
  indexed"`/`"already exists"`, `"Invalid graph operation on empty
  key"`, `"WRONGPASS"`/`"NOAUTH"`) into this package's bounded sentinel
  errors (`ErrNotFound`, `ErrConstraintViolation`, `errAlreadyExists`,
  `ErrUnauthorized`); an unrecognized error is flattened to a
  content-free generic message, never leaking raw dependency text.
  `safeDependencyError` preserves any already-known sentinel through a
  second classification pass rather than re-flattening it (a bug this
  adapter's own development live-caught: a purge followed by a watermark
  read returned a generic error instead of `ErrNotFound` until fixed).
- **`GRAPH.RO_QUERY` (read-only), unlike `GRAPH.QUERY`, does not
  auto-create an absent graph key** -- verified live, and the adopted
  mitigation for the "reading a non-existent graph creates it" hazard
  (`GRAPH.QUERY` against a typo'd or purged org silently produces an
  empty graph with no error). `falkorgraph` issues every read through
  `GRAPH.RO_QUERY` and every write through `GRAPH.QUERY`, so a read
  against an absent organization returns `ErrNotFound` honestly, with no
  separate `GRAPH.LIST` existence check or bootstrap marker node
  required. Writes still auto-create on a brand-new organization's first
  write, which is intentional (see Deletion/rebuild below).
- Context cancellation propagates to the server: every call goes through
  `db.Conn.Do(ctx, ...)`, a real `go-redis` call.

## Deployment topology

FalkorDB is self-hosted, consumed as a deployment dependency (a pinned
container image), not linked code -- **no external credential of any kind
is required**, the central operational difference from ADR 0007's Zep Cloud
posture. `deploy/compose/acr.compose.yml` adds a `falkordb` service, pinned
by digest to the exact image/module version this adapter was developed and
verified against (`falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3`,
module ver 42002, FalkorDB 4.20.2, Redis 8.6.3), profile-gated
(`context-fabric-graph`) so local development and CI do not pay for a graph
database they have not opted into, with a healthcheck that issues a real
`GRAPH.QUERY` rather than `PING` -- verified necessary: a healthy Redis port
does not prove the graph module itself is loaded and serving Cypher.

`internal/contextfabric/falkorgraph.ConfigFromEnv` reads:

| Environment variable | Purpose | Convention |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_FALKOR_ADDR` | `host:port`, e.g. `falkordb:6379` | plain value |
| `ACR_CONTEXT_FABRIC_FALKOR_PASSWORD` | Optional auth (FalkorDB has none by default) | `KEY`/`KEY_FILE`, same convention as `internal/config.SecretValue` |
| `ACR_CONTEXT_FABRIC_FALKOR_TLS` | Require TLS (default `true`) | boolean |
| `ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX` | Server-owned graph key prefix (default `acr-cf`) | plain value |
| `ACR_CONTEXT_FABRIC_FALKOR_REQUEST_TIMEOUT` | Per-request timeout (default `30s`) | Go duration |
| `ACR_CONTEXT_FABRIC_FALKOR_MAX_ATTEMPTS` | Bounded attempts, 1-5 (default `3`) | integer |
| `ACR_CONTEXT_FABRIC_FALKOR_MAX_RESULTS` | Bounded search page size, 1-50 (default `25`) | integer |
| `ACR_CONTEXT_FABRIC_FALKOR_POOL_SIZE` | `go-redis` connection pool size (default `10`) | integer |
| `ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE` | Permit a non-TLS address (local loopback only) | boolean |

`falkorgraph.Configured` reports whether `ACR_CONTEXT_FABRIC_FALKOR_ADDR` is
set at all, mirroring `zepgraph.Configured`'s "an unset dependency must never
fail closed" posture.

**Not yet done, flagged rather than silently deferred:** `deploy/helm/acr`
still only has `contextFabric.zep.*` values (ADR 0007); no
`contextFabric.falkor.*` equivalent has been added yet, and neither
`cmd/acr-projector` nor `cmd/acr-api`'s hosted runtime composition has been
changed to actually construct and select a `falkorgraph.Adapter` over the
existing `zepgraph.Adapter` -- both remain selectable in principle behind
the same `ProjectionBackend`/`GraphReader` ports, but the composition wiring
that picks one is a separate, not-yet-scoped change.

## Deletion, rebuild, and rollback

- `PurgeOrganization` is `GRAPH.DELETE <graphKey>` -- one command, full
  purge (indexes and constraints go with it), idempotent (a repeat delete
  against an already-absent key is classified to a no-op via the same
  `GRAPH.RO_QUERY`-style "Invalid graph operation on empty key" text
  `GRAPH.DELETE` itself also returns).
- A rebuild purges the organization graph, resets the ACR checkpoint, and
  replays canonical projection batches -- unchanged composition from ADR
  0007, still CHAOS-3753 worker-orchestration scope, not a `falkorgraph`
  method.
- **A write after purge re-bootstraps correctly.** Schema bootstrap
  (index + unique constraint creation, polled to `OPERATIONAL`) is
  idempotent and runs lazily on first write per organization, cached
  in-process (`Adapter.bootstrapDone`) so steady-state batches never repeat
  the cost; `PurgeOrganization` clears the cache entry for the purged
  organization so its next write re-bootstraps rather than skipping
  schema creation on the assumption it still exists. Proven live (item
  (10) of `TestLiveFalkorDBContextFabricLifecycle`).
- Rollback disables the FalkorDB adapter at ACR composition and preserves
  the canonical source systems, unchanged from ADR 0007; consumers never
  depend directly on FalkorDB state.

## Verification

Unlike ADR 0007, this adapter's full lifecycle contract has actually run
against a real backend, in CI, with no external credential gate:

- `internal/contextfabric/falkorgraph/pure_test.go` proves every
  deterministic, connection-free helper (graph key derivation, kind-label
  formatting, the `_ns` temporal-ordering fix, full-text tokenization, the
  authorization wildcard value convention, the `[]interface{}`-to-`[]string`
  property-decode fix, `safeParams`' panic-avoidance, and
  `safeDependencyError`'s sentinel preservation) without a fakeConn: this
  adapter's `conn` interface is a thin Cypher-execution boundary
  (`query(cypher string, params) ([]row, error)`), not a semantically-typed
  API surface the way `zepgraph`'s `api` interface is, so a fake
  "interpreting" arbitrary Cypher would itself be a small graph database.
- `internal/contextfabric/falkorgraph/adapter_live_integration_test.go`'s
  `TestLiveFalkorDBContextFabricLifecycle` proves the same ten-step lifecycle
  ADR 0007's `TestLiveZepContextFabricLifecycle` was written to prove but
  never ran: isolated graph creation, projection of every kind, idempotent
  replay, exact-hint and hybrid-search resolution, retrieval with temporal
  and evidence metadata, tombstone, watermark read, cross-organization
  isolation, idempotent purge, and a write after purge re-bootstrapping
  correctly -- against a real FalkorDB container started per test via
  `testcontainers-go`, **with no env gate**: this test always runs.
  `TestLiveReadOnlyPathAfterPurgeReturnsNotFoundWithoutAutoCreating` proves
  the `GRAPH.RO_QUERY` auto-create mitigation directly.
- `adapter_live_invariants_test.go` proves three more Codex-shaped
  invariants live: entity metadata (aliases/previous names) survives a
  later relationship-only write untouched, a stale out-of-order tombstone
  is correctly skipped, and the owner-wildcard repository-scope form
  (`"acme/*"`) is honored without unsafe widening to an unrelated owner.

## Consequences

- Context Fabric's permanent Go-owned graph integration boundary is
  unchanged in shape (`ProjectionBackend`/`GraphReader`); only the backend
  behind it changed.
- The backend-neutral ranking/ambiguity/evidence-budget logic
  (`internal/contextfabric/graphrank`) is now genuinely shared by two
  backend adapters, not just designed to be -- `zepgraph`'s existing test
  suite (51 tests) passed unmodified against the extraction before any
  FalkorDB code was written, the proof that the extraction was
  behavior-preserving.
- ACR owns the operational dependency and must expose its readiness and
  degradation honestly, unchanged from ADR 0007.
- No Python graph runtime is authorized, unchanged from ADR 0007.
- FalkorDB's SSPL license risk is explicitly accepted by Chris: consumed as
  a deployment dependency (a container image), not as linked code -- the
  Go client itself (`falkordb-go/v2`) is BSD-3-Clause.
- `zepgraph` stays in-tree, fully tested, and selectable by config; nothing
  in this ADR deletes it or claims FalkorDB replaces every future backend
  decision, only that it is the current one.
- Helm chart wiring and hosted runtime composition (which backend
  `cmd/acr-projector`/`cmd/acr-api` actually construct) remain open,
  explicitly flagged above rather than silently assumed done.
