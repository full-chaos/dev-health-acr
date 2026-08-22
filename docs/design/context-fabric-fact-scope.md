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
expansion-eligible pair with no policy defined — every `team` row, and every
disclosure-only project row.

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

`factScopeEligibility` declares every (requirement kind, origin kind) pair for
which a missing capability is a **reachable gap** rather than a proof of
irrelevance. A pair absent from it keeps CHAOS-3783's honest prune.

**Admission criterion (ruling, 2026-08-22): a verified typed chain, cited.** A
row exists only where the traversal chain exists in prod code *and* the row
cites it — edge names plus the producer that writes them, in the row's `Chain`
field. No chain citation, no row; `TestChaos4099_EveryEligibilityRowCitesItsChain`
enforces it. That is what keeps this a closed table rather than the rule
"anything a project cannot answer directly is a gap," which would over-claim
reachability nobody has shown.

**Why disclosure is wider than the three activation policies.** Invariant 7 is
controlling: `SourcePruned` asserts "proven nothing missing," and on a
reachable chain that assertion is **false**. CHAOS-3783's argument for a
non-degrading prune is about not degrading where pruning is genuinely sound;
it cannot justify keeping a false proof of absence. Where the two conflict,
honesty wins. Concretely, work-item status is *one* typed hop from a project —
a shorter chain than the three named policies use — so pruning it was this
ticket's own defect on a more obviously reachable path.

**Measurement effect, pre-adjudicated by the ruling:** `Coverage.Partial` fires
more often on project- and team-scoped questions than it did. That is
disclosure reflecting reality, not a regression.

The three chains, all verified at `0e662ceb`:

| Chain | Edges | Producer |
| --- | --- | --- |
| work item | `work_item -BELONGS_TO_PROJECT-> project`, `work_item -OWNED_BY_TEAM-> team` | `devhealthsource/teams_projects_edges.go` |
| repository | + `work_item -BELONGS_TO_REPOSITORY-> repository` | `devhealthsource/tables.go` |
| pull request | + `pull_request -BELONGS_TO_REPOSITORY-> repository` | `devhealthsource/tables.go` |
| review | + `pull_request_review -BELONGS_TO_PULL_REQUEST-> pull_request` | `devhealthsource/tables.go` |

Rows: `{project} × {metrics, pull_requests, reviews, health}` and
`{project, team} × {status, work, actual_completion, blockers,
required_children, identity, membership}`, plus
`{team} × {metrics, pull_requests, reviews}`.

**Deliberately absent:**

- Team-target families from a *project* origin (workload, investment,
  readiness, operational_deficiencies). The chain would run
  `project <- work_item -OWNED_BY_TEAM-> team`, through the same computed
  attribution CHAOS-4101 is holding back — approaching it from the other
  direction does not make it stronger.
- incidents, deployments, continuous_integration. Chains plausibly exist;
  none was traced end to end, and an uncited row is what the criterion
  forbids.
- source_health (organization-scoped). No chain.
- health from a *team* origin: `HealthProvider` supports team directly, so it
  never prunes and never reaches the table.

**Disclosure ≠ activation.** Widening disclosure is honesty. Widening
activation is a product commitment about what a fact family *means* for a
subject that does not own it, and each needs its own preconditions. Every
widened row carries policy `none`, never traverses, and emits
`policy_unavailable`. Activation scope stays the three ruled project policies;
`TestChaos4099_OnlyTheThreeRuledPoliciesAreEverActivatable` pins it.

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

## 6a. The outcome ladder is ordered so nothing degrading is masked

Two orderings in `expand()` were wrong on the first pass, and both failed in
the same direction — an outcome meaning "evidence is missing" was reported as
one meaning "nothing is missing," i.e. this ticket's own defect reintroduced
inside its fix:

- A truncated traversal that admitted **nothing** (every candidate on the
  first page authorization-dropped, more pages behind it) was reported
  `attempted_empty`: non-degrading, no disclosure, logged at INFO. It is the
  least-evidence case of all, so it must be the loudest. Truncation is now
  checked **before** emptiness.
- A traversal returning one valid target **and** one wrong-kind target was
  reported `expanded`: the bad candidate vanished with no gap and no
  degradation. A nonzero mismatch now forces `target_kind_mismatch` even when
  valid targets survive.

Expander and resolver mismatch counts are **disjoint by contract**: an
expander reports only targets it dropped itself and never returns them, so
summing cannot double-count.

A **third** instance was found one level up, outside `expand()`, and a
**fourth** downstream of the resolver entirely. The class is now understood as
structural rather than as three individual ordering slips: wherever a
degrading fact and a clean fact compete for one slot, the clean one wins by
default unless something makes it lose.

- **The disclosure slot.** A requirement has several origin kinds and one
  gap slot, and the slot went to the first origin in *sorted* order —
  `project` before `team`. A project chain that ran and genuinely found
  nothing (`attempted_empty`, non-degrading, `SourceNoData`) therefore
  discarded a team gap still owed a disclosure. `HasDisclosableGap` reads the
  same map, so the answer's sentence went quiet too. **The worst outcome now
  wins the slot, not the first**; among gaps that agree on whether they
  degrade, sorted order still decides, so the choice stays deterministic
  (invariant 8).
- **The planner's branches.** Only the "nothing supported at all" branch
  consulted the resolver's gap. A requirement that lost *some* targets but
  kept others took the ordinary read path and the provider answered
  `SourceAvailable` over a subject set the resolver already knew was
  incomplete. `target_kind_mismatch` is the live shape, precisely because the
  fix above made it retain its survivors. A degrading gap now attaches to a
  plan entry **that still has subjects**, and the read is marked truncated:
  the facts that did come back are kept, and the bundle says the scope was
  cut. It routes through `SourceTruncated` rather than the gap's own
  `SourceUnavailable` because `stateRejectsFacts` would otherwise reject the
  very facts being disclosed about — `SourceTruncated` is the state that
  already means "some of what you asked for, honestly labelled".

The second one matters beyond its own coverage line: the engine's
answer-level disclosure fired either way, so the defect was a **bundle that
contradicted the answer built from it**. Direct bundle consumers and
synthesis input saw complete coverage while the answer said evidence was
missing. A disclosure the fact bundle contradicts is worth less than no
disclosure at all.

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

The cap and the dedup then apply again **per requirement**, not only per
origin group. An expander is called once per (requirement, origin kind), so a
requirement with two expanding origin groups would otherwise admit twice the
declared cap — and a target reachable from both would be admitted twice.
`buildFactQuery` rejects a query whose subjects repeat and `ReadFacts` turns
that into a whole-bundle failure, so a successful expansion would destroy the
investigation it was meant to help. `AdmittedCount` is recomputed from what
survives, so telemetry reports the subjects the provider is actually asked
about.

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

**Overwriting the scope was the wrong remedy; it is now dropped before
validation.** Overwriting fixed the symptom at the resolver, but
`validateCanonicalFactRequest` runs *earlier* and computes its allowed set
with the same trusting `investigationScopeSubjectSet` — so a forged scope
still carried an unauthorized subject past the requirement-override scope
gate. Stopping the forgery at `buildFactQuery` holds only while the forged
subject is one a capability **directly supports**, because that is the gate it
walks into.

Forge an out-of-investigation **project** instead and the registry never
reaches that gate for it. The project becomes an expansion **origin** —
`Resolve` takes a requirement's roots from `requirement.Subjects` — and an
enabled policy derives a repository from it. The repository is then
legitimately in the resolved scope and *is* read. The subject reaching the
provider is authorized; the origin it hangs off never was, and no amount of
downstream scope checking catches that, because by the time the derived
subject exists the unauthorized origin has already done its work.

`request.Scope` is therefore set to `nil` **before** validation. At validation
time no derivation legitimately exists yet, so an override may name only an
investigation **root**; every legitimate derived subject enters after
`Resolve`, through the resolver, with provenance. The regression test asserts
the expander was **never called** — a rejection arriving after the traversal
already ran has already leaked the existence of the origin's repositories.

The general rule this settles: an authorization input must be neutralised at
the point the first decision is made from it, not at the point it is
conveniently overwritten later.

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
