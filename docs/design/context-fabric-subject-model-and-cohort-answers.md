---
title: Context Fabric subject model and cohort answer design
---

# Context Fabric: subject model and cohort answer design (CHAOS-4398)

Design note for the 2026-08-28 gate re-open: team/cohort questions and the chat
surface were not gate-clean (chris 04:05). Covers the current subject model,
the two corrected root causes that blocked team cohort answers, and the
cohort ranking + drivers + Rows design that answers the charter question
"which teams are struggling and why" (CHAOS-3741, CHAOS-4398).

Decision record: [Linear — Subject model + cohort answers, decisions
2026-08-28](https://linear.app/fullchaos/document/subject-model-cohort-answers-decisions-2026-08-28-8fec005d1fb3).
Plan source: `.remember/context-fabric/drafts/cohort-answer-plan.md` and
`.remember/context-fabric/drafts/subject-model-requirement.md` (session
scratch, not authoritative — this page and the Linear document are).

## 1. Subject model

`ContextFabricSubjectKind` (`internal/contracts/v1/context_fabric_types.go:84`)
declares the full subject vocabulary. Of the kinds relevant to cohort/team
answers:

| Kind | Source (ClickHouse / graph) | Ownership rule | Status |
|---|---|---|---|
| `team` | `teams` table; graph node via `queryTeams` (`devhealthsource/teams_projects.go`) | **Ownership only** — a team owns a subject iff `team_repo_ownership`/`team_project_ownership` says so. Never person→membership (CHAOS-4321 hard rule) | first-class, cohort-capable |
| `project` | `projects` table; graph node via `teams_projects.go` | `team_project_ownership` (project→team) | first-class, cohort-capable — `interpretedCohortKind` already selects `SubjectProject` for project-shaped questions and `DiscoveredCohort` admits project nodes the same way it admits team nodes. The real limitation is narrower: `interpretedCohortKind` picks exactly **one** kind per question (a substring heuristic, `graphrank/discover.go:296`), so a mixed team+project cohort in one question is not supported — that is a distinct, pre-existing gap, not "project is single-subject only" |
| `repository` | `repositories`/`tables.go` | N/A (leaf; owned by team indirectly via ownership tables, never a direct graph edge — see diagram 3 caveat below) | first-class |
| `metric` | `ContextFabricSubjectMetric` constant declared (`model.go:92`) | — | **declared, not wired** — no query producer reads it yet |
| person | — | — | **not a subject kind in acr.** No `SubjectPerson` exists. This is by design, not an oversight: the platform contract bars person-level productivity/health/workload/staffing rankings (root `AGENTS.md`, "Visualization Guardrails"); a `person` subject able to carry a ranking would need a governance decision first, not a silent addition. |

**No direct repository↔project or repository↔team graph edge exists, by
design** — `project` here is a work-tracking project (Linear-shaped), not a
repository group. The only path from a project/team to a repository-scoped
activity kind (PR, review, CI run, deployment) is through `work_item`:
`project <-BELONGS_TO_PROJECT- work_item -BELONGS_TO_REPOSITORY-> repository`,
an **activity proxy**, never an ownership claim
(`docs/design/context-fabric-architecture-diagrams.md` §3,
`docs/design/context-fabric-fact-scope.md` §1). ClickHouse-side ownership
joins (`team_repo_ownership`, used by `HealthProvider`'s project rollup) are a
fact-producer join, not a graph edge, and do not appear in the graph diagram.

## 2. Corrections (2026-08-28 gate re-open)

Two independently-real defects co-occurred on the same live "which teams are
struggling" request and were first diagnosed backwards. Both are now fixed or
in flight; **do not re-litigate the original framing** below — it is kept only
so the corrected mechanism is legible against what a reader might find in
older commit messages or tickets.

### 2a. Team authorization was a wildcard over-exposure, not a deny (CHAOS-4390, fixed)

**Original (superseded) framing:** `graphrank.AuthorizedAttributes`
(`authorize.go:48`) was believed to deny any node whenever
`principal.RepositoryScopes` is non-empty unless the node's
`authorization_repositories` matches, and since `queryTeams` never set
`RepositorySlugs` on team nodes, every team was believed to be denied.

**Falsified by a live FalkorDB testcontainers round-trip.**
`falkorgraph/projection.go`'s `authorizationValue` converts an EMPTY
`RepositorySlugs` list into the literal wildcard string `"*"`, and the
wildcard branch in `scopeContainsAttr` authorizes that unconditionally for
*any* repository-scoped principal. The real defect was the opposite
direction: every team in an organization was visible to every
repository-scoped principal, unconditionally — a cross-scope information
leak, not a false deny.

**Fix (merged, PR #313):** authorize a team iff it owns ≥1 repository inside
the principal's scope (`team_repo_ownership`, CHAOS-4321 ownership-only). A
team with no ownership row gets an explicit deny sentinel
(`noTeamOwnershipSentinel`) — never a bare empty list, which would fall back
to the wildcard and reproduce the exact leak this fix closes.

### 2b. Cohort retrieval was fulltext-only (CHAOS-4395, in flight)

`DiscoverContext`'s only node source for the subjectless-cohort case was
`fulltextSearchNodes` — a lexical search over the raw question text. A
termless "which teams are struggling" names no team by label/alias/key, so
the search legitimately returned nothing and the cohort stayed empty — even
though `Interpretation.Shape` was correctly `discovered_cohort` and
authorization (once 2a is fixed) would allow every member.

**Interpretation was never the gap.** `InterpretationOutput.Shape` is a real
enum (`single_subject|explicit_cohort|discovered_cohort|open`,
`genkitruntime/runtime.go:1221`) and the prompt already instructs the model to
pick `discovered_cohort` for exactly this question shape
(`genkitruntime/prompts.go:89`). Top-level intent is genuinely LLM-driven
(`Runtime.InterpretQuestion`).

**Fix (PR #314, stacked on #313):** when `Shape` names a cohort kind, source
cohort members from `ExactNameCandidates`/the kind census (CHAOS-4348's
existing kind-exhaustive, term-free fetch, already used by the single-subject
path) in addition to fulltext — scoped to cohort requests only. Output shape
stays offers-only (no ranking/drivers); those are CHAOS-4398, below.

## 3. Cohort answer pipeline (CHAOS-4398)

```mermaid
flowchart TD
  Q["Question: 'which teams are struggling and why?'"] --> INTERPRET["Interpret<br/>Runtime.InterpretQuestion (LLM)<br/>Shape=discovered_cohort"]
  INTERPRET --> CENSUS["Cohort census<br/>DiscoverContext: fulltextSearchNodes today<br/>+ ExactNameCandidates -- IN FLIGHT (#314/CHAOS-4395,<br/>not yet merged as of this writing)"]
  CENSUS --> AUTHZ["Authorize<br/>graphrank.AuthorizedAttributes<br/>ownership-derived (#313/CHAOS-4390, MERGED)"]
  AUTHZ --> FACTS["Per-member fact production (NEW requirement injection, §3a)<br/>investigationScopeSubjects fans out subjects, but fact KINDS<br/>still come from Interpretation.FactRequirements + graphContext.FactRequirements<br/>(engine.go:1217-1222 mergeFactRequirements) -- cohort ranking must force-add<br/>health / workload / readiness / operational_deficiencies / investment (NEW join, see 4)<br/>the same way graphContext.FactRequirements already injects non-model requirements"]
  FACTS --> RANK["RankCohort (NEW, deterministic)<br/>runs after fact reads, before synthesis<br/>Score + RankingBasis + DataCompleteness"]
  RANK --> DRIVERS["Per-member drivers (NEW, budget-bounded)<br/>ContextFabricDriverJudgment<br/>emit up to top-3 for top 16 ranked members (canonical-result<br/>safety margin, §5a) -- actual rendered count is bounded<br/>further and separately by the request's own driver budget"]
  DRIVERS --> SYNTH["Synthesize<br/>Score is passed to the model as read-only grounding<br/>context (like any other canonical fact) so it CAN<br/>narrate the real number -- it still never computes<br/>or overrides one. Existing Rows guard keeps its<br/>strip-and-tolerate behavior unchanged (model-authored<br/>Rows stripped, answer still returned, cf_model_rows_stripped)"]
  SYNTH --> ROWS["Rows panel<br/>ContextFabricProjectedCohort.RankingTable (NEW field, §4a)"]
  ROWS --> PROJECT["Projection (NEW wiring, §4a)<br/>projectCohort/projectDriver must copy<br/>Score/RankingBasis/DataCompleteness/AffectedSubjects<br/>onto the projected types -- not automatic. New fields<br/>are optional/omitempty: a v1 result computed before<br/>this change simply omits them, it is not a false zero"]
  PROJECT --> ASKDEV["Ask Dev / Workbench<br/>shared answerprojection Rows renderer<br/>needs contract pin bump (new optional fields)"]

  classDef gap fill:#78350f,stroke:#f59e0b,color:#ffffff
  classDef fixed fill:#14532d,stroke:#22c55e,color:#ffffff
  classDef newnode fill:#1e3a5f,stroke:#3b82f6,color:#ffffff
  class CENSUS gap
  class AUTHZ fixed
  class RANK,DRIVERS,ROWS,PROJECT newnode
```

The model never computes or narrates a number it was not given — the same
discipline as canonical theme roll-up (root `AGENTS.md`) and
`attachCanonicalRows` (`model_runtime.go:575`, "only place `Rows` is set").
`RankCohort` sits in that same ordering slot: after fact reads, before
`SynthesizeAnswer`. `Score` is computed server-side and the model can never
override it, but it **is** passed into the synthesis prompt as an
already-computed, read-only grounding value — the same way any other
canonical fact reaches the model — so the model can narrate the actual number
without contradicting "never narrates a number it was not given"; an earlier
draft of this design said `Score` is "never model input" in the same breath
as asking the model to narrate it, which cannot both be true, and this is the
correction. The existing `StripModelAuthoredClaimedFactRows` path already
tolerates a model-authored `Rows` claim by stripping it and continuing
(`cf_model_rows_stripped`), not by rejecting the whole answer — this design
does not change that behavior. `CENSUS` above is marked `gap`, not `fixed`:
`DiscoverContext` still sources cohort members from `fulltextSearchNodes`
only on `main` as of this writing (§2b); PR #314/CHAOS-4395 adding
`ExactNameCandidates` is in flight, not merged, and this diagram must be
updated to `fixed` in the same PR that merges it, per the standing update
rule in `context-fabric-architecture-diagrams.md`.

### 3a. Fact requirement injection (P1, must be resolved in PR1)

`investigationScopeSubjects` (fact_planner.go) only fans the SUBJECT set out
to `request.Cohort.Members` — it does not decide which fact KINDS get read.
That decision is `mergeFactRequirements(statusComposedRequirements,
graphContext.FactRequirements)` (`engine.go:1217-1222`), where
`statusComposedRequirements` comes from the model's own
`Interpretation.FactRequirements`. If the interpreter's structured output
for "which teams are struggling" requests only, say, `health` and
`operational_deficiencies`, the other three providers never run and
`RankCohort` cannot compute the documented formula — a silent, non-obvious
failure mode, not a validation error. Resolution: when
`graphContext.Cohort != nil` (a cohort answer), the engine must inject the
five ranking-formula fact requirements (`health`, `workload`, `readiness`,
`operational_deficiencies`, `investment`) into `graphContext.FactRequirements`
before the merge — the same mechanism `graphContext.FactRequirements` already
uses to add non-model-derived requirements, not a change to how the model is
prompted. This is deterministic and unconditional for any cohort answer; the
formula's own per-signal "missing" handling (§5) still applies if a given
provider returns no rows for a given member even after being read.

## 4. Data model

```mermaid
erDiagram
    COHORT ||--o{ COHORT_MEMBER : contains
    COHORT {
        SubjectKind Kind
        string InclusionReasons
        bool Complete
        bool Truncated
    }
    COHORT_MEMBER {
        string SubjectID
        int Rank "UNCHANGED: pool order, never redefined (see 5b)"
        bool RankingComputed "NEW: disambiguates absent vs null Score (see 5b)"
        int AttentionRank "NEW: score-based order, present iff RankingComputed (see 5b)"
        NullableFloat64 Score "NEW: 0-100 or null, present iff RankingComputed (see 5b)"
        string_array RankingBasis "NEW: closed-vocab signal names"
        string DataCompleteness "NEW: complete|partial|degraded (see 5c)"
    }
    COHORT_MEMBER ||--o{ DRIVER_JUDGMENT : "top-3 for a budget-derived member count, never a fixed 16 (see 5a)"
    DRIVER_JUDGMENT {
        string Category "existing closed vocab, e.g. investment (NOT investment_mix -- see below)"
        string Title "closed-vocab label, e.g. reactive_share_high"
        string_array AffectedSubjects "this member only"
        string Derivation "deterministic"
        string_array ClaimedFactIDs
        string_array EvidenceRefIDs
    }
```

`RankingComputed`/`AttentionRank`/`Score`/`RankingBasis`/`DataCompleteness`
are **additive, optional** (`omitempty`) fields on the existing
`ContextFabricCohortMember`
(`context_fabric_types.go:995`) — additive-optional is the only form allowed
to stay in the v1 contract (root `AGENTS.md`: "Additive optional fields may
stay in v1... changed meaning... require a new major contract"). Absence of
these fields on a stored/serialized result means exactly one thing: this
result was computed before CHAOS-4398 landed (or by a producer that has not
adopted ranking yet) — it is **not** a signal of zero score or degraded data,
and validation/schema must not require them. This is a contract widening,
which per standing rule requires an ask-dev pin bump before the Workbench/
chat surface can render them. `ContextFabricDriverJudgment` is reused as-is;
no new contract type — but its `Category` is a **closed vocabulary**
(`ContextFabricDriverCategory`, `context_fabric_types.go:417`) with no
`investment_mix` member. Term 1's drivers must use the existing
`ContextFabricDriverCategoryInvestment` (`"investment"`) and carry the
finer-grained closed-vocab label (`reactive_share_high`,
`deliberate_share_low`, `mix_concentrated`, `mix_shift_toward_*`) in `Title`,
which is already a free-text string field — not by widening the `Category`
enum. "No person-to-person rankings" is unaffected — a team cohort ranks
teams, never individual people.

### 4a. Projection gap (P1, must be closed in PR1 or PR3)

Adding fields to `ContextFabricCohortMember`/`ContextFabricDriverJudgment`
(the canonical result types) does **not** make them reach API/MCP/Ask Dev.
`internal/contracts/v1/context_fabric_answer_projection.go`'s
`ContextFabricProjectedCohortMember` carries only `Subject`, `Rank`,
`InclusionReasons`, `EvidenceRefIDs` today — no `RankingComputed`/
`AttentionRank`/`Score`/`RankingBasis`/`DataCompleteness`.
`ContextFabricProjectedDriver` carries `Category`, `Title`, `Summary`, etc.
but **no `AffectedSubjects`**, so a projected driver cannot be tied back to
which cohort member it explains. `ContextFabricProjectedCohort` also has no
field for a ranking table at all today — `RankingTable` in the pipeline
diagram above is a **new** field this design adds to it (type
`[]ContextFabricClaimedFactRow`, the same reused row type
`ContextFabricProjectedFact.Rows` already uses, `context_fabric_answer_projection.go:234`),
not a rename of something that exists. Whatever function builds
`ContextFabricProjectedCohort` (the `projectCohort`-shaped code path) must be
extended to copy/compute all seven new/newly-needed pieces
(`RankingComputed`, `AttentionRank`, `Score`, `RankingBasis`,
`DataCompleteness`, `AffectedSubjects`, `RankingTable`), all as `omitempty`
for the same v1-compatibility reason as above, and per the
[contract-first rule](../contract-versioning.md) that requires, in the same
PR: the Go projected types, the JSON Schema, the OpenAPI document, MCP
embedded schema copies, golden fixtures under `contracts/examples/v1`, and
parity tests — not just the canonical-result-side contract change described
above. Skipping this makes the whole cohort-ranking feature invisible to
every consumer that reads the answer surface instead of the raw
investigation result.

## 5. Score formula

Weighted, renormalized over available signals only — a signal family whose
**source was unavailable/pruned/errored** is excluded from the denominator,
never scored as zero. Every signal is normalized to `[0,1]` **before** its
weight is applied (below); without this the weighted sum is not
deterministic and can land outside the declared `[0,100]` `Score` range,
which the first draft of this design left unspecified.

**"Missing" means the source did not answer, not that it answered zero.**
`OperationalDeficienciesProvider` (and every fact provider in this design)
returns `contextfabric.FactProviderResult{State: SourceAvailable}` with zero
`Facts` when a team genuinely has no currently-fired rules — that is a
**successful read of "no risk,"** not an unavailable source. Treating a
successful empty read as "missing" would exclude the 20-point deficiency
weight and renormalize the remaining, mostly-adverse signals upward,
penalizing a healthy team for having nothing wrong.

**Availability is per-member, not per-provider-call.** `FactProviderResult.State`
describes the whole batch read for a family, not any one team: if readiness
returns a row for team A but none for team B in the same call, the call is
still `SourceAvailable` overall, which would wrongly mark team B "available"
with no value to normalize. The rule, applied per `(member, signal family)`
pair:
- **available-with-value**: the family's batch `State == SourceAvailable`
  AND this specific member has a fact row in the result.
- **available-zero** (a defined exception, not a general rule): this member
  has no deficiency row AND the batch state is `SourceAvailable` — this is
  the one family where "no row" has an established meaning ("no currently
  fired rules"). No other family in this table gets a free zero for an
  absent row.
- **missing** for this member: the batch state is anything other than
  `SourceAvailable` (pruned/unavailable/error/`SourceTruncated` needs the
  same per-row check — a truncated batch can still carry a valid row for
  this specific member, so truncation truncates the batch, not automatically
  every member in it), OR the member has no row and the family is not
  deficiency.

| # | Signal | Source | Normalization to `[0,1]` | Weight | Direction |
|---|---|---|---|---|---|
| 1 | Investment-mix imbalance (driver family #1, chris direction) | new team-scoped `theme_distribution` (§6) | already `[0,1]` by construction — see sub-formula | 30 | see sub-formula |
| 2 | Health risk | `health.compounding_risk` | already `[0,1]` (the canonical score's own persisted range — `docs/reference` ops metric) | 25 | higher risk → higher score |
| 3 | Deficiency severity | `operational_deficiencies.severity` (string; a team can have several fired rules at once, or legitimately zero — see the available-zero exception above) | zero fired rules (`SourceAvailable`, no row for this member) → `0`. Otherwise map each fired rule's severity string to an ordinal (`low=0.25, medium=0.5, high=0.75, critical=1.0` — **the exact severity value set must be confirmed against `recommendations_daily` in PR1**, this mapping is a proposed default, not verified against live data), then take the **max** across the team's fired rules (worst case governs, not an average) | 20 | higher mapped value → higher score |
| 4 | Readiness gap | `1 - readiness.estimate_coverage_ratio` | already `[0,1]` since `estimate_coverage_ratio` is itself `[0,1]` | 15 | lower coverage → higher score |
| 5 | Workload pressure | `workload.forecast_p50_days` | min-max normalized **within the cohort being ranked** (`(x - min) / (max - min)` over the cohort's own values that day; `0.5` when every member ties, i.e. `max == min`) — a z-score is unbounded and can be negative, so it cannot feed a weighted `[0,100]` sum directly | 10 | longer forecast → higher score |

**Multi-scope aggregation (P1, must be resolved in PR1).** `readiness` and
`workload` providers can each emit multiple facts per team — readiness
partitions by provider and work scope, workload partitions by work scope —
so "the" `estimate_coverage_ratio`/`forecast_p50_days` for a member is not a
single value without a stated policy. Rule (consistent with row 3's own
worst-case-governs choice, so the formula has one aggregation philosophy, not
several): take the **worst** value across a member's own scope partitions —
`min(estimate_coverage_ratio)` across scopes for readiness gap (the least-
covered scope drives the gap), `max(forecast_p50_days)` across scopes for
workload pressure (the longest forecast drives the pressure). A member with
zero rows in a scope-partitioned family after §5's per-member missing check
still resolves to "missing" for that family; a member with rows in some
scopes and not others uses only the scopes that returned rows.

**Term 1 sub-formula** (own internal weights, produces one `[0,1]` value
before the table's weight-30 applies — already bounded because each
sub-signal below is a boolean threshold crossing, not a raw magnitude): each
sub-signal is also its own closed-vocabulary `RankingBasis`/driver label —
the pattern is borrowed from `investment_mix_explain.py`'s deterministic
`quality_drivers` list, **never** that file's LLM-authored narrative prose
(the Python-prototype path the standing rule excludes as a Context Fabric
reference).

| Sub-signal | Definition | Label (closed vocab) | Sub-weight |
|---|---|---|---|
| Reactive share | `operational` theme share + `quality.bugfix` subcategory share > 0.40 | `reactive_share_high` | 0.35 |
| Deliberate share | `feature_delivery` theme share < 0.20 | `deliberate_share_low` | 0.30 |
| Concentration | `max(theme_share)` across the 5 canonical themes > 0.55 | `mix_concentrated` | 0.15 |
| Mix shift | see below — signed, not just magnitude | `mix_shift_toward_operational` / `mix_shift_toward_feature` / `mix_shift_other` | 0.20 |

**Mix shift needs a signed delta, not a magnitude sum.** `Σ|share_t -
share_t-1|` over all 5 themes tells you shift *happened* (crosses the 0.15pt
threshold) but carries no sign or destination, so it cannot choose between
`mix_shift_toward_operational` and `mix_shift_toward_feature`. Compute the
**signed** per-theme delta (`share_t - share_t-1`, 5 values summing to ~0)
and take the theme with the largest-magnitude positive delta as the
direction: label `mix_shift_toward_operational` if that theme is
`operational`, `mix_shift_toward_feature` if it is `feature_delivery`, and
`mix_shift_other` (a third closed-vocab value, added here) if the largest
mover is `maintenance`, `quality`, or `risk` — the two-label vocabulary in
the plan draft cannot represent a shift that moves mass into neither named
theme.

No new categories are invented in the taxonomy sense — every label maps onto
the fixed 5-theme/15-subcategory taxonomy (root `AGENTS.md`: "no
synonyms/overrides"), mapping stated explicitly here so it is auditable. Each
label above is carried in `ContextFabricDriverJudgment.Title` with
`Category=` the existing `ContextFabricDriverCategoryInvestment`
(`"investment"`) — **not** a new `investment_mix` category value, which is
not in the closed `ContextFabricDriverCategory` vocabulary
(`context_fabric_types.go:417`) and would be rejected by
`ContextFabricDriverJudgment.Validate`.

### 5a. Driver budget (P1, must be resolved before PR2) — two independent budgets

There are **two separate, independent driver limits**, and the canonical-
result cap alone is not the binding constraint a caller actually experiences:

1. **Canonical-result validation cap**: `ContextFabricDriversMaxCount` is
   **50** total driver judgments **for the whole answer**
   (`context_fabric_model_bounds.go:236`) — `result.Drivers` is one array
   shared with every other driver-producing part of synthesis (status,
   blockers, health, etc. on the cohort's own members' single-subject-style
   facts), not a budget cohort ranking owns alone. `MaxCohortMembers`
   defaults to **250** (`answer_reuse.go:213`). A design that reserves a
   fixed 48 of the 50 slots for cohort drivers (`16 members × 3`) leaves at
   most 2 slots for every other driver the same answer produces — appending
   even a handful of ordinary drivers would then fail validation, which is
   the same class of bug this section exists to prevent, just moved one
   layer up. Corrected design rule, at emission time (`RankCohort`/driver
   generation, which must run with visibility into how many non-cohort
   driver judgments the same synthesis pass has already produced or reserved
   this run): let `available = ContextFabricDriversMaxCount -
   reserved_for_non_cohort_drivers` (a number PR1 must define an explicit,
   conservative reservation for — e.g. reserve a fixed floor such as 10 for
   non-cohort drivers before computing the cohort's share); emit top-3
   drivers only for the **top `floor(available/3)` members by `Score`**
   (ties broken by pool order), capped at 16 as an upper bound even if more
   budget is technically available (a Rows table with drivers for 40+ teams
   is not a more useful answer). Members beyond that cutoff get `Score` +
   `RankingBasis` + `DataCompleteness` (already enough to render a ranked
   Rows table) but zero `ContextFabricDriverJudgment` entries — the same
   "disclose the bound, do not silently drop" pattern `Cohort.Truncated`
   already uses for membership. A future PR needing a higher guaranteed
   member-with-drivers count must version the driver contract, not silently
   violate `ContextFabricDriversMaxCount` or starve non-cohort drivers.

2. **Projection-time render budget** (`answerprojection.Budget.MaxDrivers`,
   `project.go:34`) is a **much smaller, separate, request-scoped** limit
   applied AFTER the canonical result exists: MCP requests default it to
   **5** (`internal/mcp/investigate_question.go:20`), the shared projection
   package defaults to **10** (`project.go:46`), and a caller can raise it up
   to `ContextFabricProjectedDriversMaxCount`. This budget is **shared across
   every driver family in the whole answer**, not reserved for cohort
   drivers, and `projectDrivers` truncates by sorting on `Standing` (then
   `DriverID` as a tiebreak) — **not** by cohort rank
   (`answerprojection/project.go:267-274`). So on a typical MCP answer, only
   ~5 of the canonical cohort drivers this design emits (bounded per point 1
   above) will actually render, and which 5 survive depends entirely on
   `Standing`, not
   on which team scored highest. To make the most-in-need team's drivers the
   ones that actually survive a small budget, this design's driver emission
   should set `Standing=ContextFabricDriverPrincipal` for the single
   highest-`Score` member's top driver and `ContextFabricDriverContributing`
   for the rest, and derive each `DriverID` so it sorts by member rank as the
   tiebreak (e.g. `cohort-<org>-<rank:02d>-<n>`) — this is a recommendation
   for PR2 to finalize, not a fully specified algorithm here, but the
   two-budget distinction itself is not optional: a design that only cites
   the 50-cap and ignores the request-level 5/10 default will ship a feature
   where teams "struggling most" routinely do not appear in what the caller
   actually sees.

### 5b. Zero-signal team (P1, must be resolved in PR1)

A team with rows in **none** of the 5 signal families (§5's "explicitly
supported" degraded case) has an empty weight denominator — `Score` cannot be
computed as a number, and assigning `0` would make the least-observed team
render as the healthiest, which contradicts this design's own "never scored
as zero" intent from the first draft.

**A nil `*float64` with `omitempty` is indistinguishable from an absent
field — that breaks the exact v1-compatibility story §4 relies on.** Go's
`encoding/json` omits a nil pointer under `omitempty` exactly the same way it
omits a field that was never set, so a today's zero-signal member (ranking
ran, found nothing) and a result computed before CHAOS-4398 (ranking never
ran) would serialize identically — the reader cannot tell "no ranking
attempted" from "ranking attempted, no signal." Resolution: add a companion
boolean, `RankingComputed bool` (`omitempty`, so a pre-CHAOS-4398 result
still omits it and reads as `false`), set `true` by any producer that ran
`RankCohort` for this member regardless of outcome. `Score *float64` then
carries three real states: `RankingComputed` absent/false → ranking never
ran (the v1-compatibility case); `RankingComputed=true, Score=nil` → ranking
ran, zero signal families, `DataCompleteness=degraded`; `RankingComputed=true,
Score=<value>` → a real, computed score. This is still additive-optional:
`RankingComputed` is a new field, not a redefinition of an existing one. The
member still appears in the Rows table — never dropped — with its score cell
rendered as "no data" rather than a fabricated number.

**Rank placement must not repurpose the existing `Rank` field's meaning.**
`ContextFabricCohortMember.Rank` is a currently-required v1 field already
assigned from graph/pool retrieval order and already projected as part of
that judgment (§1 pipeline, pre-CHAOS-4398 behavior) — redefining what it
means (pool order → attention-score order) for existing callers is a
**changed meaning**, which root `AGENTS.md` requires a new major contract
for, not an additive-optional field. Resolution: leave `Rank` exactly as-is
(pool order, unchanged) and add a new optional field,
`AttentionRank int` (`omitempty`, 1-indexed, present only when
`RankingComputed=true`), carrying the score-based order this design actually
needs. Consumers that want the pre-CHAOS-4398 pool order keep reading `Rank`
unaffected; consumers rendering the cohort-attention Rows table read
`AttentionRank`. A null-`Score` member's `AttentionRank` is placed
deterministically last: after every member with a real `Score`, ties among
null-`Score` members broken by `Rank` (pool order) as the tiebreak.

### 5c. Completeness thresholds (non-overlapping)

The first draft's `partial` (`≥1 missing`) and `degraded` (`≤2 available`)
ranges overlapped whenever exactly 1 or 2 families were missing. Corrected,
non-overlapping thresholds over the 5 top-level signal families (term 1
counts as one family), counted **per member** using §5's corrected
per-`(member, family)` availability rule — not the batch-level
`SourceAvailable` state alone:

| Families available | `DataCompleteness` |
|---|---|
| 5 | `complete` |
| 3–4 | `partial` |
| 1–2 | `degraded` |
| 0 | `degraded`, `Score=null` (§5b) |

## 6. Finding: the existing investment producer reads the deprecated source

`FactInvestment` (CHAOS-4363/#308) reads `investment_metrics_daily`, fed by
the **deprecated** `ops/src/dev_health_ops/config/investment_areas.yaml` rule
set (free-form legacy labels, e.g. `security`/`infrastructure` — not the
canonical 5-theme taxonomy). The canonical theme/subcategory distribution
comes from `work_unit_investments`, read through the query-time
`latest_work_unit_investments` CTE (not a persisted table — it dedups to one
row per `(org_id, work_unit_id)` via `argMax(..., computed_at)`), LEFT
JOINed to `work_item_team_attributions` via ops's shared
`build_unit_team_subquery` helper (`api/queries/investment.py`; see
`fetch_investment_team_edges` for the reference caller), which already
carries ownership precedence per CHAOS-2600 — **acr has no producer reading
this join today**, the same class of gap CHAOS-4347 named for repository
status and CHAOS-4365 named for cognitive load. The investment Rows proven
live on 2026-08-27 came from the deprecated source. CHAOS-4398's PR1 adds the
new team-scoped read before term 1 of the score formula can be trusted —
either a new typed query function in `api/queries/investment.py` or an
equivalent join built the same way; there is no existing exported query API
for this shape to call as-is. See the ops-side deprecation note:
`dev-health-ops` `docs/reference/data-models/investment.md`.

## 7. What this does not cover

- Cohort ranking/drivers implementation itself (CHAOS-4398 is Backlog as of
  this writing) — this page documents the design, not a shipped capability.
  Update this page's pipeline diagram's node classes (`fixed` vs. `gap` vs.
  `newnode`) as each of CHAOS-4398's 4 stacked PRs — and CHAOS-4395 — lands.
  Three review passes on this design surfaced and this page now resolves:
  the driver-count budget being a global, shared budget rather than a
  cohort-reserved one, on top of a separate smaller projection-time render
  budget (§5a); the score-narration/model-input contradiction (§3); fact
  requirements for ranking not auto-flowing from cohort subject fan-out and
  needing explicit injection (§3a); v1 optional-field compatibility for the
  new member fields, including that a nil pointer with `omitempty` cannot by
  itself distinguish "no ranking attempted" from "ranking attempted, no
  signal" (§4/§4a/§5b); that `Rank`'s existing pool-order meaning must not be
  repurposed, requiring a separate `AttentionRank` field (§5b); an undefined
  `RankingTable` field (§4a); per-member (not per-provider-batch) signal
  availability and an explicit multi-scope aggregation policy for
  readiness/workload (§5); non-overlapping completeness ranges counted per
  member (§5c); the `Category` vocabulary (§4/§5); signed mix-shift direction
  (§5); and the missing-vs-successfully-empty distinction for deficiency
  (§5). PR1/PR2/PR3 implement against these corrected sections, not any
  earlier framing.
- The severity→ordinal mapping in §5 row 3 is a proposed default that must be
  checked against `recommendations_daily`'s real severity value set in PR1,
  not assumed correct from this page alone.
- `metric` and `person` subject handling — out of scope; see §1.
- Chat-vs-Workbench structure-offer parity and the "cannot re-ask" chat
  defect — tracked separately (`.remember/context-fabric/drafts/subject-model-requirement.md`
  §2-3), not a subject-model or cohort-ranking concern.
