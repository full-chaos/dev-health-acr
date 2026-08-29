# Context Fabric canonical fact planning (CHAOS-3783)

Short reference note. Covers: what the fact-read planner decides, why its one
rule is a proof rather than a heuristic, how a skipped provider stays visible,
what was deliberately rejected, and what the measurement did and did not show.

## 1. What this replaced

The issue framed provider pruning as an efficiency change: "every
investigation fans out across all providers, a category-aware planner skips
irrelevant ones, so lower latency and smaller fact bundles."

The first half of that premise was wrong, and the correction matters more
than the optimization. `FactCapabilityRegistry.ReadFacts` never fanned out
across all providers -- it only ever iterated the requirements interpretation
and graph discovery actually named. What it did instead was fail:
`buildFactQuery` returned an error for the first subject whose kind a
capability did not support, and `ReadFacts` turned that error into a
whole-bundle failure. One inapplicable fact family did not cost latency. It
failed the entire investigation.

That was reachable with no model mistake at all:

- `falkorgraph.DiscoverContext` merges `FactHealth` and `FactWorkload` for
  **every** discovered cohort (`reader.go`).
- `graphrank.interpretedCohortKind` returns `SubjectProject` for a question
  whose text mentions "project" or "initiative" (`discover.go`).
- `FactHealth` supports `{repository, team}`; `FactWorkload` supports
  `{team}`. Neither supports `project`.

So "which projects are behind" resolved a project cohort, required workload,
and could not be answered at all. `TestReadFactsProjectCohortNowAnswers` pins
that path.

Pruning is what makes a wide requirement union survivable. Everything else is
second-order.

## 2. Where the decision lives

`planFactReads` (`internal/contextfabric/fact_planner.go`), called once by
`FactCapabilityRegistry.ReadFacts` before any provider is touched -- after
interpretation, before fan-out.

The planner is given `factPlanInput`, not the `CanonicalFactRequest`. That
matters: the request carries the full `InterpretedQuestion` -- shape,
`RequestedJudgment`, `SubjectTerms`, `ComparisonTerms`, `ClarificationReason`
-- all model-authored text. An earlier version took the whole request and
simply did not read those fields, which made "a model cannot prune by
phrasing" a property of the function body rather than of the interface: a
later edit could reach for `request.Question` with no signature change, and a
behavioral test cannot fail for an edit it was never written against.
`newFactPlanInput` is the single boundary that strips the request down, so
prose is not merely unread, it is not present. `TestFactPlanInputCarriesNoInterpretation`
pins the field set so adding one is a deliberate, reviewed act.

It lives in the registry rather than in `Engine` because the registry is the
only place that already holds everything the decision needs: the
interpretation, the resolved subjects, the cohort, the requirements, **and**
every `FactCapability`. `Engine` holds the `CanonicalFactReader` port, not
capabilities, so planning there would have meant a new port. The registry is
also where coverage is emitted, so recording a decision is a call at the same
place that makes it.

The planner is pure: no I/O, no clock, deterministic, and ordered by the
caller's requirement order so coverage output is stable. It receives
capabilities by value through `capabilityIndex()`, so it can read what a
provider *declares* and can never reach the provider to call it.

## 3. The rule

One rule: **subject-kind applicability.**

For requirement `K` with capability `C`, take the subject set the query would
use (requirement subjects, else request subjects, else cohort members) and
partition it by `C.SupportedSubjectKinds`.

| Case | Outcome |
| --- | --- |
| No subject has a supported kind | **Prune.** `C` is never called. |
| Some subjects have a supported kind | **Narrow.** `C` runs against those subjects only. |
| All subjects have a supported kind | Run unchanged. |

### Why this is a proof, not a guess

A capability that supports none of the resolved subject kinds could not have
contributed one admissible fact even if it were run. `mergeFactProviderResult`
already rejects any fact whose subject is outside the investigation set, and
`buildFactQuery` only ever asks about subjects from that set. Underneath, each
provider filters on its own ID column, which no subject of an unsupported kind
matches.

So the pruned read is not "probably useless", it is **provably empty**. That
is why no confidence threshold, score, or tunable appears anywhere in the
planner: there is nothing to be uncertain about. It is also why the
measurement harness can assert the FACTS come out identical (§6 states
precisely what that claim does and does not cover).

### Why a model cannot prune by phrasing

The mapping is `FactCapability.SupportedSubjectKinds`, declared by each
provider in its own code (`devhealthfacts.newCapability`). The interpretation
contributes only the fact **kind**, chosen from a closed enum the contract
already validates, and the subjects come from graph resolution.

No part of the planner reads question text, `requested_judgment`,
`subject_terms`, `comparison_terms`, investigation shape, or clarification
state. There is no path from prose to a pruning decision.
`TestPlanFactReadsIgnoresEveryModelPhrasingSignal` holds that line by planning
two interpretations that differ in every prose-bearing field and agree only on
the fact kind, and asserting the plans are identical.

### What it fails open on

Everything else.

- A requirement naming its own `Subjects` is honored as given -- including
  its errors. An explicit list is a caller *assertion*, so naming a subject
  outside the investigation scope is a scope violation, checked in
  `validateCanonicalFactRequest` BEFORE the planner runs so pruning can never
  swallow it. In-scope-but-unsupported still prunes; out-of-scope errors.
  Scope itself has ONE derivation, `investigationScopeSubjectSet`, shared by
  that check, `buildFactQuery`, and the planner: it is `request.Subjects` else
  the cohort's members, a fallback and not a union. The registry once keyed a
  union while the planner applied the fallback, so a cohort member could pass
  the scope check for a request that had scoped it out.
- A capability supporting even one resolved subject runs.
- An ambiguous or low-confidence interpretation prunes **nothing extra**,
  because none of that reaches the planner. Ambiguity widens the requirement
  union and every capability that fits still runs.
- An unregistered kind keeps its existing `SourceUnconfigured` path -- a
  different and more specific statement than "pruned" (nothing is configured
  to answer this at all, for any subject).
- An investigation with no subjects keeps its existing error, because "no
  subjects" and "no fitting capability" are different failures and collapsing
  them would hide the first.

## 4. How a skipped provider stays visible

Every decision is recorded in `Coverage`. A pruned capability appears as a
`SourceObservation` with source `canonical_fact:<kind>`, state `pruned`, and a
reason -- exactly like one that ran and failed. Absence is always explainable.

Three properties are load-bearing:

- **`pruned` rejects facts.** A provider that was never called cannot have
  produced any.
- **`pruned` does not degrade.** Every other non-available state means
  something the answer wanted is missing. A prune means the source had nothing
  to contribute to *this* question, so nothing is missing.
  `factStateDegrades(SourcePruned)` is false. Marking it partial would make
  `Coverage.Partial` true for every correctly-scoped investigation and destroy
  the signal precisely as pruning became routine.
- **`pruned` is not a provider-returnable state.** It stays out of
  `validFactSourceState`, because a provider claiming to have pruned itself
  has by definition already run.

The contract's own "a non-available source requires a reason" rule applies to
`pruned` too, which is what makes the empty-states rule hold for it
mechanically rather than by convention.

## A read that returned nothing is `no_data`, never `available` (CHAOS-4521)

`pruned` and `unexpanded:<outcome>` cover the capabilities that never ran.
The remaining case is the one that ran: a provider that queried ClickHouse
and matched **no rows**.

Until CHAOS-4521 that reported `available` with an empty reason whenever the
question sat on the current time axis, on the reasoning that "zero rows has
always meant an ordinary empty read there". Run J (CHAOS-4450) showed the
cost. A project-status question committed a real Linear project subject; six
capabilities reported `available`; the `CanonicalFactBundle` was empty;
`SynthesisDraft.ValidateAgainst` therefore permitted no claims, and the
answer said *"No canonical facts were observed"* beside a Coverage block
asserting that six sources had contributed. Nothing in the finished result
distinguished "the sources answered and synthesis ignored them" from "the
sources returned nothing", so diagnosing it required a purpose-built rig.

`available` is a claim that the source **answered**. A source that was
reached and held nothing has answered *nothing*, which the closed
`SourceState` vocabulary already spells `no_data`, and which North Star
check 12 requires be kept distinct from a healthy read ("missing is not
healthy -- unknown/stale/sparse/not-applicable/zero are distinct").

- Both zero-row cases are `no_data`; they stay distinguishable by their
  **reason**, not their state. A historical zero may predate the retained
  corpus (`outOfRetentionReason`); a current-axis zero is a plain absence
  (`emptyReadReason`).
- `no_data` deliberately does **not** set `Coverage.Partial`
  (`factStateDegrades` excludes it): "we looked and there is nothing" is a
  complete answer, not a degraded one. What changed is that Coverage now
  *says* so, with a reason, instead of claiming a contribution.
- The rule is stated once, in `factTimeBound.retentionState`
  (`devhealthfacts/timebound.go`). The six Tier C providers that refuse every
  historical axis and so never build a bound reach it through
  `currentAxisReadState`, which **delegates** rather than restating -- those
  six are exactly where the original defect survived undetected, because they
  never called `retentionState` at all.

### The per-capability ledger

The same ticket adds `FactCapabilityRegistry.recordFactRead`: one structured
record per **planned** capability, whichever branch of the plan loop it took.

| field | meaning |
| --- | --- |
| `kind` | the `FactKind` planned |
| `outcome` | which branch minted the coverage entry -- `unconfigured` / `scope_gap` / `pruned` / `failed` / `completed` |
| `state` | the `SourceState` the coverage entry carries, read back off the bundle so the two cannot drift |
| `subjects` / `subject_kinds` | how many subjects, and their kinds only |
| `facts` | how many facts the provider returned, captured **before** the bundle-wide cap trims them |
| `truncated` | whether more existed than were kept |

`outcome` is a separate axis from `state` on purpose: the state says what the
answer claims, the outcome says which branch produced it, so a defect is
attributable to a branch without re-reading source. Every field is a count, a
boolean, or a value from a closed vocabulary this package owns -- no subject
label, no canonical ID, no question text, and no provider reason string
(reasons carry provider wording and are already published, clamped, on
Coverage).

The registry takes its logger from `FactRegistryOptions.Logger`; the hosted
runtime passes its own configured logger, never `slog.Default()`, which
ignores `ACR_LOG_LEVEL` and the configured handler.

A **narrowed capability that then fails** carries both records. The narrowing
note is attached on every path a narrowed read can take, not just the success
path: the read failing and the planner having cut the subject list are
independent facts, and recording only the first would silently lose the
second. `TestReadFactsNarrowedProviderThatFailsKeepsBothRecords` pins it.

### Per-fact source state

`pruned` being absent from `validFactSourceState` bounds what a provider may
return as its RESULT state, but each individual fact carries its own
`SourceState` too, and the evidence requirement in `mergeFactProviderResult`
is keyed on that exact field. A fact stamped with anything other than
`available`/`stale` therefore skipped `RequiresEvidence` entirely -- an
evidence-free fact could ride inside an ordinary `available` result just by
mislabelling itself. Merge now rejects a fact whose own state is outside the
provider-legal set (catching `pruned`) or is a facts-rejecting state
(`no_data`, `unavailable`, `unconfigured`, `unauthorized`, `conflicted`,
`not_applicable` -- all of which mean "there is no fact here", so a fact
wearing one is self-contradicting).

With those two guards in place exactly `available`, `stale`, and `truncated`
remain reachable, and all three are fact-bearing -- so the evidence
requirement is keyed on the **capability alone**, not on the fact's state.
An earlier form named `available`/`stale`, which silently exempted
`truncated` even though the registry mints that state itself when the bundle
cap trims a result. Truncation says "there are more facts than these", never
"these facts need less grounding".

### Coverage text bounds

`SourceObservation.Reason` and each `DegradedReasons` entry are separate
contract limits on separate strings, and the degraded entry is the longer one
(it carries a `"<kind>: "` prefix). Both are clamped **after** composition:
clamping the reason first and prefixing afterwards pushed the degraded entry
back over its bound by exactly the prefix length, failing validation for the
whole investigation -- the outcome the clamp exists to prevent. The live path
is a narrowed provider that fails, where the narrowing note and the failure
reason are concatenated before they reach coverage.

`clampCoverageText` truncates by **runes**, because the contract bounds are
rune counts (`stringLengthBetween` uses `utf8.RuneCountInString`) and a byte
slice could also cut a rune in half. It strips leading whitespace before
truncating, which is what makes the result provably non-empty: the retained
prefix then starts with a non-space rune, so trimming its tail cannot empty
it -- and an empty reason on a non-available source is itself a contract
violation.

### Reason codes, and why narrowing rides along

Reasons carry closed code prefixes -- `pruned:subject_kind_unsupported`,
`narrowed:subject_kind_unsupported` -- so consumers and tests match a prefix
instead of parsing a sentence. They name only the closed subject-**kind**
vocabulary, never a canonical ID or label: coverage reasons are stored,
replayed, and read by operators, so investigation content must not leak into
them.

`ContextFabricCoverage.Validate` requires coverage source names to be
**unique**, so the subjects a narrowed capability could not be asked about
cannot get an observation of their own. The narrowing note is prefixed onto
that capability's own reason instead, never replacing whatever the provider
said about its own read.

## 5. Rejected alternatives

### A model-selected judgment-category gate -- rejected

The obvious "category-aware" reading of the issue is a closed
`judgment_category` enum on `InterpretedQuestion`, mapped in code to a set of
fact families. It was rejected, and should not be re-proposed without new
evidence:

- `RequestedJudgment` is free text, so gating on it directly is gating on
  phrasing -- the exact failure the issue forbids.
- A new closed enum needs a contract change plus an interpretation prompt
  version bump, and hands a weak model a new way to lose the answer: pick the
  wrong category and the fact family that would have answered the question is
  silently gone.
- It is a heuristic. The subject-kind rule is a proof. Mixing the two under
  one "pruning" banner would make it impossible to reason about whether a
  given prune was safe.

Revisit only if measurement shows a fat tail of genuinely irrelevant
providers surviving the subject-kind rule.

### An environment kill switch -- rejected

There is no `ACR_CONTEXT_FABRIC_FACT_PRUNING_ENABLED`. The "off" position of
such a flag is the whole-bundle failure described in §1, and a flag whose off
position restores a crash is not a safety valve. Where every resolved subject
kind is already supported, the planner is a no-op, so there is nothing to
switch off in the safe case either.

### Reusing `not_applicable` instead of a new state -- rejected

`not_applicable` is a statement the provider made *after running* ("I do not
apply here"). `pruned` is a statement the planner made *instead of running it*
("you could not have applied here"). A wrong `not_applicable` is a provider
bug; a wrong `pruned` is a planner bug. Collapsing them would make pruning
decisions unauditable except by parsing free text, and would leave
CHAOS-3746's in-flight surface -- which copies coverage -- unable to count
skipped sources off the state alone.

## 6. What the measurement showed

`TestCHAOS3783PruningMeasurement`
(`internal/contextfabric/devhealthfacts/`) is opt-in and skips unless
`ACR_CHAOS3783_CLICKHOUSE_DSN` and `ACR_CHAOS3783_ORG_ID` are set.

**Every bundle it compares comes from the real
`FactCapabilityRegistry.ReadFacts`.** The harness never assembles a fact
bundle of its own (see "Rejected: a second bundle-building path" below).

The counterfactual it compares against is **counted, never executed**: one
round-trip per registered requirement, every subject bound to each. It cannot
be executed, and that is not an oversight -- `buildFactQuery` refuses to ask a
provider about an unsupported subject kind and refuses an empty subject list,
so forcing a plan-everything mode through `ReadFacts` would reproduce the
pre-CHAOS-3783 whole-bundle failure (§1) rather than a fan-out. A count has no
bundle, no stamping, and no serialization, so it has nothing to diverge on. It
is not production "before" -- §1 explains why no slow before exists.

### Rejected: a second bundle-building path

Do not reintroduce this. An earlier harness built its own comparison bundle by
calling providers directly and stamping the fields the registry stamps. That
made it a transcription of the registry pipeline, and every semantic it failed
to mirror -- per-fact state rewrites, the aggregate fact cap, ordering,
serialization -- surfaced as a separate review finding, three rounds running.
A re-implemented comparison measures its own divergence from production, not
pruning. Sharing the production path makes those semantics identical by
construction instead of by transcription.

What proves pruning did not change the answer is a **second real `ReadFacts`
call with the pruned kinds removed from the request**: asking for
`{A, B, C}` where `C` is pruned must produce exactly the facts of asking for
`{A, B}`. `TestPruningIsInvisibleToTruncationAndCaps` pins the same property
package-locally, with a provider pushed past the aggregate cap so the
truncation rewrite and the cap are both live.

On the live dev corpus (3 repositories, 3 work items, 1 team):

| Case | Round-trips | Subject bindings |
| --- | --- | --- |
| team question, targeted union | 5 → 5 (0%, correctly) | 5 → 5 |
| team question, broad union | 17 → 5 (71%) | 17 → 5 |
| repository question, broad union | 17 → 4 (76%) | 51 → 12 |
| work item question, broad union | 17 → 5 (71%) | 51 → 15 |
| mixed-kind open question | 17 → 11 (35%) | 119 → 32 (73%) |
| project cohort | 2 → 0 | previously unanswerable |

**The honest negative result: the FACTS are identical in every case.**
Pruning saves round-trips and subject bindings, not fact-bundle size, and
that is structural rather than a property of this corpus -- a pruned
capability would have returned nothing anyway (§3). The issue's "smaller fact
bundles, fewer hallucination surfaces" rationale is therefore **not**
supported by measurement. The round-trip and correctness wins are.

### What "identical" covers, precisely

The claim is **fact-identity**, not bundle-identity. The compared digest is
over `bundle.Facts` alone. `Coverage` deliberately DIFFERS between the two
runs -- the pruned observations are the entire point of the feature -- so
folding coverage into the digest would make the claim unfalsifiable.

The coverage difference is not excluded from checking, though; it is asserted
exactly. For every case that prunes, the harness requires that the planned
run's coverage minus the reduced run's coverage is **precisely the pruned
set**: a pruned kind must be absent from the reduced run, every surviving kind
must be present in both, and a surviving kind's state and reason must be
byte-equal across the two. A prune that also perturbed a surviving
capability's observation fails there. That turns an intentional difference
into a checked property rather than a carve-out.

`TestCHAOS3783FactsDigestIsOrderInsensitiveAndContentSensitive` guards the
shared serializer without needing a database, so it runs in ordinary CI rather
than only under the opt-in measurement. It pins both directions: permuting the
input must not change the digest, and altering one value to a **different
value of the same length** must change it. The second half matters because an
earlier version compared byte LENGTHS, under which any two same-size bundles
compare equal -- including in the permutation test written to catch exactly
that.

### The all-pruned case

The project-cohort case prunes every requirement, so there is no reduced run
to compare against: `validateCanonicalFactRequest` rejects a request with zero
requirements, and there is no "ask for nothing" call to make. Rather than
leaving this ticket's headline case as the only unverified one, the identity
statement is made directly for it -- an investigation whose every capability
was pruned must produce no facts at all, and must explain every one of them in
coverage with a non-empty reason.

### The harness fails loudly

Subject discovery treats any query, scan, or `rows.Err()` failure as a test
failure rather than returning what it managed to read. A case with no subjects
is skipped, so a silently-degrading discovery would shrink the subject set,
shrink the measured savings, and still report success. A measurement layer
that fails toward "fine" is worse than one that breaks, because nobody
re-reads a green benchmark. `TestCHAOS3783SubjectDiscoveryFailsLoudly` covers
all three failure paths, including the mid-iteration one that only `rows.Err()`
reveals.

Wall-clock is reported but caveated. Dev tables are small enough that provider
time is round-trip dominated, so round-trip **count** is the durable number
and the observed improvements are supporting evidence only.

## 6a. CHAOS-4347 — a capability's own SupportedSubjectKinds can grow

Nothing in §3's rule changes when a capability declares a WIDER
`SupportedSubjectKinds` set than it used to: the planner still partitions
purely on the declared set, still proves the pruned branch empty, still
never reads phrasing. CHAOS-4347 widened `MetricsProvider` from
`{repository}` to `{repository, team, project}` by giving it real
`team_metrics_daily`/`team_project_ownership` sources for the new kinds
(`metrics.go`'s package doc comment) — the planner requires no change to
narrow or run against the wider set correctly, because §3's rule was
already declaration-driven rather than hard-coded to any one provider's
prior shape. See `context-fabric-fact-scope.md` §11 for why this widening
is architecturally distinct from a `FactReadScopeResolver` expansion (a
capability gaining a REAL source for a kind, versus being granted a
disclosed READ permission onto a kind it still does not itself support).

## 7. Known adjacent work, not taken

`graphrank.AdmitEdges` knows which subject each admitted edge concerns, but
writes `FactRequirement{Kind: factKind}` with no `Subjects`, and
`genkitruntime.factRequirementOutput` has no subjects field at all. So every
requirement fans out over every investigation subject even when its origin
knew better. Narrowing at the producers is a real further win on the
subject-binding axis; it touches `graphrank` and `falkorgraph` and was left
out of CHAOS-3783.

## 8. Status-category composition (CHAOS-4347)

### The gap this closes

`ContextFabricInterpretedQuestion.FactRequirements` is authored directly by
the model, picking `Kind` values from the closed `FactKind` vocabulary --
there is no `judgment_category` enum for it to route through (§5 explains
why one was rejected). For a question about a subject's current state, the
vocabulary offers exactly one obviously-matching name: `status`. The model
picks it regardless of the resolved subject's kind, because nothing in its
input tells it `FactStatus` is `work_item`-only
(`devhealthfacts.StatusProvider`, `internal/contextfabric/devhealthfacts/
workitems.go` -- backed literally by `work_items.status`, a column that has
no repository or team analog at all).

`planFactReads`'s subject-kind rule (§2-3) then does exactly what it is
supposed to: `FactStatus`'s capability declares `SupportedSubjectKinds:
{work_item}`, a repository or team subject does not match, the requirement
prunes `subject_kind_unsupported`, and the investigation answers from graph
paths alone -- even when real canonical facts exist for that subject under a
DIFFERENT `FactKind` name. CHAOS-4344 case 23 is this exactly: a real,
human-curated oracle case (`question_class=subject_status`,
`kind=repository`, `authority=annotation`) that a bare `FactStatus`
requirement could never answer, though `devhealthfacts.NewProviders`
registers 19 producers and four of them (metrics, health, identity,
membership) already cover `repository`.

### The fix: compose, don't gate

`composeStatusCategoryRequirements`
(`internal/contextfabric/chaos4347_status_category_composition.go`), called
once in `Engine.Investigate` right before the fact requirements are merged
(same call site `mergeFactRequirements` already occupied) -- **after**
subject resolution, so the resolved subject KIND is known, which is the
load-bearing difference from the rejected §5 design: this is not a model
category pick being trusted, it is the SAME subject-kind rule §1-§3 already
key pruning off, applied one step earlier to WIDEN a requirement instead of
narrowing it. A bare `FactRequirement{Kind: FactStatus}` is expanded into
`statusCategoryFactKindComposition`'s closed set for whichever of the
requirement's own subjects (or, if unset, the investigation-wide subjects --
mirroring `factQuerySubjects`' own precedence exactly) have an entry:

| Resolved subject kind | Composed fact kinds |
| --- | --- |
| `repository` | `metrics`, `health`, `identity` |
| `team` | `health`, `workload`, `readiness` |
| `work_item` | unchanged (`status`) -- no entry in the table by design |

A mixed-kind cohort never loses either half: a subject kind absent from the
table (`work_item`, or a future kind this table has not caught up to) keeps
its own `FactStatus` requirement alongside whatever OTHER subject kind's
requirement also composed. Every other category stays 1:1, untouched --
this is scoped to the ONE category CHAOS-4344's corpus evidence proved
broken, not a general category→set rewrite.

### Telemetry (same change, per the standing order)

`RecordCategoryFactComposition` (`EngineTelemetry`, non-optional -- the
`RecordFactScopeExpansion`/`RecordSynthesisStatusOverride` discipline this
file's own §2-3 pruning telemetry already follows) fires once per (bare
`FactStatus` requirement, resolved subject kind) pair that actually
composed, naming the requirement kind, the subject kind, and the exact
composed set. It does not fire for a `work_item`-only requirement (nothing
composed) or for any other category (never reached).

### Corpus-level effect (structure only, no question text)

12 of the 65 corpus cases are `subject_status` × `repository`; this change
gives all 12 the composed fact set for the first time. A further 8 are
`project`-kind and remain unanswerable by this composition -- `project` has
no registered fact producer at all (§1's "project cohort" row already
documents `2 → 0` round-trips, "previously unanswerable," for the same
underlying reason) -- that gap is tracked separately (CHAOS-4347's own
project-producer phase), not closed here.


## A project subject reads the project's own data (CHAOS-4521b)

The zero-row honesty above told the truth about an empty project answer.
This says why it was empty, and fixes the half acr owns.

All six project fact rollups reached a project the same way: `projects` ->
`team_project_ownership` -> a team-scoped daily table. That was wrong in two
independent ways, both observed on live data (org `70d529e0`, 2026-08-29).

**It could not reach a real project.** The join keys on
`projects.project_key`, and every real Linear project carries `project_key`
NULL; the only non-empty Linear key belongs to the
`{org}:linear:<teamKey>` pseudo-project a team-key fallback writes. So the
join matched that pseudo-row and nothing else. The producer half is
CHAOS-4530 — and note the shape of that defect: a **team key** was being
used as a project key, so a project fact resolved to "team CHAOS".

**When it did resolve, it returned the wrong rows.** The readers joined the
daily table on `team_id` alone and never constrained `work_scope_id`, while
still selecting it. A "project" fact was therefore assembled from *every
work scope its owning team touched* — other projects' rows included. This
second defect is invisible to a fake-client row assertion, which is why the
pinning tests assert the **statement**.

### Which rollups can drop the team hop, and which cannot

`work_scope_id` **is** `work_items.project_id` — dev-health-ops' own oracle
asserts it (`github_work_item_derived_surfaces_oracle_test.go`: *"same
work_scope_id (project_id)"*). Three source tables carry it; three do not.

| rollup | source table | project dimension | team hop | after CHAOS-4521b |
| --- | --- | --- | --- | --- |
| flow | `work_item_metrics_daily` | **`work_scope_id`** | incidental | keyed on project identity |
| readiness | `estimate_coverage_metrics_daily` | **`work_scope_id`** | incidental | keyed on project identity |
| workload | `capacity_forecasts` | **`work_scope_id`** | incidental | keyed on project identity |
| health | `compounding_risk_daily` | none (`scope` ∈ repo/team) | **load-bearing** | keeps the hop |
| investment | `investment_metrics_daily` | none (`repo_id`, `team_id`) | **load-bearing** | keeps the hop |
| landscape | `ic_landscape_rolling_30d` | none (`repo_id`, `identity_id`, `team_id`) | **load-bearing** | keeps the hop |

Removing the hop from the bottom three would be a silent capability loss,
not a fix: their data cannot answer for a project any other way. They now
report `no_data` with a reason naming the routing
(`teamScopedProjectReason`) rather than the generic empty-read text, because
"this project has no health" and "this question could not be routed to the
project" are different answers.

### The two id spaces, and why both arms are load-bearing

`ProjectIdentityJoinSQL` resolves each subject once against `projects` to
`(id, project_key)`; `ProjectIdentityMatchSQL` matches a project-identity
column against either.

| provider | `projects.id` | the identity column holds | matched by |
| --- | --- | --- | --- |
| linear | `6241316a-…` (UUID) | `6241316a-…` | the **id** arm |
| gitlab | `{org}:gitlab:77145099` | `full.chaos/dev-health-ops` | the **project_key** arm |

The `project_key` arm keeps a `key_resolution_count = 1` guard so an
ambiguous key cannot attribute one project's rows to another; the id arm
needs none, `projects.id` being unique by construction.

**The ownership join moved onto the same identity match** (behind the single
constant `ProjectOwnershipJoinColumn`), because a `project_key` join could
not survive either direction of CHAOS-4530: it reaches no real Linear
project today, and once 4530 nulls `project_key` on the UUID-keyed rows it
would match nothing at all, taking health/investment/landscape to zero on
deploy. Both arms stay for the same reason in reverse — the id arm picks up
the UUID rows the moment they land, the key arm keeps today's GitLab rows.

`provider` is matched on the **ownership** edge and not on the work-scope
tables. Cross-provider equal ids are one project by design here (Linear
imports GitHub), so requiring provider equality on a work-scope read would
drop legitimate rows — and `capacity_forecasts` has no `provider` column at
all. The ownership edge is different: it decides which **teams** a project
inherits, and "equal ids are one project" is a statement about project
identity, not a licence to merge two providers' ownership catalogs.

`team_id` stays in every `row_number()` partition. An earlier revision
dropped it from the readiness and workload partitions, which makes
`row_number()` keep one team's row per work scope and silently drop every
other contributing team. The org this was measured against has a single
team, so the defect was invisible in the data; the statement assertions pin
it instead.

### One definition, not two

acr's `devhealthfacts/shared.go` used to carry its own copy of
`projectOwnershipJoinSQL`. It now delegates to `readers`. The copies had to
be moved off `project_key` together and nothing would have failed if only
one of them had been — the drift this repo's AGENTS.md warns about, in its
most literal form.
