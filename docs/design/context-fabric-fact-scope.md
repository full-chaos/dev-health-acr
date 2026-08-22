# Context Fabric fact-read scope resolution (CHAOS-4099)

Short reference note. Covers: the defect the pruner's own proof concealed,
the ratified architecture, the invariants binding on the implementation, what
stage 1 ships, and what stage 2 must prove before it activates.

Companion to `context-fabric-fact-planning.md` (CHAOS-3783), which this note
amends rather than replaces.

## 1. The defect

`planFactReads` prunes a capability strictly by resolved subject **kind**. No
canonical-fact capability declares `project` in `SupportedSubjectKinds`, so a
committed project subject prunes *every* capability -- `pull_requests`,
`reviews` and `metrics` included. The fact read returns zero facts and the
model honestly reports `no_match`.

Observed as ext65 corpus index 60 (`sha8 64f16228`). Before CHAOS-4098 it
surfaced as a 500; after it, as a `false_no_match`.

The load-bearing part is not the missing facts. It is the claim the system
made about them. `FactPruneReasonSubjectKindUnsupported` is documented as a
**proof** -- "the capability could not have produced a single admissible
fact" -- and `SourcePruned` is deliberately excluded from `factStateDegrades`
on exactly that ground: nothing is missing, so the answer is not partial.

That reasoning is sound for a subject kind nothing could ever reach from. It
is false for a project. The full typed chain already exists in projection
code (`devhealthsource/tables.go`, verified live at `0e662ceb`):

```
project <-BELONGS_TO_PROJECT- work_item -BELONGS_TO_REPOSITORY-> repository
pull_request        -BELONGS_TO_REPOSITORY->     repository
pull_request_review -BELONGS_TO_PULL_REQUEST->   pull_request
```

The facts are reachable. The planner simply had no step that reached them,
and reported the gap as a proof of absence.

**`team` shares the identical gap** -- confirmed during the design spike,
tracked as CHAOS-4101.

## 2. Premise correction

"Project" here is a *work-tracking* project (Linear-shaped), not a repository
group. There is **no** direct project↔repository edge. The chain above is an
**activity proxy** -- "repositories with at least one project-linked work
item" -- and never an ownership claim. That distinction is disclosed, not
assumed away.

`work_items.repo_id` carries the **zero UUID** for repo-less Linear issues.
It must never expand to a fake repository.

## 3. Architecture (ratified: option D)

```
DP9 resolution -> FactReadScopeResolver -> planFactReads -> provider reads
```

Two subject sets:

- **RootSubjects** — the committed resolution, the cohort, or a requirement's
  own override. Untouched.
- **ReadSubjects** — directly-supported roots ∪ authorized derived targets.

Rejected alternatives:

| Option | Verdict |
| --- | --- |
| A — widen each capability's `SupportedSubjectKinds` + inline joins | rejected: N duplicated joins, no central policy |
| B — unconditional global pre-plan expansion | rejected: temporal ambiguity, unbounded traversal |
| C — disclosure only | required fail-closed path, not a fix |
| **D — `FactReadScopeResolver` between resolution and planning** | **ratified** |

The resolver sits at that seam deliberately: later than resolution so it can
never feed back into a commit decision, earlier than planning so the planner
keeps its single auditable rule and providers keep their declared
`SupportedSubjectKinds` unchanged.

## 4. Invariants

1. DP9 untouched: `SubjectResolution.Committed` and commit bases are never
   mutated by expansion, and expansion cannot feed back into resolution.
2. Derived scope grants fact-**READ** permission only — never a second
   commit, never a new investigation subject.
3. Requirement overrides stay authoritative; no ID smuggling past
   authorization via an override.
4. `investigationScopeSubjectSet` gains derived targets **only** through the
   resolver's provenance list — no global widen.
5. `CanonicalFact.Subject` stays the exact repository/PR/review. Derivation
   is tracked in `FactScopeDerivation`, an engine-owned parallel struct, not
   as a field on `SubjectRef`.
6. Proxy nature always disclosed: `Coverage.Partial` plus a fixed limitation
   string on any activity-proxy-derived result.
7. Expansion gaps get their **own** outcome vocabulary. Never `SourcePruned`,
   which asserts "proven nothing missing".
8. Deterministic ordering and dedup; cap detection by limit+1;
   partial → `expanded_partial`, never silent truncation.
9. Authorization enforced at every graph hop; auth-drops are telemetry-only
   (no existence side-channel).
10. Version bump so pre-fix cached false-no-match results are not reused
    post-activation.

## 5. Vocabulary

`internal/contextfabric/fact_scope.go`.

**Policies** (`FactScopePolicy`): `project_work_item_repository_v1`,
`project_work_item_pull_request_v1`,
`project_work_item_pull_request_review_v1`, plus `none` for an
expansion-eligible pair with no policy defined (every `team` row today).

**Basis** (`FactScopeBasis`): `direct` | `activity_proxy`.

**Outcome** (`FactScopeExpansionOutcome`): `not_needed`, `policy_unavailable`,
`attempted_empty`, `target_kind_mismatch`, `expanded`, `expanded_partial`,
`failed`.

Outcome → `SourceState`, none of which is ever `SourcePruned`:

| Outcome | State | Degrades |
| --- | --- | --- |
| `policy_unavailable` | `unconfigured` | yes |
| `expanded_partial` | `truncated` | yes |
| `failed` | `unavailable` | yes |
| `target_kind_mismatch` | `unavailable` | yes |
| `attempted_empty` | `no_data` | no |
| *(unrecognised)* | `unavailable` | yes |

`target_kind_mismatch` degrades. The cause is a wiring error, but
`Coverage.Partial` describes the **answer**, and a mismatched traversal
yields no facts — the reader is exactly as short of evidence as if the policy
had been switched off. A loud telemetry event for the operator is not a
substitute for telling the reader.

Both mapping functions **fail closed** on an outcome they do not recognise. A
new enum value defaulting to "nothing is missing" would reintroduce this
ticket's defect silently, one addition at a time.

The contract's `SourceState` enum is **not** widened — that would require a
new major contract. The expansion vocabulary rides in the structured reason
prefix `unexpanded:<outcome>`, the same discipline CHAOS-3783's own
`pruned:` / `narrowed:` prefixes established.

## 6. Eligibility is a closed table, not a rule

`factScopePolicies` is keyed by (requirement kind, origin kind). A pair
**absent** from it is not expansion-eligible and keeps CHAOS-3783's honest
prune.

That bound is deliberate in both directions. The tempting generalisation —
"any capability a project/team subject cannot answer directly is a disclosed
gap" — over-claims (nothing has established a path from a project to
team-scoped workload/investment/readiness facts) and destroys the signal
CHAOS-3783 built (a non-degrading prune is what keeps `Coverage.Partial`
meaningful; flipping every subject-kind prune to degrading makes every
correctly-scoped investigation read as compromised).

So the table holds exactly the pairs the design spike traced end to end:
`{project, team} × {metrics, pull_requests, reviews}`.

## 7. Telemetry

`EngineTelemetry.RecordFactScopeExpansion` — declared on the interface
itself, not an optional side interface, so a sink that drops it fails to
compile. (CHAOS-4085 shipped telemetry behind an optional interface nothing
implemented, and every event vanished silently.)

One event per (requirement, origin kind, policy) triple that needed a
decision; **none** for a requirement answerable directly from its roots.

Fields: `RequirementKind`, `OriginKind`, `TargetKind`, `Policy`, `Basis`,
`Outcome`, `OriginCount`, `CandidateCount`, `AdmittedCount`,
`AuthorizationDroppedCount`, `TemporalDroppedCount`, `MissingNextHopCount`,
`TargetKindMismatchCount`, `Truncated`, `FailureClass`. Closed vocabularies
and counts only.

The count split is not decoration. Each one demands a different operator
response: authorization drops are normal on a shared graph; temporal drops on
a current-axis question mean a projection bug; `MissingNextHopCount` is how a
regression in the zero-UUID sentinel filter announces itself; a target-kind
mismatch is always a wiring error.

`AuthorizationDroppedCount` is telemetry-**only**. "There were targets you
cannot see" is an existence side-channel and never reaches the answer or
public provenance.

## 7a. Two disclosures, not one

They say opposite things and neither implies the other:

- `ContextFabricFactScopeUnexpandedLimitation` — "we could not reach some
  evidence."
- `ContextFabricFactScopeActivityProxyLimitation` — "we DID reach evidence,
  by a route weaker than it looks."

One investigation can be both (metrics expanded through the proxy while
reviews hit a disabled policy). A reader told only the first would take
everything *present* in the answer at face value, which is the misreading
invariant 6 exists to prevent.

The proxy disclosure ships in **stage 1** even though nothing is admitted
while every policy is disabled, so stage 2 flips a policy flag rather than
also introducing an invariant — and the mechanism is tested before it is
load-bearing rather than after.

## 7b. Who owns the cap

The resolver, not the expander. `FactScopeExpansionRequest.Limit` asks for up
to `Limit+1` targets and the resolver detects overflow from the extra row,
enforces the cap, and ORs its own finding into the expander's `Truncated`.

An expander that issued `LIMIT 200` rather than `LIMIT 201` returns exactly a
full page with `Truncated=false`, and a full page is indistinguishable from a
truncated one without the overflow row. Trusting the flag there produces
`expanded` with no disclosure over a scope that was actually cut — the silent
truncation invariant 8 forbids. The party that owns the invariant checks for
it.

## 7c. `CanonicalFactRequest.Scope` is output, never input

`ReadFacts` **always** runs the resolver and overwrites any incoming
`request.Scope`.

It briefly honored one, as a test injection point. `investigationScopeSubjectSet`
trusts every `Derivations` entry, so an in-process caller could hand over a
forged derivation for an unauthorized repository, name it in a requirement
override, and have `buildFactQuery`'s own scope check wave it through — ID
smuggling past authorization (invariant 3), and a route to new fact reads
while every policy is disabled. Tests reach the same outcomes through a
`FactScopeExpander`, which is the real port.

`ReadFacts` returns the in-progress bundle alongside a post-resolution error
so the scope telemetry survives a failed read; the engine emits from
`facts.Scope` **before** it checks `err`.

## 8. Stage 1 (this change)

Ships the resolver, the vocabulary, the disclosure and the telemetry, with
**every policy disabled**. No new fact reaches synthesis; what changes is
what the answer *says* about the facts it did not get.

- Project and team origins for metrics/PRs/reviews resolve to
  `policy_unavailable`, recorded with a degrading source state and the
  `unexpanded:` reason prefix.
- The answer carries `ContextFabricFactScopeUnexpandedLimitation` and
  `Coverage.Partial`. The activity-proxy disclosure is wired and tested,
  though no stage-1 path admits a derived subject to trigger it.
- Non-current axes refuse **before** traversing.
- Ineligible pairs keep CHAOS-3783's non-degrading prune.

Stage 1 also moved five CHAOS-3783 tests off `metrics × team` as their
"everything prunes" fixture — that pair is now precisely the CHAOS-4101
disclosed gap — onto a genuinely ineligible pair. The invariants they pin are
unchanged.

## 9. Stage 2 preconditions

Activation of the three project policies requires, with evidence in the PR
body:

- Graph-expansion vs. direct-ClickHouse oracle comparison per representative
  project: linked work items, nonzero/zero-UUID `repo_id`, unmatched repos,
  distinct repos/PRs/reviews.
- Canonical-ID alignment across all four producers, without parsing
  relationship IDs.
- Authorization filtering proven at every hop with mixed
  authorized/unauthorized repos.
- Product sign-off on the `activity_proxy` disclosure semantic.
- Current axis **only**. The `work_item -> project` edge carries `ObservedAt`
  but no `ValidFrom`/`ValidTo`, so an as-of expansion would silently answer
  with today's membership under a historical label. Deferred to its own
  projection-validity work.

Plus a version bump so pre-fix cached false-no-match results are not reused
post-activation, and a zero-UUID sentinel regression test whose edge removal
flips the outcome.

## 10. Team activation is a separate ticket

CHAOS-4101. The team-attribution edge is `rule_inferred`/`source_asserted`
rather than a typed source-asserted chain, so expanding through it would
launder an inference into fact scope. Activation needs a product ruling
naming the policy and its disclosure semantics. Until then the team rows
carry `FactScopePolicyNone` and disclose.
