# CHAOS-3832 T2 — HNSW efRuntime/efConstruction + over-fetch sweep

Status: MEASUREMENT-ONLY. No production default changed. Tooling landed
under `internal/contextfabric/falkorgraph` (`hnsw_sweep.go`, `hnsw_recall.go`,
plus HNSW-options/drop/over-fetch plumbing in `vector.go`); live probe run
2026-08-14 against a `GRAPH.COPY` of org `70d529e0-3c06-4597-8480-794fd02328b6`'s
graph (35,987 subjects, 100% embedded, 3-large/3072-dim).

Scope: spec `.remember/embed-text-spec-v2.md` §6 T2, §5 L2/L3, §7 D3.

## 1. §7 D3 live probe — index drop/recreate safety

Confirmed live, against a verified-matching `GRAPH.COPY` of the production
org graph (never the live graph itself):

- `DROP VECTOR INDEX FOR (n:Subject) ON (n.embedding)` removes only the index
  structure. Node properties (`embedding`, `embedder_identity`,
  `embedder_dimension`) are untouched — `MATCH (n:Subject) WHERE n.embedding
  IS NOT NULL RETURN count(n)` reported 35,987 before AND after the drop.
- `CREATE VECTOR INDEX ... OPTIONS {..., M, efConstruction, efRuntime}`
  re-indexes those same stored vectors with no re-embedding involved.
- Proven with content, not just counts: a self-similarity query
  (`MATCH (seed) WITH seed.embedding AS v CALL db.idx.vector.queryNodes(...)`)
  against one node's own vector returned byte-for-byte identical top-3
  neighbors and scores before and after a drop+recreate cycle at different
  HNSW parameters.
- Dropping an absent index fails with `"Unable to drop index on
  :Subject(embedding): no such index"` — classified into a new sentinel
  (`errIndexNotFound`, `config.go`/`client.go`) and tolerated by
  `dropVectorIndex`, mirroring `createVectorIndex`'s existing already-exists
  tolerance in the opposite direction.
- **No per-query efRuntime override exists on this pinned server** (graph
  module 42002): `CALL db.idx.vector.queryNodes(label, prop, k, vector,
  {efRuntime:N})` errors `"Procedure requires 4 arguments, got 5"`. efRuntime
  sweeps therefore require an index rebuild per value, exactly as the spec
  assumed — there is no cheaper query-time lever available on this version.
- Index build latency, measured live (M=16, 35,987 vectors, from-empty
  build): efConstruction 200 build cycle completed inside the request-path
  ~30s budget; the efConstruction 512 build took **~46s** to reach
  `OPERATIONAL` (23 polls at ~2s). This sets a hard planning number: a
  production efConstruction bump above 200 needs a rebuild window budget in
  the tens of seconds per organization at this corpus size, not a request-path
  operation.
- Live index confirmed the spec's premise exactly: the production index today
  reports `{dimension: 3072, similarityFunction: cosine, M: 16,
  efConstruction: 200, efRuntime: 10}` — FalkorDB's stated defaults, unedited
  since bootstrap.

Copy safety: node/vector counts verified equal to the source graph
immediately after `GRAPH.COPY`, before any probe query ran; the copy was
`GRAPH.DELETE`d after each probe; the live organization graph's node count and
index options were re-verified unchanged afterward.

## 2. Tooling shipped

- `hnswIndexOptions` + `createVectorIndexWithOptions` (`vector.go`): additive
  generalization of `createVectorIndex`; the zero value renders the identical
  OPTIONS clause `createVectorIndex` always sent (test-proven). No production
  call site passes a non-zero value.
- `dropVectorIndex` / `recreateVectorIndexWithOptions` (`vector.go`): not
  called from any production path; the sweep/probe primitive. Reads the
  pre-drop HNSW options first and, if EITHER the post-drop create errors OR
  the create succeeds but the subsequent poll times out, restores the
  original options (re-dropping the unconfirmed new index first, so the
  restore's create cannot be silently no-op'd by FalkorDB's idempotent
  already-indexed tolerance) — reported loudly either way (Luna round-1
  finding 2b, extended round-2 finding 2 to cover the poll-failure half).
- `vectorSearchNodesWithOverFetch` (`vector.go`): generalizes
  `vectorSearchNodes` with an explicit multiplier. `vectorSearchNodes` now
  delegates with multiplier=1, rendering byte-identical Cypher to before
  (test-proven). Formula: raw fetch `k' = (multiplier × limit) + 1`,
  preserving the existing `limit+1` truncation-sentinel contract exactly —
  `truncated` is still derived only from how many rows survive the org/tau
  post-filters beyond the caller's `limit`, never from the raw server row
  count.
- `RecallAtK`, `cosineSimilarity`, `BruteForceTopK` (`hnsw_recall.go`): pure,
  fully unit-tested, no live dependency. `ScoredID`, `TieExpandedTop`, and
  `RecallAtKTieTolerant` add tie-tolerant comparison at the k-th boundary
  (Luna round-1 finding 3) — real in this corpus, not a hypothetical: 78% of
  it is near-duplicate `ci_pipeline_run` text (spec §1), which projects to
  near- or exactly-identical embeddings.
- `RunHNSWSweep`, `vectorSweepSeedTopK`, `referenceTopKTieComplete`,
  `SweepBuildPoint`, `SweepResult` (`hnsw_sweep.go`): the live sweep runner —
  recreates the index once per distinct (M, efConstruction, efRuntime) point,
  runs every seed query against it, and reports recall@K (tie-tolerant,
  relative to the reference point's own top-K) plus p50/p95 query latency,
  index build time, and — Luna round-1 finding 1 — `Queries`/`SkippedSeeds`
  so partial coverage is always visible rather than silently folded into a
  clean-looking number. A point where every seed's query fails
  (`Queries==0`) still delivers its diagnostic result but makes
  `RunHNSWSweep` return a non-nil error: a zero-coverage sweep is not a valid
  measurement and must not read as a green pass. The reference build's
  top-K fetch (`referenceTopKTieComplete`) ESCALATES past the initial 2x
  overfetch window while the k-th boundary score is still tied with the
  window's own last row, up to `maxReferenceTieMultiplier` (64x) — a tie
  group larger than 2x was previously silently truncated (Luna round-2
  finding 3); a tie group that never resolves within the bound fails that
  seed closed (routed through the same `SkippedSeeds` accounting finding 1
  established, not a separate error path).
- `isSweepTargetSafe` (`hnsw_sweep.go`): the safety gate the live probe
  uses, rewritten per Luna round-2 finding 1 — FAIL-CLOSED against the org's
  ACTUAL production graph key, DERIVED at runtime via `graphKey()` (the
  SAME derivation `identity.go` uses for every real read/write), never a
  hardcoded list. Round-1's fix (an exact-match denylist of ONE known key)
  still failed open for a different prefix or a different, unlisted org — a
  derivation-based comparison cannot, because there is nothing to have
  forgotten to list. The target must ALSO exactly equal an independently
  operator-declared `expectedCopyKey` (a "state your intent twice" check);
  the round-1 `"copy"` substring heuristic is REMOVED entirely — Luna named
  it as adding nothing once the derivation-based comparison exists.
  Underivable inputs (empty prefix/org id/expected key) REFUSE, never pass
  silently.
- `hnsw_sweep_live_test.go`: the runnable live probe, gated behind dedicated
  `ACR_TEST_HNSW_SWEEP_*` env vars (never a production `ACR_CONTEXT_FABRIC_*`
  name) — now including `_ORG_ID`, `_GRAPH_PREFIX`, and
  `_EXPECTED_COPY_KEY`, all required for the derivation-based safety gate —
  and (finding 1) rejects any point with `SkippedSeeds > 0` rather than
  merely logging it.

### Scoping note: what this measures vs. T1's harness

This tool measures **ANN-algorithm recall** — how much of a higher-fidelity
setting's own top-K a lower setting actually returns, using each sampled
node's own stored vector as its query (a leave-one-in self-query; the
self-match term is identical across every swept setting and cancels out of
the comparison). It does **not** measure T1's **text-relevance recall**
(does the right *subject* come back for a real paraphrase question), which
needs the withheld 50-question corpus and lane-3831's oracle. The two are
deliberately different numbers (spec §5 L2: "the T1 oracle quantifies exactly
how many misses are ANN-attributable") — this tool is what makes that split
possible without needing the withheld corpus, not a replacement for T1's
number. **L3's own recall@multiplier number (spec §5 L3's stated test) is
therefore out of this lane's scope** — the over-fetch formula and
truncation-sentinel contract are proven correct here at the code level
(`TestVectorSearchNodesWithOverFetchFormula`,
`TestVectorSearchNodesWithOverFetchTruncationStillDerivedFromSurvivors`), but
a live recall number for L3 needs T1's harness/oracle and real org+tau
filtering pressure, which is out of T2's scope by design.

No client-side vector decoding was added to `client.go` for this: the pinned
`falkordb-go` client's `decodeValue` has no case for a vector-typed `RETURN`
value today (verified by reading it — only *writing* a vector via `vecf32()`
is exercised anywhere in this package). Every sweep query keeps the query
vector server-side (`WITH seed.embedding AS v CALL
db.idx.vector.queryNodes(...)`), which is why the reference point is "the
highest-fidelity setting this sweep's own range reaches" rather than a true
brute-force oracle — see the scoping note above for why that is the correct
boundary for T2 rather than a shortcut around one.

## 3. Live sweep results

Ran 2026-08-14 against `acr-cf-3832-sweep-copy-run2` (verified `GRAPH.COPY`
of the live org graph, 35,987/35,987 nodes with embeddings both before and
after copy; copy `GRAPH.DELETE`d after the run, live graph re-verified
unchanged — 35,987/35,987 nodes, index options still the untouched
`{M:16, efConstruction:200, efRuntime:10}` production default). 58 seeds
stratified across all 9 subject kinds present in the corpus (ci_pipeline_run,
pull_request, pull_request_review, deployment, project, repository, team,
organization, work_item), every seed present in every point's result set. k=20.
Reference point: M=16, efConstruction=512, efRuntime=200 (top of the swept
range — see §2's scoping note for why this is "best-in-range", not a true
brute-force oracle).

**Metric note (Luna round-1 finding 3, disclosed honestly).** This run's
numbers were computed with the ORIGINAL strict top-K comparison (the manual
driver script captured ids only, not scores), before `RecallAtKTieTolerant`
existed. Given this corpus is 78% near-duplicate `ci_pipeline_run` text with
correspondingly tied/near-tied embeddings (spec §1), some fraction of the
gap below 1.0 at every non-reference point is very likely boundary-tie noise
rather than a genuine ANN miss — the fixed tooling (`RunHNSWSweep`, now
tie-tolerant and score-aware) would very likely report SOMEWHAT higher
recall at every point, though the RELATIVE ordering across points (which is
what the §4 recommendation rests on) is unlikely to flip, since the same
tie-boundary noise affects every point's comparison against the same
reference. Re-running with the fixed tool is the correct way to get an exact
number; not done here to avoid a second live-contention round on top of the
one already documented below. Flagged as a residual (§5).

**Methodology note on this specific run.** The automated Go tool
(`hnsw_sweep_live_test.go`) hit a real limit twice under host contention
(other lanes' Docker load; verified via `docker stats` at 82% CPU on the
FalkorDB container) — a `CREATE VECTOR INDEX` rebuild at efConstruction=512
exceeded the adapter's `RequestTimeout`, which `Config.validate` caps at 2
minutes (a real production safety bound, not a test artifact). To get numbers
without fighting that ceiling, this run used a patient driver script issuing
the IDENTICAL Cypher the Go tool uses, with unbounded polling instead of a
client-side deadline. Recall numbers below are unaffected (same queries, same
computation); the reported per-query latency (p50/p95 ~45-57ms across every
point) is dominated by `docker exec` + `redis-cli` subprocess overhead
(confirmed against this session's earlier direct probes, which measured
FalkorDB's own `Query internal execution time` at <1ms-3ms server-side) and
should be read as "no measurable in-process latency difference across
settings at this scale," not as an absolute production latency number — the
Go tool (in-process, no subprocess overhead) is the correct instrument for an
absolute latency claim and is what production tooling should use going
forward.

| point (efConstruction, efRuntime) | recall@20 vs (512,200) | index build time | mean query latency* |
|---|---|---|---|
| (512, 200) — reference | 1.000 | 75s | 47.1ms |
| **(200, 10) — current production default** | **0.853** | **27s** | 46.2ms |
| (200, 50) | 0.915 | 28s | 47.2ms |
| (200, 100) | 0.956 | 27s | 47.1ms |
| (200, 200) | 0.979 | 29s | 45.4ms |
| (512, 10) | 0.932 | 73s | 46.8ms |
| (512, 50) | 0.955 | 85s | 46.0ms |
| (512, 100) | 0.967 | 76s | 47.3ms |

\* dominated by subprocess overhead this run, see methodology note — read as
"flat across settings," not as an absolute number.

Two independent effects are visible and separable:

- **efRuntime, holding efConstruction=200 (no rebuild-class cost change):**
  recall climbs monotonically 0.853 → 0.915 → 0.956 → 0.979 as efRuntime goes
  10 → 50 → 100 → 200, with build time flat at 27-29s throughout (efRuntime
  is encoded in the same index build, so this is not "free," but it is the
  SAME build-cost tier the corpus already pays today).
- **efConstruction, holding efRuntime fixed:** bumping efConstruction from
  200 to 512 costs roughly 2.5-3x the build time (27-29s → 73-85s) and buys
  LESS recall than simply raising efRuntime at efConstruction=200 would have
  — e.g. (512,10)=0.932 is below (200,100)=0.956, and (512,50)=0.955 is
  below (200,200)=0.979. At this corpus size, efRuntime is the cheaper lever
  by a wide margin; efConstruction is not obviously worth its cost here.

## 4. Recommendation (documented, not applied)

**Raise the production `efRuntime` default from 10 to 200; leave
`efConstruction` at 200 (M unchanged at 16).**

- Live-measured ANN recall@20 relative to the best-tested setting rises from
  0.853 (today's default) to 0.979 — the biggest single lever in this sweep,
  matching the spec's suspicion that `efRuntime=10` is a silent recall
  killer at 36k vectors.
- Costs the SAME index-build tier the corpus already pays (27-29s, vs 75-85s
  for any efConstruction=512 point) — it is a query-quality knob, not a
  rebuild-class one, on this pinned server version.
- No `efConstruction` bump recommended: the recall it buys is smaller than
  raising `efRuntime` alone, at ~3x the rebuild cost.
- No latency regression evidence either way at this corpus size — this run's
  methodology cannot resolve sub-few-ms differences (see the note above); a
  future in-process (Go tool) run is the right instrument if a tighter
  latency bound is needed before shipping.

**This is a recommendation only.** Per T2's sequencing rule (spec §6,
rev-4 rescoping), no production default changes in this changeset — a
production HNSW parameter change is a retrieval-policy change and must wait
for T3 phase 1 (the persisted `embed_retrieval_identity` /
`retrieval_policy_version` reuse-key columns + conjunctive `FindReusable`
predicates) to be serving fleet-wide first, per §4's rollout gate. When that
lands, `RetrievalPolicyVersion` is the mechanism to bump alongside this
change (spec §4, "Retrieval-policy changes without re-embedding").

## 5. Residuals / follow-ons

- A true brute-force oracle (needed to validate the reference point itself,
  not just relative recall among swept points) requires either (a) adding
  vector-typed `RETURN` decoding to `client.go`'s `decodeValue`, or (b)
  fetching the corpus through a different path. Neither is done here —
  scoped explicitly to T1/a future extension, see §2's scoping note. Once
  lane-3831 (T1)'s `fetchEmbedderFenceCorpus` / `bruteForceRank` merge,
  `RunHNSWSweep`'s reference point should switch to their true-oracle output
  (`ann_loss` framing) as a follow-up commit — not done in this changeset per
  team-lead's direction, kept as the documented no-oracle fallback.
- **The Go tool's client-side `RequestTimeout` (capped at 2 minutes by
  `Config.validate`) is a real operational limit, not just a test
  inconvenience**: under host contention this session, an efConstruction=512
  rebuild twice exceeded 100-115s client-side before the patient manual run
  (§3) finally completed the same rebuild in 73-85s. A production operator
  running `acr-projector rebuild --org` (or a future retrieval-policy rollout
  tool) with an efConstruction bump configured should expect this ceiling to
  bite under load and needs either a longer-lived, non-request-path execution
  mechanism, or explicit retry/resume handling — `RunHNSWSweep`'s new
  `onResult` callback and partial-results-on-error contract (added this
  session after the first timeout) are the minimum version of that, but a
  production rebuild path needs its own answer, out of T2's scope.
- **Index build latency is genuinely host-load-dependent, not a fixed
  number**: §1's very first probe (least contended point in this session)
  measured efConstruction=512 at ~46s; §3's full sweep (running later,
  alongside other lanes' work, confirmed via `docker stats` at up to 82% CPU
  on the FalkorDB container) measured the same configuration at 73-85s, and
  the automated Go tool twice failed to complete it within a 100-115s client
  timeout at the worst-contended moments. Treat §3's table as this session's
  measured values under real contention, not a guaranteed constant — whoever
  sizes the T3 rebuild-window budget should budget for the high end (≥100s
  per efConstruction=512 rebuild at this corpus size under load), not the
  uncontended §1 figure.
- This sweep's 58-seed self-query set is a reasonable spread across kinds but
  is NOT the T1 paraphrase corpus; treat its recall numbers as ANN-fidelity
  signal, not as a stand-in for AC-3778-2's lift measurement.
- **§3's reported numbers predate the tie-tolerant recall fix** (Luna round-1
  finding 3): they were computed with a strict top-K comparison, before
  `RecallAtKTieTolerant`/`TieExpandedTop` existed. A re-run with the now-fixed
  `RunHNSWSweep` against a fresh `GRAPH.COPY` is the correct way to get an
  exact number — likely somewhat HIGHER recall at every non-reference point,
  given this corpus's known near-duplicate density, with the relative
  ordering across points unlikely to change. Not re-run in this changeset to
  avoid a second live-contention round; flagged here rather than silently
  left as if the numbers were final.
