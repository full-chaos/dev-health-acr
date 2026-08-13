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

- A requirement naming its own `Subjects` is honored as given.
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
`FactCapabilityRegistry.ReadFacts`.** A re-implemented baseline measures its
own divergence from the production pipeline, not pruning: an earlier version
assembled a "naive fan-out" bundle by calling providers directly and stamping
what the registry stamps, and each registry semantic it failed to mirror --
per-fact state rewrites, the aggregate fact cap, ordering, serialization --
surfaced as a separate review finding, three rounds running. Sharing the
production path makes those semantics identical by construction rather than
by transcription.

The naive counterfactual is therefore **counted, never executed**: one
round-trip per registered requirement, every subject bound to each. It cannot
be executed, and that is not an oversight -- `buildFactQuery` refuses to ask a
provider about an unsupported subject kind and refuses an empty subject list,
so forcing a plan-everything mode through `ReadFacts` would reproduce the
pre-CHAOS-3783 whole-bundle failure (§1) rather than a naive baseline. A count
has no bundle, no stamping, and no serialization, so it has nothing to diverge
on. It is not production "before" -- §1 explains why no slow before exists.

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

## 7. Known adjacent work, not taken

`graphrank.AdmitEdges` knows which subject each admitted edge concerns, but
writes `FactRequirement{Kind: factKind}` with no `Subjects`, and
`genkitruntime.factRequirementOutput` has no subjects field at all. So every
requirement fans out over every investigation subject even when its origin
knew better. Narrowing at the producers is a real further win on the
subject-binding axis; it touches `graphrank` and `falkorgraph` and was left
out of CHAOS-3783.
