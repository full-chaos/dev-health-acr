# Design note: `falkorgraph` — a FalkorDB backend for the Context Fabric graph ports

- Status: **accepted design-of-record — implementation in progress.** Chris made
  the final backend call (FalkorDB, SSPL risk explicitly accepted) and the
  team lead ruled on every open question this note raised (§10, updated
  inline below). `internal/contextfabric/graphrank` (§7) has landed and is
  proven; `internal/contextfabric/falkorgraph` itself has not yet.
- Scope: `internal/contextfabric/falkorgraph` implementing `ProjectionBackend` + `GraphReader`
- Relationship to ADR 0007: this note does **not** amend it. It needs a new
  ADR (0009) that records FalkorDB as the selected backend and states
  explicitly what happens to `zepgraph` (kept in-tree, selectable by config,
  per current instructions — not superseded or deleted yet).
- **Provenance note:** this note and `internal/contextfabric/graphrank` were
  produced by two sessions working the same CHAOS-3752 investigation
  concurrently (a session-compaction artifact, not a deliberate split) and
  collided on this exact seam (§7, §10.5). The team lead resolved it: one
  session continues as sole driver, this note is folded in as design input,
  and every substantive finding below was independently re-verified or acted
  on rather than taken on faith. Nothing here is quoted from documentation;
  §1 was verified against a live container and a live Go build.

---

## 1. Verified ground truth

### 1.1 Server

- Image `falkordb/falkordb:latest`, digest
  `sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3`
  (created 2026-08-09, arm64).
- `redis_version:8.6.3`, `redis_mode:standalone`.
- `MODULE LIST` → `name graph`, `ver 42002` (4.20.2),
  args `MAX_QUEUED_QUERIES 25 TIMEOUT 1000 RESULTSET_SIZE 10000`.
  A second `vectorset ver 1` module is loaded (Redis-native, unrelated).
- Index internals are RediSearch — `db.indexes()` exposes `docTrieSize`,
  `numTerms`, `invertedSize`, `tagCaseSensitive`.

### 1.2 Capability summary

| Area | Verdict |
| --- | --- |
| Named graphs, `GRAPH.LIST`, `GRAPH.DELETE` | Work. Delete is a full purge — indexes and constraints go too. |
| Unique node constraint | Works, **requires a supporting index first**, creation is **async**. |
| Unique relationship constraint | Works. Different error text from the node form. |
| Mandatory constraint | Works, no index needed. |
| Range index + `GRAPH.EXPLAIN` | Works; `Node By Index Scan` confirmed for `=` and range predicates, strings included. |
| Index/constraint introspection | `db.indexes()`, `db.constraints()`, `db.labels()`, `db.relationshipTypes()`, `db.propertyKeys()` — sufficient for idempotent bootstrap. |
| Full-text index and query | Works, with prefix, fuzzy, phrase, boolean and field-scoped syntax, and a **real varying relevance score**. |
| `MERGE` + `ON CREATE` / `ON MATCH`, `SET n += $map` | Work. |
| Parameters, map/list params, `UNWIND` batch upsert | Work, idempotent. |
| Single-query atomicity | Confirmed, with full rollback on mid-query error. |
| Vector index | Works, but via `CREATE VECTOR INDEX` DDL, not the `db.idx.vector.createNodeIndex` procedure. Not needed by this design. |

### 1.3 Confirmed hazards — these drive the design

1. **Variable-length traversal crosses tenants inside one graph.**
   Seeded `a{org:A}-[:E]->b{org:B}-[:E]->c{org:A}` with no direct `a→c` edge:

   ```
   MATCH p = (x:N {org:'A'})-[:E*1..2]->(y:N {org:'A'})
   RETURN x.id, y.id, [n IN nodes(p) | n.id+':'+n.org], length(p)
   → a, c, [a:A, b:B, c:A], 2
   ```

   Both endpoints filtered to org A; the path routes through org B. Undirected
   gives the same result. The only fix is a post-hoc
   `WHERE all(n IN nodes(p) WHERE n.org = $org)` — there is no inline predicate
   on intermediate nodes of a var-length pattern, and nothing enforces the
   guard. This is a per-query obligation that is easy to forget and impossible
   to check at compile time.

2. **A tenant predicate on a full-text query is a post-filter.**
   `GRAPH.EXPLAIN` shows `Filter` above `ProcedureCall` — every tenant's
   matches are materialized and then discarded.

3. **Reading a non-existent graph creates it.**
   `GRAPH.QUERY g_typo "MATCH (n) RETURN count(n)"` → `0`, and `g_typo` then
   appears in `GRAPH.LIST`. There is no "graph not found" error, so a typo'd
   org ID silently produces an empty graph rather than an error.

4. **Index creation is not idempotent and `IF NOT EXISTS` does not parse.**
   Second create → `Attribute 'email' is already indexed`.
   `CREATE INDEX IF NOT EXISTS ...` →
   `errMsg: Invalid input 'I': expected '=', CREATE INDEX ON or CREATE INDEX FOR`.
   Constraints behave the same way (`Constraint already exists`).

5. **Constraint creation is asynchronous.** `GRAPH.CONSTRAINT CREATE` returns
   `PENDING`; `CALL db.constraints()` later reports `OPERATIONAL`. Bootstrap
   must poll, not assume.

6. **`TIMEOUT` bounds reads only.**
   `GRAPH.RO_QUERY ... TIMEOUT 1` → `Query timed out`, but
   `UNWIND range(1,2000000) AS i CREATE (:Z {i:i}) TIMEOUT 1` completed
   with `Nodes created: 2000000`. A runaway write cannot be cut off per query.

7. **`=~` is not supported**: `FalkorDB does not currently support =~`.
   `CONTAINS` / `STARTS WITH` / `ENDS WITH` work but **never** use an index —
   `GRAPH.EXPLAIN` shows a full `Node By Label Scan` even with a RANGE index on
   the property. Full-text is the only indexed lexical path.

8. **No timezone-aware temporal type.** `datetime()` → `Unknown function
   'datetime'`. Only `localdatetime()`, `date()`, `localtime()`, `duration()`,
   `timestamp()` exist.

9. **RFC3339Nano strings do not order correctly.** See §5 — this one is subtle
   and is a correctness bug waiting to happen.

10. **Constraint violation messages carry no property name or value**, so the
    adapter must map an error back to a field by knowing which constraint it
    created.

11. **No multi-statement transactions.** The atomicity boundary is exactly one
    `GRAPH.QUERY`.

### 1.4 Go client

Pin **`github.com/FalkorDB/falkordb-go/v2 v2.1.0`** (BSD-3-Clause,
`Copyright (c) 2018, Redis Labs`), published 2026-01-15.

- The bare path `github.com/FalkorDB/falkordb-go` is the legacy line; its
  `@latest` is `v0.1.0` (2024-06-03) and its `v1.0.0` is a proxy ghost whose
  tag no longer exists upstream. `v2.0.0` is unusable — the proxy rejects it
  (`go.mod has non-.../v2 module path`).
- Driver is `github.com/redis/go-redis/v9 v9.17.2`. `ConnectionOption` is a
  **type alias** for `redis.Options`, so auth, TLS, pooling and timeouts are
  plain go-redis fields.
- Maintenance is thin: last substantive commit 2026-01-15, 3 open issues and
  13 open PRs, 24 stars. Treat it as a codec, not a supported driver.
- `tablewriter` + `fatih/color` + `clipperhouse/*` (8 transitive deps) are
  pulled in solely for `QueryResult.PrettyPrint()`.

**Blocking defect: no `context.Context` anywhere in the client API.**
`falkordb.go:11` is a package-level `var ctx = context.Background()` used by
every wrapped method. There is no `QueryWithContext` and no `WithContext`
anywhere in the package.

That is disqualifying for a worker with shutdown semantics — but it is
routable, and this was verified end to end (§2).

Other client defects to design around:

- `ToString` **panics** on anything outside
  `nil, string, int, int64, float64, bool, []interface{}, []string, map[string]interface{}`.
  Verified panicking live on `int32` and `time.Time`; also panics on `uint64`,
  `float32`, `[]int64`, `map[string]string`. Note `Node.ID` is `uint64` while
  Cypher `id(n)` returns `int64`, so feeding a node ID straight back panics.
- `CallProcedure` is **broken**: `graph.go:99` is `for arg := range args`,
  which ranges a slice by index and stringifies `0,1,2…`. Live proof — the raw
  call returns hits, `CallProcedure` with identical args returns empty **with
  no error**. Never use it with arguments.
- `GraphSchema` has no mutex and is mutated lazily during result parsing. A
  `*Graph` shared across goroutines is a data race.
- `QueryResult` is fully buffered; no streaming.
- Errors are untyped — `"Query timed out"` must be string-matched.

---

## 2. Client boundary: use the library as a codec, not as a client

Verified working, with a real context on every call:

```go
g := db.SelectGraph(graphKey)              // pure local ctor, no I/O

text := falkordb.BuildParamsHeader(params) + cypher   // both exported
raw, err := db.Conn.Do(ctx, "GRAPH.QUERY", graphKey, text, "--compact").Result()
qr, err := falkordb.QueryResultNew(g, raw)            // exported, no reshaping
for qr.Next() {
    rec := qr.Record()
    n := mustGet(rec, "r").(*falkordb.Node)  // n.Labels, n.Properties
}
```

Live output:

```
HEADER="CYPHER k=\"acme/ops\" n=7 "
WRITE err: <nil>
PARSE err: <nil>
col r ok=true type=*falkordb.Node
  labels: [Repo] props: map[id:acme/ops n:7]
CANCELLED err: context canceled
```

and, separately, `1ns deadline err: context deadline exceeded | is DeadlineExceeded: true`.

Three constraints on this pattern, all verified:

- **`--compact` is mandatory.** Without it the parser panics:
  `interface conversion: interface {} is string, not []interface {}`.
- **`QueryResultNew` is not guaranteed I/O-free.** On first sighting of a new
  label / relationship type / property key it refreshes its schema cache via
  the package-global background context. Pre-warm the schema per `*Graph` at
  startup, and give each goroutine its own `SelectGraph` handle (free) to avoid
  the data race.
- **Own a param-normalizing shim** in front of `BuildParamsHeader` that
  converts to the nine safe types and returns an error for anything else, so
  `ToString` can never panic. Strings go through `strconv.Quote`; a live
  round-trip of `he said "hi"\nand \ that` was byte-correct, so quote and
  backslash injection through string *values* is blocked.

This buys us roughly 580 LOC of compact-mode decoder we do not write (integer
offset indirection for labels/reltypes/property keys, a 13-case scalar type
switch, recursive array/map/path decoding, the statistics block) while keeping
a real context on every call.

Belt and braces on timeouts: pass the client `ctx` **and** append
`TIMEOUT <ms>` for reads. Per §1.3(6) the server-side timeout does not bound
writes, so the client context and `redis.Options.ReadTimeout` are the only
bound on a runaway write.

---

## 3. Tenancy: one graph per organization — recommended

**Recommendation: one FalkorDB graph key per organization**, keyed by the
existing `graphID(prefix, orgID)` — `acr-cf-<sha256_128hex>`, server-derived,
never caller-supplied. This is the same identity function `zepgraph` already
uses, so the two backends agree on graph identity for free.

Why, in order of weight:

1. **The var-length crossing hazard becomes structurally impossible.** In a
   shared graph, tenant isolation depends on remembering
   `all(n IN nodes(p) WHERE n.org = $org)` on every single traversal — an
   unenforceable per-query obligation whose failure mode is a silent
   cross-tenant leak. With one graph per org the query is issued against the
   org's own graph key and there is nothing else in it. A forgotten predicate
   can only ever fail *within* one tenant.
2. **`PurgeOrganization` becomes `GRAPH.DELETE`** — one command, no scan, no
   residue, and indexes and constraints go with it. In a shared graph, purge is
   a `MATCH ... DELETE` sweep that must be paged, is not atomic across pages,
   and leaves index entries behind.
3. **Full-text stops paying for other tenants.** Per §1.3(2) the org predicate
   is a post-filter, so a shared graph materializes every tenant's matches on
   every search and then throws them away. Search cost grows with total corpus
   size rather than with the tenant's own.
4. **Per-graph indexes and constraints.** A unique constraint on
   `node_key` means what it says, rather than needing `(org, node_key)`
   composite semantics.
5. **Blast radius.** A bad migration, a bad bootstrap, or a bad purge damages
   one tenant.

Costs, stated honestly:

- One Redis key per org, each with its own index and constraint structures.
  Memory per graph is not free; a deployment with very many small orgs pays
  overhead a shared graph would amortize. **Unverified:** whether FalkorDB
  Cloud or an enterprise tier caps graph count. This must be checked before
  committing to a hosted plan.
- Bootstrap runs per graph, lazily on first write (§4.4), rather than once as
  a migration.
- `GRAPH.LIST` enumerates all org graphs. Because the key is a SHA-256 digest
  of the org ID, that enumeration leaks a count, not identities. Keep it that
  way — do not switch to a readable key.
- No cross-org queries. Context Fabric never wants one.

Two consequences worth writing down:

- **Keep `nodeUUID` org-derived anyway.** Even though the graph key already
  scopes the org, deriving node identity from `orgID + kind + canonical_id` (as
  `zepgraph` does) preserves the `verifiedNodeSubject` invariant: a node whose
  attributes do not hash back to the key it was fetched under is detectably
  wrong. That keeps a cheap, defense-in-depth check available against a restore
  into the wrong graph key, and keeps identities identical across the two
  backends.
- **The auto-create hazard (§1.3(3)) inverts `ensureGraph`.** `zepgraph` does
  `GetGraph` → 404 → `CreateGraph`. FalkorDB has no such thing; any read
  creates the graph. So "does this org's graph exist" is not answerable by
  reading it. Use `GRAPH.LIST` (or a bootstrap marker node) when existence
  actually matters, and treat the watermark-not-found path as "graph exists but
  has no watermark node", which is the only shape FalkorDB can express.

---

## 4. Schema and write path

### 4.1 Node shape

```
(:Subject:<Kind> {
   node_key:      "<orgID-derived deterministic UUID>",   -- unique, indexed
   canonical_id:  "...",
   subject_kind:  "repository" | "team" | ... ,
   label:         "...",
   authorization_repositories: "|acme/ops|acme/pay|",     -- encodeScope, unchanged
   authorization_projects:     "*",
   authorization_teams:        "*",
   evidence_refs:              "|ev-1|ev-2|",
   source_version:             "...",
   observed_at:      "2026-01-01T00:00:00Z",              -- display / read-back
   observed_at_ns:   1767225600000000000,                 -- comparison (see §5)
   valid_from_ns:    ..., valid_to_ns: ...,
   search_text:      "<label + aliases + previous names>" -- fulltext-indexed
})
```

Documents and episodes are the same shape under `:Content` / `:Episode`
labels, with `body` / `goal` / `outcome` / `summary` folded into `search_text`
and kept as separate properties for read-back.

Bookkeeping nodes (`organization-root`, `projection-watermark:<source>`) get a
**reserved label** `:_AcrInternal` rather than being ordinary
`Organization` / `Metric` subjects. Exclusion then happens structurally — reads
never `MATCH` that label — instead of by filtering canonical IDs after the
fact. Keep `isInternalBookkeepingSubject` as a second layer anyway; it is cheap
and it is what the existing tests assert.

### 4.2 Edge shape

```
()-[:<REL_TYPE> {
   relationship_id: "...",        -- unique, indexed
   derivation: ..., epistemic_status: ...,
   authorization_*: ..., evidence_refs: ...,
   observed_at_ns:, valid_from_ns:, valid_to_ns:
}]->()
```

### 4.3 Upsert — one query, no read-modify-write

```cypher
MERGE (n:Subject {node_key: $node_key})
ON CREATE SET n += $attrs
ON MATCH  SET n += $attrs
```

`SET n += $map` is a **merge**, verified live: starting from
`{k:'a', keep:'old', over:'old'}` and applying `{over:'new', fresh:'yes'}`
yields `{k:'a', keep:'old', over:'new', fresh:'yes'}`. That is exactly
`mergedSubjectAttributes`' contract — fresh scalars layered over previously
projected canonical metadata — executed server-side in one atomic query with
no read.

**This eliminates the read-merge-write race that ADR 0007 documents as
unfixable.** See §10.2 for what that does and does not mean for the port
contract.

Batch writes use `UNWIND $rows AS row MERGE ... ON CREATE SET n += row ON
MATCH SET n += row`, verified idempotent across re-runs (second run created
only the genuinely new row and updated the changed one).

### 4.4 Bootstrap

Per graph, lazily on first write, cached in memory per org:

1. `CALL db.indexes()` / `CALL db.constraints()` to see what exists.
2. Create only what is missing — index creation is **not** idempotent and
   `IF NOT EXISTS` does not parse (§1.3(4)), so introspect first and treat
   `Attribute 'x' is already indexed` / `Constraint already exists` as success
   for the concurrent-bootstrap race.
3. Unique constraints need their supporting index created **first**.
4. Poll `db.constraints()` until `status = OPERATIONAL` before relying on a
   constraint — creation returns `PENDING` (§1.3(5)).

### 4.5 Tombstones

```cypher
MATCH (n:Subject {node_key: $node_key})
WHERE n.observed_at_ns IS NULL OR n.observed_at_ns <= $effective_ns
DELETE n
```

A stale out-of-order tombstone matches nothing, structurally, in one query —
replacing `deleteNodeIfNotNewer`'s read-then-decide round trip. The
`IS NULL` arm preserves `tombstoneIsStale`'s existing behavior: a target with
no parseable timestamp is treated as not-stale and the delete proceeds.

`PurgeOrganization` is `GRAPH.DELETE <graphKey>`, treating
`ERR Invalid graph operation on empty key` as success so re-purge stays
idempotent (the analogue of `zepgraph` swallowing a 404).

---

## 5. Temporal storage — store epoch nanos, not RFC3339 strings

This is the sharpest correctness trap found, and it is not obvious.

Go's `time.Format(time.RFC3339Nano)` **trims trailing zeros**, so a whole-second
timestamp renders with no fractional part while a sub-second one renders with
one. Lexicographic comparison then breaks, because `'.'` (0x2E) sorts before
`'Z'` (0x5A):

```
MATCH (t:T) RETURN t.n, t.s ORDER BY t.s ASC
→ frac   2026-01-01T00:00:00.5Z
  whole  2026-01-01T00:00:00Z          <-- earlier instant sorts LAST

MATCH (t:T) WHERE t.s > '2026-01-01T00:00:00Z' RETURN t.n
→ (empty)                              <-- should have matched the .5Z node
```

The range filter silently returns the wrong answer. Every temporal predicate
this design needs — tombstone staleness, `valid_from` / `valid_to` windows,
watermark comparison — would be affected.

The same probe with epoch nanos is correct:

```
MATCH (t:T) WHERE t.ns > 1767225600000000000 RETURN t.n
→ frac
```

(A separate probe found ISO strings comparing and ordering correctly — that
was with uniform-width strings, which is exactly the case that hides this bug.
Uniform width is not something the projection path can guarantee.)

**Ruled: adopted.** `observed_at` stores the RFC3339Nano string for
read-back fidelity and byte-parity with `zepgraph`'s attributes;
`observed_at_ns` stores an `int64` epoch-nanosecond value and every
comparison and `ORDER BY` uses `_ns`, never the string. Same for
`valid_from` / `valid_to`. Index the `_ns` variants. The team lead's ruling
is broader than just this design: **comparisons never use RFC3339Nano
strings, full stop** — this is now the standing rule for any future
timestamp-comparison code in this backend, not a one-off fix. The test plan
(§9) adds a regression test with mixed-width fractional values (a
whole-second and a sub-second timestamp in the same comparison) to lock
this in, since that mixed-width case is exactly what a uniform-width probe
hides.

Do not reach for FalkorDB's native temporal types: there is no timezone-aware
`datetime()` (§1.3(8)), only `localdatetime()`, so a native type would silently
drop the zone the domain model carries.

---

## 6. Retrieval

### 6.1 Lexical search replaces hybrid search

`zepgraph` calls one Zep hybrid-search endpoint with an RRF reranker.
FalkorDB has no semantic retrieval — full-text is a RediSearch lexical index.
Verified syntax:

| Form | Example | Result |
| --- | --- | --- |
| term | `'gateway'` | d1, d3 |
| prefix | `'gate*'` | d1, d3 |
| fuzzy | `'%paymnt%'`, `'%%gatway%%'` (distance 2) | d1, d2 / d1, d3 |
| phrase | `'"payment gateway"'` → d1 (1.5); `'"gateway payment"'` → empty | true adjacency |
| AND (default) | `'payment gateway'` | only d1 |
| OR | `'payment\|retry'` | d2 (4.5), d1 (0.5) |
| NOT | `'gateway -config'` | d1 only |
| field-scoped | `'@body:nginx'` → d3; `'@title:nginx'` → empty | real scoping |

Two behavioral consequences:

- **Space is AND.** `DiscoverContext` currently passes the whole question text
  as one query. Against FalkorDB that ANDs every word and will almost always
  return nothing. The adapter must tokenize and join with `|`, or issue one
  query per term. `ResolveSubjects` already loops per term, so it fits as is.
- **`@nosuchfield:x` returns empty silently**, with no error. Field names must
  come from a fixed internal vocabulary, never from caller text.

Multi-property full-text indexes must be built **incrementally** — passing
`('Doc','title','body')` when `title` is already indexed errors with
`Attribute 'title' is already indexed`; calling again with just `body` works
and yields `{title: [FULLTEXT], body: [FULLTEXT]}`.

### 6.2 Scores are real but unbounded — and must not go through `resultConfidence`

Full-text scores vary meaningfully (`goroutine` → 4, `pay*` → 0.5,
`payment|retry` → d2 4.5 / d1 0.5), so there is a usable ranking signal. But
they are **unbounded above**, and the existing normalizer is actively wrong for
that shape:

```go
if *score > 1 { return clamp(1 / *score) }   // reader.go:995
```

A 4.5-scoring hit becomes 0.22 while a 0.5-scoring hit stays 0.5 — the
ordering **inverts**. `resultConfidence` was written for Zep's already-0..1
relevance and must not be fed a RediSearch score.

The adapter must normalize before the shared ranking helper sees anything —
max-normalize within a result set, or use rank position. That normalization is
backend-specific and stays in `falkorgraph`; the shared helper stays
score-agnostic and continues to consume a 0..1 confidence.

**This is the single largest behavioral risk in the port.** The commit
thresholds in `resolveFromMergedCandidates` — `>= 0.72` for a lone candidate,
`>= 0.88` with a `>= 0.12` gap for a top-two — were tuned against Zep relevance
values. Any normalization we pick changes which questions auto-commit and which
return a clarification prompt. Proposed deterministic ladder, to be reviewed on
real data rather than assumed:

| Case | Confidence |
| --- | --- |
| exact canonical hint resolved | 1.0 (unchanged) |
| exact label / alias match, case-insensitive | 1.0 (unchanged) |
| sole full-text hit, label-field match | 0.75 (clears the 0.72 lone-candidate gate) |
| alias / previous-name field hit | 0.60 |
| body-only hit | 0.50 (today's default) |
| traversed observation parent | × 0.85 (unchanged) |

Without the 0.75 rung, a single relevant lexical hit never reaches 0.72 and
every such question degrades to a clarification prompt — a visible regression.

### 6.3 Second-hop verification disappears

`zepgraph` needs `verifiedNodeSubject`, `fetchSecondHop` and the
`second_hop_node_*` degraded-reason counters because Zep's `GetNode` is a
UUID-only lookup with no graph scoping, so an edge endpoint has to be fetched
separately and then proved to belong to the caller's org.

With graph-per-org, one Cypher query returns the whole path from inside the
org's own graph. There is no unscoped lookup and no second hop. So:

- `verifiedNodeSubject` is retained as a cheap assertion (§3), not as a
  security boundary.
- `falkorgraph` will legitimately **never** emit `second_hop_node_unavailable`,
  `second_hop_node_unauthorized` or `second_hop_node_unverified` in
  `Coverage.DegradedReasons`.

Therefore the shared admission helper must accept drop-reason counters as an
**input from the adapter**, not own them. Otherwise the helper hard-codes a
vocabulary only one backend can produce.

### 6.4 Authorization stays in Go

Keep the pipe-delimited `encodeScope` attribute format exactly as `zepgraph`
writes it, and keep filtering in Go via `scopeContains` / `authorizedAttributes`
after reading.

Do **not** push scope predicates into Cypher. A `CONTAINS "|slug|"` predicate
would be a partial reimplementation of `scopeContains` — it cannot express the
`owner/*` wildcard rule that mirrors `internal/auth.RepositoryAllowed`, and two
implementations of an authorization rule will drift. Per §1.3(7) `CONTAINS`
would not use an index anyway, so there is no performance argument for it.

The cost is over-fetching rows that are then filtered out. Bound it with the
existing `MaxResults` / `LIMIT` and accept it. The org predicate itself needs
no pushdown at all — it is structural.

---

## 7. Extract the ranking logic first — as its own commit — DONE

**Status: landed.** `internal/contextfabric/graphrank` exists, `zepgraph` is
rewired to call it, and the full existing test suite (all ~51
`zepgraph` tests, including every `ResolveSubjects`/`DiscoverContext`/
`identity_test.go` scope test, plus the whole repo's `go test ./...`) passes
unmodified — the proof that the extraction is behavior-preserving. Committed
as its own commit (`refactor(contextfabric): extract graph ranking logic
into graphrank`) before any `falkorgraph` code was written, per the ruling
below.

The ranking, admission and budget logic in `reader.go` was backend-neutral
already and encodes a long tail of adversarial-review fixes (N2–N6, P1-1,
P2-4, G5, Codex rounds 3 and 4). Reimplementing it in a second adapter would
mean reimplementing those fixes, and they would drift — that was true of both
sessions' independent framing of this section and is why it survived
reconciliation unchanged in substance.

**Ruling on scope (§10.5's conflict, resolved):** the first cut of this
extraction lifted the *whole* `ResolveSubjects`/`DiscoverContext`
orchestration behind `ResolveDeps`/`DiscoverDeps` dependency structs,
including Zep's second-hop fetch-and-verify machinery
(`FetchSecondHop`/`VerifySecondHopSubject`, the `second_hop_node_*`
degraded-reason vocabulary) — exactly the over-reach this section's original
draft warned about (§6.3, §10.5): a backend with no second-hop concept
(one-graph-per-org resolves a whole path from a single query) would have had
to fake an implementation of a Zep-shaped interface just to satisfy the
shared struct. The team lead accepted that critique. **`graphrank` was
shrunk** to:

- `ResolveDeps`/`graphrank.ResolveSubjects` — kept as orchestration, because
  `ResolveSubjects`'s I/O (exact-hint lookup, node search, observation
  traversal) has no backend-specific "shape" beyond the I/O calls
  themselves; the decision logic dominates.
- `graphrank.AdmitEdges` — **not** an orchestrator. It takes
  `[]ResolvedEdge`, edges whose endpoints the calling adapter has *already*
  fully resolved to `SubjectRef` however it needed to (Zep's first-hop-trust
  + second-hop-fetch-and-verify, or falkorgraph's single whole-path query).
  `graphrank` owns only the pure admission decision: self-loop/
  internal-bookkeeping exclusion, the evidence budget, path/driver
  construction and validation, and the aggregate evidence/fact-requirement
  lists. `graphrank.SortEdgesByRelevance` is a small pure sibling the
  adapter calls first (sorting only needs relevance/score/UUID, all present
  pre-resolution).
- `graphrank.DiscoveredCohort` — unchanged from the first cut; it was already
  pure (reads only already-fetched first-hop nodes, no second-hop concept
  ever touches it).

`zepgraph`'s `DiscoverContext` now owns the full second-hop
fetch-and-verify loop directly (restored to essentially its pre-extraction
shape for that part), builds a `[]graphrank.ResolvedEdge` from whatever
survives it, and calls `graphrank.AdmitEdges` for the decision. `Coverage`
(`Partial`/`DegradedReasons`) is built by the adapter from its own
second-hop drop counters — `graphrank` never computes or owns that vocabulary,
per §6.3's original point: the shared helper must accept drop-reason
counters as an adapter-owned input (or, as it turned out, not need them at
all), never own a vocabulary only one backend can produce.

What actually moved (final, not the original table's proposal):
`resolveFromMergedCandidates`, `finalizeExactResolution`,
`anyCallerSourced`, `clarificationPrompt`, `nodeCandidate`,
`traverseObservationToSubject` (with its I/O parameterized via callbacks,
not orchestrated), `resultConfidence`, `clamp`, `relationMeaning`,
`relationTitle`, `normalizeRelation`, `safeAttributeName`,
`isObservationSubjectKind`, `isObservationAttributionRelation`,
`subjectKey`, `subjectTerms`, `uniqueSorted`, the wildcard/owner-wildcard
matching core of `scopeContains` (as `graphrank.ScopeMatch`, operating on an
already-decoded `[]string` — the pipe-encode/decode scheme itself and the
fail-closed sentinel stay in `zepgraph`, since a native-list backend like
falkorgraph never needs them), and `deterministicUUID`. **Not** moved:
`isInternalBookkeepingSubject` (a Zep-specific concept — falkorgraph has no
anchor/marker nodes, so its own predicate is trivially always-false, not a
port of this one), `graphID`/`nodeUUID`/`contentUUID`/`relationshipUUID`
(Zep-specific graph/node addressing; falkorgraph derives its own node keys
per §4.1, using the same `deterministicUUID` primitive for consistency but
different composition), and the second-hop machinery discussed above.

**Existing tests stayed unmodified**, including the ones that call moved
functions directly (`TestIsInternalBookkeepingSubjectIsCaseInsensitive`,
`resultConfidence`, `edgeEvidence`, and the full `identity_test.go` scope
suite) — those three names were kept as thin zepgraph-local wrappers
delegating to `graphrank` rather than aliased away, since they're each
independently exercised by a direct test call and the wrapper is one line.

**Phase 2 (not started):** `falkorgraph` implements transport, query
construction, and mapping into `CandidateNode`/`CandidateEdge`/
`ResolvedEdge`, and calls `graphrank` for every ranking decision.

---

## 8. Infrastructure

Add a `falkordb` service to `deploy/compose/acr.compose.yml` alongside
`postgres` and `clickhouse`, pinned by digest exactly like they are:

```yaml
falkordb:
  image: falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3
  healthcheck:
    test: ["CMD", "redis-cli", "GRAPH.QUERY", "_acr_health", "RETURN 1"]
```

The healthcheck issues a real `GRAPH.QUERY` rather than `PING`, so it proves
the graph module is loaded and not merely that Redis is up. Note that this
creates the `_acr_health` graph as a side effect (§1.3(3)) — that is fine and
intentional, but it should be a reserved name.

Environment contract, mirroring the ADR 0007 table and the
`internal/config.SecretValue` `KEY` / `KEY_FILE` convention:

| Variable | Purpose | Default |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_FALKOR_ADDR` | `host:port` | — (unset = adapter not constructed) |
| `ACR_CONTEXT_FABRIC_FALKOR_PASSWORD` / `_FILE` | auth | — |
| `ACR_CONTEXT_FABRIC_FALKOR_TLS` | require TLS | `true` outside development |
| `ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX` | server-owned key prefix | `acr-cf` |
| `ACR_CONTEXT_FABRIC_FALKOR_REQUEST_TIMEOUT` | per-request | `30s` |
| `ACR_CONTEXT_FABRIC_FALKOR_MAX_RESULTS` | bounded page size, 1–50 | `25` |
| `ACR_CONTEXT_FABRIC_FALKOR_POOL_SIZE` | go-redis pool | `10` |

Mirror `zepgraph.Configured` — an unset address means the deployment did not
opt in and no adapter is constructed, so it never fails closed over a
dependency it did not choose.

Unlike Zep, **this needs no external credential**, which is what makes an
always-running live test possible. That is the main operational win.

Licensing: the FalkorDB **server** is SSPL. We consume it as a deployment
dependency (a container image), not as linked code — the same posture as
Postgres or ClickHouse — and the Go client is BSD-3-Clause. There is no
automated dependency-license gate in this repo to satisfy. The image should
still be declared in `docs/container-images.md`.

---

## 9. Test parity

`make test` is `go test -count=1 ./...` and already runs
`pgprojection`'s testcontainers-based integration tests with **no env gate and
no skip**. `testcontainers-go v0.43.0` is already in `go.mod`. So the live
FalkorDB lifecycle test follows that established pattern exactly — a generic
container with a wait strategy that issues a real `GRAPH.QUERY`, and plain
`require.NoError`. No new dependency, no new CI shape, and no env-gated skip of
the kind that let the Zep live test sit unrun.

Mapping the 51 existing `zepgraph` tests:

**Move to `graphrank` (13, pure ranking; `zepgraph` keeps thin delegation
tests):** the exact-hint truncation and receipt-vs-caller precedence tests, the
hybrid-merge and shared-parent-retention tests, the ambiguity/clarification
pair, `TestIsInternalBookkeepingSubjectIsCaseInsensitive`, and the four
`DiscoverContext` budget/ordering tests (evidence truncation, higher-relevance
admission regardless of backend order, rejected-path budget, deterministic tie
break).

**Need a `falkorgraph` twin (~34):** graph identity and org isolation;
projection of every kind with temporal triples and watermark; idempotent
replay; tombstones across all four kinds plus the stale and nil-target cases;
purge and idempotent re-purge; watermark not-found; authorization filtering
including both wildcard forms; alias and previous-name resolution; the six
observation-traversal tests; cancellation.

**No analogue (4):** the SDK transport tests
(`TestSDKAPIGetCallsRetryBoundedAttemptsOnServerErrors`,
`TestSDKAPIBodyBearingCallsMakeExactlyOneRequestOnServerError`,
`TestSDKAPIUsesPinnedClientBaseURLAuthenticationAndSafeRateLimitClassification`,
`TestNewSDKAPIIsPinnedAndConstructible`). Replace with: pinned-version
constructibility, auth/TLS option wiring, context cancellation and deadline
propagation through `Conn.Do`, server-side read timeout, and error
classification against the verbatim strings `Query timed out`,
`unique constraint violation on node of type X`, and
`ERR Invalid graph operation on empty key`.

Worth stating plainly: **the ADR 0007 retry caveat disappears.** That whole
section exists because `zep-go` v3.22.0 re-issues the same `*http.Request`
without rewinding the body, forcing `MaxAttempts(1)` on all body-bearing calls.
go-redis rebuilds its request per attempt and has no equivalent race, so
bounded retry on writes and searches is simply available.

New tests this design specifically needs, because each corresponds to a
verified hazard:

- epoch-nanos ordering, with a whole-second and a sub-second timestamp, to lock
  in §5 (ruled mandatory — see §5);
- bootstrap idempotency under a concurrent second bootstrap of the same org;
- constraint `PENDING` → `OPERATIONAL` polling;
- a var-length traversal that would cross tenants in a shared graph returning
  nothing, proving the graph-per-org boundary;
- `AND`-vs-`OR` tokenization of a multi-word question, proving `DiscoverContext`
  does not silently return empty;
- score normalization ordering (a 4.5 hit must outrank a 0.5 hit);
- **auto-create-on-read (§1.3(3), ruled):** a read against an org whose graph
  was never written to must be *detected* as "no such org graph yet" through
  a deliberate existence check (`GRAPH.LIST`, or a bootstrap marker node —
  §3's inverted `ensureGraph`), not discovered after the fact by noticing
  FalkorDB silently created an empty graph key for it. Prove both that (a)
  the deliberate existence check correctly reports "absent" before any read
  touches that graph key, and (b) if a read must still happen first for some
  path, the graph key FalkorDB creates as a side effect is empty/harmless
  and does not desync from the bootstrap-idempotency check in (2) above --
  a stray empty graph key must not make a later real bootstrap think the
  org's schema already exists.

---

## 10. Open questions for the orchestrator

1. **Does `falkorgraph` replace `zepgraph`, or sit beside it?** ADR 0007 is
   Accepted and names Zep as *the* backend. This note assumes a new ADR 0009
   that records the FalkorDB decision and states whether `zepgraph` is
   superseded, retained as a second selectable backend, or deleted. I have not
   assumed an answer; §7's shared-helper extraction is worth doing either way,
   and is strictly required if both are retained.

2. **Do we relax the per-org serialization precondition?** `ports.go` and ADR
   0007 both document, at length, that `ApplyProjectionBatch` callers must
   serialize per organization because the read-merge-write path has no CAS.
   FalkorDB's `SET n += $map` is atomic per node and removes that specific
   race. **My recommendation is to change nothing in the port contract**: the
   documented obligation is on callers of the *interface*, `zepgraph` still
   needs it, and batch-level ordering across entities is a separate concern
   from single-node attribute merging. `falkorgraph` should note that it
   satisfies the attribute-merge half independently — but the CHAOS-3753 worker
   should keep serializing. Flagging it because silently weakening a documented
   safety precondition would be exactly the wrong kind of change to make
   quietly.

3. **The confidence ladder (§6.2) needs a decision, ideally against real
   data.** The thresholds were tuned for Zep relevance and there is no
   equivalent signal. The proposed ladder is a starting point, not a
   validated one, and it changes how often users see a clarification prompt.

4. **Graph-count limits.** Whether a hosted FalkorDB tier caps the number of
   graphs is unverified and bears directly on §3. It should be checked before
   a plan is chosen.

5. **RESOLVED.** Two sessions were working the same seam — a session-compaction
   artifact (the note's author was spawned believing the original session had
   died mid-task), not a deliberate split. The team lead stood the extra
   session down and named one session sole driver of this worktree going
   forward. Both open sub-points were ruled on directly:

   - **Proof obligation:** satisfied. `zepgraph` is now rewired to
     `graphrank` and the full existing test suite (51 tests) plus the whole
     repo's `go test ./...` passes unmodified, committed as its own commit
     before any `falkorgraph` code. See §7.
   - **Boundary too wide:** accepted and fixed. The second-hop
     fetch-and-verify path and its `second_hop_node_*` vocabulary were pulled
     back out of `graphrank` into `zepgraph`; `graphrank.AdmitEdges` takes
     already-endpoint-resolved edges and never sees the concept. See §7 for
     the final shape.

   No further action needed here; kept for the record of how the conflict
   was actually resolved rather than deleting the history.
