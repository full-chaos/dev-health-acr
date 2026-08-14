# CHAOS-3781 — historical time axis: design note

Status: IMPLEMENTED. All six decisions ruled by team-lead; see §7 for what
was decided and §9 for what changed relative to the proposal.
Scope source: TRD §19.8 (AC-3781-1..7), drift item D5.

---

## 0. Probe findings (load-bearing; everything below rests on these)

Verified against the live stack, not assumed.

**P1 — the graph carries NO canonical validity windows today.**
`internal/contextfabric/devhealthsource/**` never sets `ValidFrom`/`ValidTo` on any
`ContextFabricEntityProjection` or `ContextFabricRelationshipProjection` (zero hits in the whole
package). CHAOS-3785 made the owned write authoritative, so a `nil` is now written as an explicit
Cypher null. Result: every canonical node and edge in a projected graph has
`valid_from_ns`/`valid_to_ns` **absent**. The only exception is the episode node
(`falkorgraph/projection.go:358`), which sets them from `StartedAt`/`EndedAt`.

Consequence: "graph reads respect validity windows" is a **no-op** against today's data. AC-3781-4
("an edge whose validity window excludes the requested time is not returned") is vacuously true and
delivers nothing. Filtering alone would return the current graph under a historical label — the
exact H6 defect the refusal exists to prevent.

**P2 — graph reads apply no temporal predicate at all today.**
`falkorgraph/queries.go` MATCH clauses filter on `org_id`/`subject_kind`/`canonical_id` only.
`valid_from`/`valid_to` are read back and passed to `graphrank` as display data
(`discover.go:124`); nothing drops an expired edge. So even a *current* answer today can cite an
edge that ended long ago.

**P3 — ClickHouse `valid_time` as-of is real and works.**
`default.repo_metrics_daily`: 149 distinct `day` values spanning 2025-12-27 → 2026-08-13.
Live probe:

| query | winning row |
|---|---|
| `WHERE day <= '2026-03-01'`, `row_number() … ORDER BY day DESC` | `2026-02-28` |
| no bound (today's provider) | `2026-08-04` |

A genuinely different, genuinely historical answer. The daily-grain providers can honestly answer
`valid_time`.

**P4 — `observed_time` as-of is structurally unanswerable. This is the sharpest finding.**
Same table: `day` spans 2025-12-27 → 2026-08-13, but `computed_at` spans only
2026-07-02 → 2026-08-13. Every row for a day before 2026-07-02 was written later. `computed_at` is
"when this row was last recomputed", not "when this was first known". Live probe of
`WHERE computed_at <= '2026-03-01'` returns **zero rows**.

The entity tables are worse: their only observation column is `last_synced`, and they are
`ReplacingMergeTree` — a re-sync **destroys** the prior version. There is no observation history
anywhere in ClickHouse to query.

This is drift item D15 (event-time cursor, backfill not re-observed) showing up as a hard limit.

**P5 — work-item status has no reconstructable history.**
`default.work_items` status vocabulary: `done, backlog, canceled, in_progress, unknown, todo`
(3300 rows). Nothing in the row lets us say which of `backlog`/`todo`/`in_progress` was true at a
past instant. But `completed_at` is populated for 2685/3299 rows, and
`git_pull_requests` carries `created_at`/`merged_at`/`closed_at` with vocabulary
`merged, closed, open, NULL`. So *some* facts are derivable and most are not — the split is
per-provider, not per-table.

**P6 — refusal sites.** 18 non-test files: `engine.go` (2 checks + helper), `ports.go` (sentinel),
`devhealthfacts/shared.go` (`checkCurrentTimeOnly` + reason) with 17 call sites across 14 provider
files, and `internal/api/context_fabric_routes.go:106` (the 400 mapping).

---

## 1. Time-axis request contract — what activates the axis, what bounds it

**No wire-contract change to the request.** `ContextFabricTimeContext` already carries
`{axis, as_of, start, end}`, and `validate_context_fabric_request.go:98` already enforces the
correct shape per axis:

- `current` — no timestamps permitted.
- `valid_time` / `observed_time` — `as_of` required and non-zero; `start`/`end` forbidden.
- `range` — ordered `start`/`end` both required; `as_of` forbidden.

The axis is therefore already fully activated by existing fields. CHAOS-3781 adds **service-level**
bounds the contract deliberately does not own (per `ErrUnsupportedTimeAxis`'s own doc comment: the
wire contract's accepted axes are unchanged; what is unsupported is this engine's ability to
answer).

New engine-level validation, replacing `requireCurrentTimeAxis`:

1. **Not in the future.** An `as_of`/`end` after `now` is refused (`ErrInvalidTimeBound`, 400). A
   future as-of is a prediction; §19.8.3 puts counterfactual and predictive answers out of scope.
   Tolerance: `+1m` for clock skew, then clamp to `now`.
2. **Not before the retention floor.** An `as_of` before the earliest data the sources can speak
   for is answered, not refused, but the answer is labeled `out_of_retention` and every source
   reports `no_data`. Refusing would be wrong — "we have nothing that far back" is a real answer.
3. **Bounded window.** `range` width is capped (proposal: 400 days, above the ~230-day live
   corpus). A wider range is refused with `ErrInvalidTimeBound`.
4. **`range` semantics = interval overlap**, not a snapshot. See §2.

`range` is in scope even though the ticket title names only valid/observed time: AC-3781-6 forbids
a partial removal, and leaving `range` refused is exactly the partial removal that reproduces the
H6 defect through the remaining door.

**Both existing check points survive**, with `requireCurrentTimeAxis` replaced by
`validateTimeContext` — the wire-request check (what was asked) and the post-`Interpret` check
(what the question was understood to mean). The second is what stops an interpreter from turning
`axis=current` into a historical question that then runs unlabeled. Its doc comment's reasoning is
unchanged; only the verdict changes from "refuse" to "bind the as-of and label it".

---

## 2. Graph-read semantics

### 2.1 The precondition: projection must write validity windows

Given P1, filtering alone delivers nothing. **CHAOS-3781 must first populate `ValidFrom`/`ValidTo`
in `devhealthsource` from source interval columns.** Proposed mapping (all columns confirmed to
exist in §0 probes):

| projected subject | ValidFrom | ValidTo |
|---|---|---|
| work item | `created_at` | `completed_at` ?? `closed_at` (nil if open) |
| pull request | `created_at` | `merged_at` ?? `closed_at` (nil if open) |
| PR review | `submitted_at` | nil (a review is a point event; see §2.3) |
| CI run | `started_at` | `finished_at` (nil if running) |
| deployment | `started_at` ?? `deployed_at` | `finished_at` (nil if in flight) |
| incident | `started_at` | `resolved_at` ?? `deleted_at` (nil if open) |
| repo | `created_at` | nil |
| relationship edge | the later ValidFrom of its two endpoints | the earlier non-nil ValidTo of its two endpoints |

`?? ` = first non-null.

The edge rule is the conservative one: a relationship is valid only while **both** endpoints are.

**Operational consequence, flagged for the lead:** this changes what `devhealthsource` emits, so
`ClickHouseSourceVersion` must bump (v3 → v4). Per `ErrProjectionSourceVersionChanged`, every
already-projected organization's worker then refuses every tick until an operator runs
`acr-projector rebuild --org`. CHAOS-3785 just did v2 → v3, so this is the second forced rebuild in
two changes. Until the rebuild lands, historical graph reads have no windows to filter on and must
report reduced coverage — they must not silently return the unwindowed graph.

**Alternative if the lead wants to descope:** land the read-side filtering only, accept that
AC-3781-4 is vacuous today, and split the projection write into a follow-up. I do **not** recommend
this — it ships the H6 defect under a historical label, which §19.8.3 puts explicitly out of scope.

### 2.2 Admission predicate

Point-in-time (`valid_time` / `observed_time` with `as_of = T`), evaluated in Cypher on the `_ns`
properties only (AC-3781-7 — never on the RFC3339Nano string half):

```
(valid_from_ns IS NULL OR valid_from_ns <= T_ns)
AND (valid_to_ns IS NULL OR valid_to_ns > T_ns)
```

Interval (`range` with `[S, E]`) — **overlap**, not containment:

```
(valid_from_ns IS NULL OR valid_from_ns <= E_ns)
AND (valid_to_ns  IS NULL OR valid_to_ns  >  S_ns)
```

Half-open `[valid_from, valid_to)` throughout, so an element that ends exactly at `T` is not
returned at `T`. This makes adjacent intervals partition cleanly with no double-count.

### 2.3 What a missing window means — the ruling

| case | meaning | ruling |
|---|---|---|
| `valid_to_ns` absent | open-ended: still valid | **valid at every `T >= valid_from`.** Standard SCD convention, and it is what the write side means. |
| `valid_from_ns` absent | unbounded start | **valid at every `T <= valid_to`.** CHAOS-3785 made a nil an *assertion* by the canonical source, not an omission, so this is "no lower bound", not "unknown". |
| both absent | no validity bound at all | **admitted at every `T`, and recorded.** |

The "both absent" case is the honest hard part. Excluding it empties the graph for every
pre-rebuild organization and for every subject kind we do not map in §2.1. Admitting it silently
is the H6 defect. So: **admit, count, and label.** The count feeds a
`graph_validity_windows_missing` coverage observation and a `Limitations` entry naming how many
admitted elements carried no window. A reader can then see exactly how much of a historical answer
rests on unbounded elements.

**`observed_at_ns` is deliberately NOT used as a validity proxy.** It is the *projection*
observation time; a rebuild resets it to now, so after any rebuild every historical graph read
would return empty. Naming this so a later change does not reintroduce it.

### 2.4 Irreducible limitation

The graph holds only the current projection. A subject deleted at source is removed by full-snapshot
projection, so `{edges that existed at T}` is only ever approximated by
`{edges that exist now whose window covers T}`. This is unfixable without an append-only graph
history and is **out of scope**. It is stated as a `Limitations` entry on every historical answer,
not smoothed over.

### 2.5 AC-3781-3

"A subject that did not exist at the requested time returns a clear not-applicable state." Handled
in `ResolveSubjects`: a subject whose window excludes `T` resolves to `unresolved` with a temporal
reason, and its fact requirements are recorded `not_applicable` rather than being queried. It must
not fall through to a current-state answer.

---

## 3. Fact-provider semantics — the three tiers

`checkCurrentTimeOnly` is replaced by `resolveTimeBound(query)`, returning either an as-of bound to
apply or an honest degradation. Every provider is classified. **No provider silently answers with
current data on a non-current axis** — that is the whole point of the ticket.

### Tier A — as-of native (valid_time answerable; recommended core of this change)

Daily/periodic tables that already pick "latest row"; an as-of query only moves the cut.

| provider | table | as-of predicate |
|---|---|---|
| `metrics.go` | `repo_metrics_daily` | `day <= as_of` |
| `investment.go` | `investment_metrics_daily` | `day <= as_of` |
| `health.go` | `compounding_risk_daily` | `day <= as_of` |
| `readiness.go` | `estimate_coverage_metrics_daily` | `day <= as_of` |
| `deficiencies.go` | `recommendations_daily` | `window_end <= as_of` |
| `workload.go` | `capacity_forecasts` | `computed_at <= as_of` (forecast is a point observation) |
| `source_health.go` | `backfill_log` | `created_at <= as_of` (append-only MergeTree) |

The existing `row_number() … ORDER BY day DESC, computed_at DESC, cityHash64(…) DESC` tiebreaker
stays exactly as is — only the `WHERE` gains the bound. That preserves the determinism work those
files' comments document.

For `range`, Tier A returns the rows inside `[S, E]` at their natural grain rather than one winner.

Effective grain is **day**, not instant. An `as_of` of `2026-03-01T14:00Z` is answered by the
`2026-02-28` row. The answer must state the effective as-of (§4), never imply instant precision.

### Tier B — derivable from immutable interval columns (recommend IN, flagging the cost)

The fact is a pure function of timestamps the row already carries. Not a reconstruction of an
unrecorded fact (§19.8.3), because the interval column IS the record.

| provider | fact | derivation at `T` |
|---|---|---|
| `workitems.go` completion | completed | `completed_at IS NOT NULL AND completed_at <= T` |
| `pullrequests.go` PR state | open/merged/closed | `merged_at <= T` → merged; else `closed_at <= T` → closed; else `created_at <= T` → open; else not-applicable |
| `pullrequests.go` review | submitted | `submitted_at <= T` |
| `ci.go` run status | its status / running | `finished_at <= T` → final status; else `started_at <= T` → running |
| `deployments.go` | its status / in flight | same shape as CI |
| `incidents.go` | open/resolved | `started_at <= T AND (resolved_at IS NULL OR resolved_at > T)` → open; else resolved |

Every Tier B derivation also gates existence: an entity whose `created_at`/`started_at` is after `T`
returns not-applicable (AC-3781-3), never a current-state row.

Derived values stay inside each provider's existing vocabulary — probes confirm PR states are
`merged/closed/open` and work-item completion is a boolean.

**If the lead wants to cut scope, Tier B is the cut.** Dropping it degrades honestly (those
providers report `not_applicable` on non-current axes) and still satisfies AC-3781-1/5. It costs
most of the user-facing value, since "was it merged / was it done / was the incident open" is what
people actually ask historically.

### Tier C — no history; must degrade (no honest as-of exists)

| provider | why |
|---|---|
| `workitems.go` status | vocabulary `backlog/todo/in_progress/…` is a mutable attribute with no history (P5) |
| `workitems.go` title, `identity.go` | titles/labels/identity are mutable, `ReplacingMergeTree` keeps only current |
| `dependencies.go` | `work_item_dependencies` carries only `last_synced` — no interval at all |

These return `State: not_applicable` with a fixed, non-parameterized reason naming the temporal
limitation (AC-3781-5). The rest of the answer survives, per §8.6. `not_applicable` is the right
state, not `unconfigured` — the source exists and is healthy; it simply cannot speak for that time.
(Today's code returns `unconfigured`, which is wrong on its own terms.)

**Reason strings stay fixed literals**, never interpolating the requested time — preserving the M6
finding's rule that `Reason` reaches the public contract verbatim.

### The `observed_time` ruling (needs an explicit call, see §7-B)

Per P4, **no ClickHouse source can honestly answer `observed_time`.** `computed_at` is a recompute
stamp, `last_synced` a re-sync stamp, and `ReplacingMergeTree` destroyed the prior versions.

AC-3781-1 requires an answer, not a 400. So: `observed_time` is accepted, runs, and returns an
answer in which **every** ClickHouse fact source reports `not_applicable` with an
observation-history reason, and the answer is labeled as having no observed-time fact coverage. The
graph side is equally weak (`observed_at_ns` is rebuild-reset, §2.3), so it is excluded too.

That answer is nearly empty. It is also the only honest one, and §19.8.3 rules an absent record
unknown, never zero. The alternative — keeping the 400 for `observed_time` alone — reads cleaner to
a caller but violates AC-3781-1 and leaves a partial refusal that AC-3781-6 forbids.

---

## 4. Answer labeling

AC-3781-2 requires the as-of or window in a **structured** field.

`Interpretation.TimeContext` already round-trips `{axis, as_of, start, end}` in the result, so the
*requested* time is structurally present with zero contract change. That is necessary but not
sufficient: it does not carry the **effective** as-of (Tier A quantizes to day grain) or the
temporal coverage verdict.

**Proposed additive optional field on `ContextFabricInvestigationResult`** — permitted in v1 per the
contract-first rule ("additive optional fields may stay in v1"):

```go
// ContextFabricTemporalLabel states the time this answer actually speaks
// for, which is not always the time that was requested.
type ContextFabricTemporalLabel struct {
    Axis           ContextFabricTemporalAxis `json:"axis"`
    RequestedAsOf  *time.Time `json:"requested_as_of,omitempty"`
    RequestedStart *time.Time `json:"requested_start,omitempty"`
    RequestedEnd   *time.Time `json:"requested_end,omitempty"`
    // EffectiveAsOf is the instant the answer speaks for after each
    // source's own grain is applied. Never later than RequestedAsOf.
    EffectiveAsOf  *time.Time `json:"effective_as_of,omitempty"`
    // Grain names the coarsest source grain that contributed ("day",
    // "instant"). A caller must not read instant precision from a day-grain
    // answer.
    Grain          string `json:"grain,omitempty"`
    // Complete is false when any source could not speak for this time.
    Complete       bool   `json:"complete"`
}
// on the result:
Temporal *ContextFabricTemporalLabel `json:"temporal,omitempty"`
```

`nil` on a `current`-axis answer, so no existing golden fixture changes shape.

Contract-first unit, all in one change: Go types → JSON Schema → canonical OpenAPI JSON + generated
YAML mirror → golden fixtures → parity tests. MCP definitions are touched **only if** the field is
exposed through MCP — and `internal/mcp/**` is lane-3746's. See §7-E.

Degradation continues to ride the existing `Coverage.Sources[].State/Reason`, `Coverage.Partial`,
`DegradedReasons`, and `Limitations`. No new mechanism.

---

## 5. Answer reuse interaction (CHAOS-3782) — a correctness bug if missed

**The problem.** `ReuseKey` is `{QuestionHash, ContractVersion, ProjectionVersion, ModelIdentity}`.
`QuestionHash` is `SHA-256(CanonicalizeQuestion(question))` — **question text only**. Today every
non-current axis is refused, so every stored result is implicitly `axis=current` and the key is
sound. The moment historical answers are stored, the identical question text at two different as-of
times produces the **same** key, and a June answer is served for a March question. That is a silent
wrong answer, worse than the refusal this ticket removes.

**Ruling: add a fifth dimension, `TimeAxisKey`, to `ReuseKey`.** Not folded into `QuestionHash` —
that hash's doc contract is "the canonicalized question text", and conflating two things into one
opaque digest destroys the debuggability the six-condition policy depends on.

Canonicalization of `TimeAxisKey` (deterministic, total, no wall clock):

| axis | value |
|---|---|
| `current` | the fixed literal `"current"` |
| `valid_time` / `observed_time` | `"<axis>:<as_of UTC UnixNano>"` |
| `range` | `"range:<start UnixNano>:<end UnixNano>"` |

**The trap, stated so it cannot be reintroduced:** `current` must map to a fixed literal, never to
`now`. Substituting a wall clock would make every current-axis key unique and silently reduce the
reuse rate to zero — CHAOS-3782 would still pass its own tests while delivering nothing.

Nanoseconds, not a formatted string: the same instant must produce the same key byte-for-byte, and
this matches the `_ns` convention AC-3781-7 requires everywhere else.

Storage: additive column `time_axis_key TEXT NOT NULL DEFAULT 'current'` on the reuse lookup table,
included in the lookup predicate and its unique index (migration `0013` -- see §9). The default backfills
every existing row correctly, because every stored row today *is* a current-axis answer.

**Conditions 1–7 are otherwise unchanged, deliberately.** A reviewer will expect the argument that a
historical answer is immutable and can therefore be cached longer or exempted from the watermark
check. That argument is **wrong** here: per D15 and P4, a backfill or correction rewrites rows for
past days, so what was true at `T` can change in our store after the fact. Watermark equality
(condition 3) and the rebuild epoch (condition 4) stay exactly as they are. No relaxation.

Condition 6 (authorization recheck) also stays as is. It re-resolves subjects through
`ResolveSubjects` with the candidate's own stored `Interpretation` — which carries the historical
`TimeContext` — so the recheck runs on the same axis the candidate was built on, with no change
needed.

**Collision with lane-3786.** They are adding a reuse-key dimension for fallback binding. Both
changes are additive and independent (a new key field + a new column + inclusion in the same unique
index). Whoever lands second rebases: one migration number and one struct literal. Alternative if
team-lead prefers: one of us lands the `ReuseKey` struct change with both fields and the other
consumes it. My preference is to sequence rather than share, since a shared struct change creates a
merge-order dependency in the acceptance tests too. **Team-lead's call.**

---

## 6. Explicitly out of scope

1. Reconstructing a fact never recorded at that time. An absent record is unknown, never zero
   (§19.8.3, §3.5). Tier C degrades; it does not guess.
2. Any observation history that does not exist. No new ClickHouse table, no history capture, no
   append-only graph log. `observed_time` degrades honestly (P4) rather than inventing a store.
3. Counterfactual or predictive answers. A future `as_of` is refused.
4. Deleted subjects. The graph holds only the current projection (§2.4) — stated as a limitation.
5. `answerprojection/`, `internal/mcp/`, and the API routes' response shaping — lane-3746 owns
   them. If the §4 temporal label needs to reach the MCP surface or the API projection, that is a
   **post-3746-merge integration step**, listed as such and not attempted here.
6. Vector/semantic retrieval, edge-vocabulary expansion, provider pruning (CHAOS-3778/3779/3783).
7. Relaxing any CHAOS-3782 reuse condition (§5).
8. Per-organization retention configuration. The retention floor is derived from data, not
   configured.

---

## 7. Decisions I need from team-lead before implementing

**A. Projection write-side (§2.1) — the big one.** Populate `ValidFrom`/`ValidTo` in
`devhealthsource`, bumping `ClickHouseSourceVersion` v3 → v4 and forcing a second org-wide
`acr-projector rebuild` in two changes? **My recommendation: yes.** Without it, "graph reads respect
validity windows" ships as a no-op and AC-3781-4 is vacuous.

**B. `observed_time` (§3).** Accept and answer with zero fact coverage (my recommendation,
satisfies AC-3781-1/5/6), or keep a 400 for that axis alone (violates AC-3781-1, leaves a partial
refusal)?

**C. Tier B derived-state providers (§3).** In (my recommendation — it is where the user-facing
value is) or out (cheaper, still honest, much less useful)?

**D. Contract addition (§4).** Add `ContextFabricTemporalLabel` (my recommendation — AC-3781-2 asks
for the *answer's* as-of, and only this carries the effective/quantized value), or rely on the
existing `Interpretation.TimeContext` round-trip and add nothing?

**E. The route refusal (§0 P6, AC-3781-6).** `internal/api/context_fabric_routes.go:106` maps
`ErrUnsupportedTimeAxis` to a 400. AC-3781-6 requires it removed in the **same change** as the
engine and providers, but lane-3746 owns the API routes. That is a direct conflict between the AC
and the ownership boundary. Options: (i) I make the one-hunk deletion in that file with lane-3746
notified; (ii) lane-3746 takes it and we land together; (iii) I land engine+providers and the route
deletion follows immediately. **(i) is the smallest and best preserves AC-3781-6; team-lead's
call.**

**F. Reuse-key sequencing with lane-3786 (§5).** Sequence (my preference) or share the struct
change?

---

## 8. Implementation milestones (after GO)

1. Contract: `ContextFabricTemporalLabel` + validation + JSON Schema + OpenAPI + goldens + parity
   tests (pending D).
2. `devhealthsource`: validity-window population, `ClickHouseSourceVersion` bump, table-level tests
   (pending A).
3. `falkorgraph`: `_ns` admission predicate in the read queries + unbounded-element counting;
   testcontainers live tests covering point-in-time, overlap, and both-absent (AC-3781-4/7).
4. `devhealthfacts`: `resolveTimeBound` replacing `checkCurrentTimeOnly`; Tier A bounds; Tier B
   derivations (pending C); Tier C `not_applicable`; per-provider tests (AC-3781-5).
5. `engine.go`: `validateTimeContext` replacing `requireCurrentTimeAxis` at both check points;
   as-of binding; temporal label composition; AC-3781-3 not-applicable subject state.
6. `answer_reuse.go` + `pginvestigation` + migration `0013`: `TimeAxisKey` (pending F).
7. Route refusal removal (pending E).
8. Acceptance tests AC-3781-1..7; docs (`docs/design/`, `docs/operations.md` rebuild note,
   `internal/contextfabric/AGENTS.md`, root `AGENTS.md`).

Evidence per milestone will be reported as it lands.

---

## 9. As implemented — decisions and deltas

All six decisions in §7 were approved as recommended:

| # | Decision | Ruling |
|---|---|---|
| A | Populate `ValidFrom`/`ValidTo`, bump v3 → v4 | **Yes.** Team-lead owns the rebuild sweep; CHAOS-3785's v3 had not deployed, so one org-wide rebuild lands at v4, not two. |
| B | `observed_time` accepted with zero fact coverage | **Yes.** AC-3781-1 wins; P4 makes the near-empty answer the only honest one. |
| C | Tier B derived-state providers | **In.** |
| D | Additive `ContextFabricTemporalLabel` | **Yes.** |
| E | Route refusal removed in this changeset | **Option (i).** lane-3746 merges last and rebases over it. |
| F | Reuse-key sequencing with lane-3786 | **Sequential.** 3786 lands first and now carries migration `0012` (its reuse-epoch cutover), so `TimeAxisKey` is migration `0013` and rebases over it. |

### Deltas from the proposal

These are places the implementation is deliberately different from §1-§6.

1. **`Requested`/`Effective` are full `ContextFabricTimeContext` values**, not
   six loose `*time.Time` fields (§4 proposed the latter). The axis-shape
   rules are then defined exactly once, on the type that already validates
   them, and the JSON Schema `$ref`s the existing `TimeContext` def twice
   instead of restating a 60-line `oneOf`.
2. **`Grain` is a closed enum** (`instant` / `day` / `none`), not a free
   string. Drift item D9 records free-string vocabularies in this contract
   as a governance gap; adding another would have widened it.
3. **`CoverageComplete`, not `Complete`.** Avoids reading as a restatement of
   the `complete` investigation *status*, which it is not.
4. **The contract refuses an unlabeled historical result.** Not in the
   proposal. A composition bug that dropped the label would otherwise ship
   exactly the unlabeled historical answer this issue removes, so the
   invariant is enforced where it cannot be bypassed.
5. **`existencePredicate` applies only the upper bound**, never the lower one,
   even for a range. An entity created *before* a requested window still
   existed during it; bounding its creation below would silently drop the
   long-lived subjects a historical question is usually about. Period rows
   (Tier A) keep both bounds, because a row outside the window genuinely
   describes a different period.
6. **`ErrInvalidTimeBound` is a new sentinel**, not a reuse of
   `ErrUnsupportedTimeAxis`. The retired one meant "historical questions are
   unsupported"; the new one means "these bounds are not answerable". Both
   map to 400, but conflating them would tell a caller their whole class of
   question is unsupported when only their bounds were wrong.
7. **Range grain narrows at both ends.** The effective start moves forward
   and the end backward, so the effective window always sits inside the
   requested one — the direction the contract validates.

### Migration numbering and the CHAOS-3786 rebase

`TimeAxisKey`'s migration is **0013**, not 0012 as §5 originally proposed:
CHAOS-3786 now carries 0012 (its one-time reuse-epoch cutover) and merges
first.

Until that merge reaches this branch, this worktree's applied set is
`{1..11, 13}` -- twelve migrations with a deliberate gap at 12. That
applies cleanly and leaves the branch green standalone, because
`migrations/postgres/runner.go` sorts by version and rejects only
duplicate versions; it does not require contiguity (checked before
renaming, not assumed).

The head-pin assertions are centralized in one `expectedMigrationVersions`
var (`migrations/postgres/runner_integration_test.go`) rather than
repeated across five assertions, so the rebase is a single-line edit to
the contiguous `{1..13}`. `cmd/acr-migrate/cli_test.go` asserts a COUNT
(12 now, 13 after) and carries the same note.

### Evidence

- **P3 confirmed in production shape**: `repo_metrics_daily` as-of
  2026-03-01 returns the `2026-02-28` row against the unbounded query's
  `2026-08-04`.
- **P4 confirmed**: `WHERE computed_at <= '2026-03-01'` returns zero rows;
  `day` spans 2025-12-27..2026-08-13 while `computed_at` spans only
  2026-07-02..2026-08-13.
- **Tier B derivation is not theoretical**: of 639 pull requests created
  before 2026-03-01, **15 read `merged` today but were `open` at that
  instant** — e.g. #208, created 2026-02-27, merged 2026-03-05. That is the
  false historical answer H6 named, now answered correctly.
- **Graph admission proved live** against real FalkorDB
  (`falkorgraph/temporal_live_test.go`): exclusion of closed windows, the
  before-anything-existed case, the half-open boundary, range overlap, and
  current-axis non-regression on the same seeded data.
- **Reuse collision proved live** against real Postgres
  (`pginvestigation/time_axis_reuse_integration_test.go`): a June answer is
  not served for a March question, and the June question still reuses it.

---

## 10. Codex round 1 — findings and resolutions

BLOCK, 8 findings (4 High, 4 Medium), all fixed with red→green guards.
Cleared on the first pass: the six §9 deltas, the observed-time honesty
fixes, the narrowing math, the Tier-A tiebreakers, the refusal sweep,
migration registration, and the unbounded-count surfacing.

| # | Sev | Defect | Resolution |
|---|---|---|---|
| F1 | High | Every answered source labeled day grain, but Tier B answers from EXACT timestamps — a PR merged at 14:00Z serialized as midnight | Grain is now per PROVIDER (`FactProviderResult.Grain`); the registry keeps the coarsest among providers that CONTRIBUTED, and only that reaches the label |
| F2 | High | Incident severity was current-row data emitted under a historical label | Severity is EXCLUDED from historical incident facts entirely, with the omission named in the provider's reason. Status stays — it derives from immutable columns |
| F3 | High | Referenced stubs inherited the window of the relationship or episode that mentioned them; projection ORDER decided which won | Stubs carry no validity window at all. Only the authoritative entity write states validity — CHAOS-3785's stub discipline |
| F4 | High | Deployment→incident edges were unbounded, so admitted at every requested time | Window derives from both endpoints (incident interval ∩ deployment validity), joined LEFT so an unresolvable endpoint leaves it absent for the admit-count-label path |
| F5 | Med | Dependency-edge window came from the SOURCE endpoint only, asserting validity while the target did not exist | Both endpoints intersect. The target join is LEFT because `target_work_item_id` may name a cross-system reference; an unresolved target contributes no bound |
| F6 | Med | Lookup keyed from the wire request, Save keyed from the interpretation — an interpreter axis flip saved under an unreachable key | BOTH sides key from the wire request, threaded to `Save` as an explicit parameter. Interpretation identity stays covered by condition 6 |
| F7 | Med | The skew tolerance accepted a future instant and let it reach predicates and the label | `resolveTimeContext` validates AND clamps; the label reports the clamped value |
| F8 | Med | A bounded historical query returning zero rows still reported `available` | Reports `no_data` with a fixed out-of-retention reason — "nothing happened then" and "we retain nothing that far back" are different answers |

### Live verification of the round-1 fixes

- **F5** is exercised on real data: all 1149 `work_item_dependencies` rows
  in the live corpus resolve their target, so the intersection runs on
  every one; the LEFT-JOIN fallback is the rare path and is unit-tested.
- **F4**'s query parses against live ClickHouse, but
  `work_graph_deployment_incident_edges` is EMPTY in this corpus (0 rows),
  so there is no live row to verify the derived window against. Both
  branches are unit-tested instead. Stated rather than implied.
- **F3** is proved against a real FalkorDB
  (`TestLiveReferencedStubsCarryNoValidityWindow`): a stub survives a query
  outside an unrelated relationship's window, while the EDGE is still
  correctly excluded — the fix must not weaken AC-3781-4.
- **F6** is proved against real Postgres: an interpreted-historical answer
  is found by the identical wire request that produced it.

## 11. Round-6 hardening — three durable rules

Rounds 2–5 are recorded in their commit messages; round 6 changed three
things that outlive this branch, so they belong here.

**Timestamp ingress is derived, not enumerated.** R5-3 bounded projection
timestamps to the representable epoch-nanosecond range by listing the
fields that carry one — and the list missed `Contents` and `Episodes`, the
fifth hand-enumeration miss on this branch. The list is now computed:
`validateRepresentableInstants` walks a projection value reflectively and
bounds every `time.Time` it actually contains, including fields nobody has
added yet. The general rule the branch keeps re-learning: when a check must
cover "all of X", derive X from the type or the declaration; a list written
by inspection is an absence audit, and absence audits are unfalsifiable.

**Nil is absent; zero is a present year-1 instant.** The engine's time-bound
guard used to skip every zero value on the assumption that zero meant "not
supplied". Only `nil` means that. A non-nil `*time.Time` pointing at the
zero value asserts year 1, and reaching the graph predicate it would admit
on a window that starts before all data — every row. The guard now refuses a
present zero and skips only nil.

**Every absence check states why it cannot fail silently.** The schema
closure passes by finding nothing, which cannot distinguish "nothing is
wrong" from "the check stopped working". Round 5 added a non-vacuity anchor
to one pass; round 6 found the sibling pass and the version-column check
still unguarded — fixing the instance and not the class, inside the round
that named the pattern. `closure_test.go` now carries a NON-VACUITY REGISTER
naming every check and its guard, or the structural reason it fails loud. A
check added without a register entry is the defect returning.

Two anchor details are load-bearing and easy to get wrong:

- The version-column anchor derives its expected count from `EngineFull` at
  runtime. A literal `13` would re-enter the hand-enumerated-constant drift
  class inside the closure's own test.
- It derives that count with a **different predicate** than the loop it
  guards (substring `replacing` vs the loop's `HasPrefix`). Deriving it the
  same way is circular: both counts fall to zero together and the anchor
  agrees that nothing needed checking.

## 12. Round-7 — two rules the earlier rounds had half-applied

**A check that cannot reach something must refuse it, not accept it.** The
reflective walk capped traversal at 8 levels and returned nil there, so a
timestamp nested deeper was ACCEPTED — the validator reported success having
examined less than the value it was handed. That is the fails-toward-fine
shape again: indistinguishable from a clean result. Reaching the cap is now
an error, which makes the cap self-reporting; a type that genuinely nests
deeper breaks a test rather than quietly losing its bound.

**Nil is absent, zero is malformed — everywhere, not just at the engine.**
R6-2 established this at the engine boundary and the walk still skipped
present zeros. The semantics were settled with evidence rather than
symmetry: all three production producers were swept, every nullable
timestamp goes through `validity.go`'s `(isNotNull, ifNull)` pair whose
`optionalTime` returns nil and never inspects the value, no bare `time.Time`
scan target is fed by a Nullable or LEFT-joined column, and episodes cannot
carry a zero `EndedAt` because the column is NOT NULL and episodes are
recorded post-hoc. Decisively, `validateTimeRange` ALREADY skips nil and
errors on `IsZero()` — so the walk was the piece disagreeing with the rule
around it, and no legitimate producer loses anything.

**A fix applied to one pass has to be applied to the class — including the
fix that said so.** R6-4 line-scoped the fragment pass's exemptions; the
rival pass was still file-wide a round later, while its own comment claimed
otherwise. Any exemption anywhere in a file silenced the whole file. Now
every sighting is attributed to its line and an exemption covers only what
it sits beside.

Line-scoping immediately surfaced what the file-wide skip had been hiding:
legitimate table-name literals outside every window — seeding INSERTs, a
producer registry's tail, a table→canonical-ID expectation map. Each now
carries its own marker and reason, which is the mechanism working rather
than a cost of it. One case was NOT marked but excluded on principle: a line
naming a table while calling `devhealthschema.DDL(...)` is USING the single
source, definitionally not a rival.

**The rival threshold is measured, not tuned.** Lowered 4 → 3 after sweeping
the repository: thresholds 4 and 3 trip on exactly the same four files,
because no file names precisely three declared tables, so the tighter bound
is free. 2 was measured and rejected — it pulls in files that legitimately
mention a pair of tables, and an exemption handed out to quiet a false
positive is permanent and covers whatever is written beside it later.
Re-measure if the declaration grows; the reasoning is "no file sits at 3",
not "3 feels right".

## 13. Evidence hygiene — the ledger

Five rounds of this branch produced a defect class that was never in the
product code: **defects in my own evidence**. They are recorded because the
fixes are cheap and the failure mode is invisible.

- **Round 6**: a red proof injected its bypass INSIDE an exemption window,
  so both halves proved nothing. Re-placed clear of every marker.
- **Round 7**: a mutation left an unused variable, so the "pre-fix" run was
  a BUILD FAILURE that a boolean read as "not green".
- **Round 8**: a mutation never applied — the patch used four tabs where
  the file had three — so "pre-fix" and "fixed" were the same code.
- **Round 9**: a fixture went into a production file, but the sweep under
  test only scans `*_test.go`. "Not caught" actually meant "never scanned".
- **Round 10**: the round-9 masking claim ("an unbalanced call continues
  onto the next line, so the remainder is masked with it") was FALSE. The
  state could not survive a function called once per line — and the red
  proof used a single-line nested call, so it never exercised the shape the
  claim described. Confirmed by re-running that exact shape this round: it
  passes, and always did.

Three standing rules came out of it:

1. **A mutation must assert it applied.** A silent no-op patch makes
   "pre-fix" and "fixed" identical and both look like success.
2. **Verdicts are READ from output, never inferred from a boolean.** Every
   ambiguous result above was a build failure or a mis-target that a
   boolean reported as a clean pass.
3. **A proof must exercise the shape the claim describes.** Round 10's
   lesson: a test that cannot fail for the claimed reason does not support
   the claim, however green it is.

One judgement worth keeping: at round 9 I declined to self-assess the
paren balancer by reading it, because judging one's own parser by
inspection is the same audit shape that had already failed repeatedly here.
Round 10 found a real defect in exactly that code — through execution.
