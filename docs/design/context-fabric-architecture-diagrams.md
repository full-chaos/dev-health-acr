# Context Fabric architecture diagrams (CHAOS-4133)

Five mermaid diagrams covering the question-answering pipeline, the
candidate-pool mechanism that hid CHAOS-4348, the live subject/graph data
model, the fact data model, and the two-turn trial harness's measurement
fields. Built against live code (`codegraph_explore`, file:line cited
throughout) and the live kiac trial graph (`kubectl exec ... redis-cli
GRAPH.QUERY`, counts only, org `70d529e0-3c06-4597-8480-794fd02328b6`,
graph key `acr-cf-fa7030e2106de7411bfbf8ebce74c620-e2`).

Why this page exists (chris, 2026-08-26): a same-day incident (CHAOS-4348)
would have been caught immediately by a diagram that drew the candidate-pool
vs. offer-only-pool split. Before this page, `acr/docs` had four mermaid
blocks total and none of them drew that split.

**Legend (node classes, used across all five diagrams):**
- `defect` / `gap` (amber): a known, currently open gap or in-flight fix.
- `fixed` / `terminal` (green): a shipped, ratified behavior.
- `refuse` (red): a broken or refusing branch.
- dashed edges: skipped/discarded path, shown for context only.

## How to read these / update rule

Each diagram is paired with a one-paragraph legend and file:line anchors to
the code it depicts. Anchors are read-time citations, not links — the repo
has no stable line-anchor URL scheme, so re-run `codegraph explore` on the
named symbol if a line has moved.

**Standing rule (chris, 2026-08-26 17:55): any PR touching `graphrank`,
fact planning (`fact_planner.go`, `fact_registry.go`, `fact_scope.go`),
`devhealthfacts`, or `internal/contracts/v1` MUST update the affected
diagram in the same PR.** A diagram that silently drifts from the code it
claims to depict is worse than no diagram.

---

## 1 — Question flow end-to-end

```mermaid
flowchart TD
  ASKDEV["Ask Dev UI<br/>StructureNeedsPanel.tsx (ask-dev repo)<br/>renders kind/anchor/handle/window/candidate offers"] --> API["POST /api/v1/context-fabric/investigations<br/>internal/api/context_fabric_routes.go"]
  API --> ENGINE["Engine.Investigate<br/>engine.go:435"]
  ENGINE --> INTERP["Interpret (model call)<br/>RuntimeQuestionInterpreter.Interpret<br/>model_runtime.go:510"]
  INTERP --> STRUCT["structure needs: kind / anchor / handle / window<br/>composeEffectiveWindow (window.go)<br/>canonicalizeStructure (chaos3900)"]
  STRUCT --> WGATE{"class-default window gate<br/>WindowCanonicalizationGatedClassDefault<br/>CHAOS-4040/4234"}
  WGATE -->|"regime A: inferred_default"| GATED["gatedOfferMaterial<br/>chaos4234_offers_only.go"]
  GATED --> OFFRES["graph.ResolveSubjects(WithOffersOnlyResolution)<br/>same pool mechanism as regime B,<br/>commit-bearing output DISCARDED"]
  OFFRES --> WOFFER["windowConfirmationRequiredResult<br/>status=clarification_required, SubjectResolution EMPTY<br/>kind/handle/candidate offers minted BESIDE window offer"]
  WGATE -->|"regime B: explicit / confirmed window"| RESOLVE["graphrank.ResolveSubjects<br/>ResolveDeps: Search / SearchQuestion / SearchKind /<br/>AliasLookup + CHAOS-4038 coverage floor<br/>(see diagram 2)"]
  RESOLVE --> POOL["candidate pool split:<br/>pool (commit-eligible) vs offerOnlyPool (CHAOS-4271)<br/>-- see diagram 2 for why this matters"]
  POOL --> GATE1["ResolveFromMergedCandidatesWithGateAndBasis<br/>resolution.go:401 -- corroboration -> ranked_cut -> commit decision"]
  GATE1 --> DISCOVER["DiscoverContext<br/>cohort + structure offers, falkorgraph/reader.go"]
  DISCOVER --> SCOPE["FactReadScopeResolver<br/>fact_scope.go -- CHAOS-4099, activated<br/>project/team origin -> activity-proxy / attributed-team expansion"]
  SCOPE --> PLAN["planFactReads<br/>fact_planner.go:183 -- per-capability<br/>SupportedSubjectKinds partition: prune / narrow / run"]
  PLAN -->|"no supported subject, no scope gap"| PRUNE["pruned:subject_kind_unsupported<br/>proof of absence (CHAOS-3783)"]
  PLAN -->|"no supported subject, scope gap disclosed"| UNEXP["unexpanded:&lt;outcome&gt;<br/>CHAOS-4099 -- honest 'reachable but not read'<br/>NEVER SourcePruned"]
  PLAN -->|"some/all subjects supported"| READ["FactCapabilityRegistry.ReadFacts<br/>fan-out over devhealthfacts providers (ClickHouse)<br/>-- see diagram 4"]
  PRUNE --> BUNDLE
  UNEXP --> BUNDLE
  READ --> BUNDLE["CanonicalFactBundle + Coverage"]
  BUNDLE --> SYN["Synthesize (model call)<br/>RuntimeAnswerSynthesizer.Synthesize<br/>model_runtime.go:596"]
  SYN --> ROWS["attachCanonicalRows (CHAOS-4355)<br/>model-authored Rows rejected in ValidateAgainst;<br/>engine copies Rows verbatim from the canonical fact<br/>each claim cites -- model_runtime.go"]
  ROWS --> OVERRIDE["applySynthesisStatusOverride<br/>CHAOS-4098 -- decisive clarification_required -> no_match<br/>runs BEFORE the commit gate"]
  OVERRIDE --> GATE2{"CHAOS-4085 DP9 commit-affirmation gate<br/>applyCommitAffirmation<br/>chaos4085_commit_affirmation.go:447"}
  GATE2 -->|"exempt / affirmed"| COMMIT["Commit subject(s)"]
  GATE2 -->|"refused"| RETRACT["Retract-only<br/>(fail-closed, never fabricates a commit)"]
  COMMIT --> STATUS["Answer status:<br/>complete / degraded / no_match / clarification_required"]
  RETRACT --> STATUS
  WOFFER --> STATUS
  STATUS --> SAVE["Save (keyed on clamped request context)"]
  SAVE --> RENDERBACK["answerprojection.Project<br/>-> API / MCP -> Ask Dev renders offers + answer"]
  RENDERBACK --> ASKDEV

  classDef gap fill:#78350f,stroke:#f59e0b,color:#ffffff
  classDef fixed fill:#14532d,stroke:#22c55e,color:#ffffff
  classDef refuse fill:#7f1d1d,stroke:#ef4444,color:#ffffff
  class POOL gap
  class UNEXP,GATE2,ROWS fixed
  class RETRACT refuse
```

**Caption.** One investigation runs interpretation, a window gate that
forks into a discard-heavy "offers-only" resolution (regime A, CHAOS-4234)
or a decisive resolution (regime B), subject resolution against a split
candidate pool, fact-scope expansion, fact planning, fact reads, synthesis,
and a post-synthesis commit-affirmation gate, in that order. The amber node
is today's open gap: the **candidate pool split** (diagram 2) is where
CHAOS-4348 (project/team subjects silently excluded from the commit-eligible
pool) and CHAOS-4347 (fact routing/coverage widening for those same kinds,
in progress as of 2026-08-26 17:52 PDT) both live. **Two items in the
original brief for this diagram were verified stale and are corrected
here, not drawn as gaps:**
- **CHAOS-4171 (structure-offer renderer) is BUILT, not "not built."**
  `ask-dev/src/components/StructureNeedsPanel.tsx` (547 lines) renders
  kind/anchor/handle/window offers AND `CandidateOptionsSection` (CHAOS-4012)
  with model-generated `phrasing` per option; `.remember/today-2026-08-26.md:2`
  records "CHAOS-4171/3478/4244 chain closed." Treat any doc or comment
  still citing an unbuilt renderer as stale.
- CHAOS-4098 and CHAOS-4085 (the status-override and commit-affirmation
  gates) are shipped, ratified behavior — drawn `fixed`, not `gap`, matching
  their state in the CHAOS-4061 retro diagrams
  (`.remember/retro-4061-diagrams-draft.md`, diagram 1) reused here for
  visual convention.

**Updated 2026-08-27 (CHAOS-4355, `lane-4355-acr`): hop 5 (Rows render) is
BUILT, not "not built."** CHAOS-4347 (#300) added `Rows` on
`ContextFabricClaimedFact`/`ProjectedFact` additively but left them
unreachable — `SynthesisDraft.ValidateAgainst` rejected any claim that set
Rows (model-authored or otherwise), and nothing else set them, so a
producer's renderable table (e.g. `MetricsProvider`'s project rollup
`team_breakdown`) never survived synthesis. CHAOS-4355 closes that gap
WITHOUT letting the model author Rows: `ValidateAgainst` still rejects a
model-supplied `claim.Rows` unconditionally (even one that happens to match
canonical exactly — see
`TestSynthesisDraftValidateAgainstRejectsModelAuthoredRowsEvenWhenTheyMatchCanonical`),
and `attachCanonicalRows` (`model_runtime.go`), which runs INSIDE
`Synthesize` immediately after `ValidateAgainst` passes, is the only place a
`ClaimedFact.Rows` is ever set — copied verbatim from the SAME canonical
fact (`Kind`+`Subject`) the claim's scalar `Value` was already grounded
against, never derived or reworded. `answerprojection.Project` already
carried `Rows` through unchanged since #300 (`project.go`'s
`projectDrivers`), so this was the one missing hop. `RecordProjectedRowsCount`
(`EngineTelemetry`) reports the total rows attached and whether any claim's
table lost content -- capped at `ContextFabricClaimedFactMaxRows`, or
because its canonical fact carried more than one Rows-shaped field and only
one was kept (the fact-plan-adjacent "dropped by cap/pruning" signal) --
once per `Synthesize` call that reaches claim assembly, zero included. A
call rejected earlier (unavailable runtime, a model draft `ValidateAgainst`
itself rejects, a receipt-sink failure) reports nothing here; that failure
is the receipt sink's own outcome to record.

---

## 2 — Candidate pool internals: why an offer-only hit can never commit

```mermaid
flowchart TD
  TERM["Per interpreted term"] --> SEARCH["ResolveDeps.Search -> hybridSearchNodes<br/>falkorgraph/vector.go:531"]
  SEARCH --> LEX["fulltextSearchNodesForResolution (lexical)<br/>MatchLexical, vector.go:565"]
  SEARCH -->|"embedder configured,<br/>current axis, fence readable"| VEC["vectorSearchNodesWithOverFetch<br/>vector.go:643"]
  LEX --> MERGE1["append(lexical, vectorCandidates...)<br/>vector.go:650 -- NOT deduplicated here"]
  VEC --> MERGE1
  QSEARCH["ResolveDeps.SearchQuestion -> questionVectorSearchNodes<br/>vector.go:680 -- once per resolution"] --> MERGE2
  MERGE1 --> MERGE2["mergeSearchResults -> candidatesBySubject<br/>graphrank/resolve.go:3033<br/>= the commit-eligible 'pool'"]

  MERGE2 --> FLOOR{"applyKindCoverageFloor<br/>chaos4038_kind_coverage.go:264<br/>fires when confirmedKind == nil<br/>(resolve.go:1912)"}
  FLOOR -->|"missing kind: pull_request /<br/>work_item / ci_run / pull_request_review<br/>('census kinds')"| POOLIN["merge into pool<br/>(commit-eligible, unchanged)"]
  FLOOR -->|"missing kind: repository /<br/>project / team ('alias-lookup-<br/>scoped' kinds, isAliasLookupScopedKind)"| OFFERONLY["merge into offerOnlyPool<br/>chaos4038_kind_coverage.go:300-327 (CHAOS-4271)<br/>identity-claim args nilled to avoid<br/>cross-candidate identity collision"]
  POOLIN --> POOL[("pool = candidatesBySubject")]
  OFFERONLY --> OOP[("offerOnlyPool")]

  RESCUE["applyConfirmedKindRescue<br/>chaos4132_confirmed_kind_rescue.go:69<br/>fires when confirmed-kind pool is empty<br/>-- targeted SearchKind for that ONE kind"] -.->|"its truncated/degraded DOES<br/>feed the commit gate here"| POOL

  POOL --> GATE["ResolveFromMergedCandidatesWithGateAndBasis<br/>graphrank/resolution.go:401<br/>corroboration trace (resolution.go:444-462)<br/>-> ranked_cut trace -> MaxSubjectCandidates cut -> commit decision"]
  GATE --> COMMITTABLE["resolution.Candidates / .Committed<br/>ONLY ever built from 'pool'<br/>(resolve.go:1926-1933, explicit doc comment)"]

  OOP --> UNION["unionCandidatesForOffer"]
  POOL -.-> UNION
  UNION --> OFFERS["offer builders: kindOfferMaterial,<br/>candidateOfferMaterial, handleOfferMaterial<br/>-- an offer/suggestion only"]

  GATE -.->|"emits 'corroboration' trace event<br/>ONLY for candidates that reached 'pool'"| TRACE["ResolutionTraceEvent Stage=corroboration"]
  TRACE --> EXPPOOL["harness field expected_subject_in_pool<br/>(chaos3742_two_turn_confirmation_test.go:2309)<br/>reads TRUE only if the subject's find<br/>reached POOL, never offerOnlyPool"]

  classDef gap fill:#78350f,stroke:#f59e0b,color:#ffffff
  classDef fixed fill:#14532d,stroke:#22c55e,color:#ffffff
  class OFFERONLY,OOP,EXPPOOL gap
  class COMMITTABLE fixed
```

**Caption.** Every resolution builds candidates through two independently
merged mechanisms — ordinary unscoped lexical+vector search
(`hybridSearchNodes`) and a kind-scoped, lexical-only `SearchKind` pass used
by the CHAOS-4038 coverage floor and the CHAOS-4132 confirmed-kind rescue.
The floor's own split (CHAOS-4271, `chaos4038_kind_coverage.go:300-327`) is
the load-bearing fact this diagram exists to draw: a find for one of the
four "census kinds" (pull request, work item, CI run, review) merges into
the same `pool` the commit gate reads, but a find for repository, project,
or team merges into a **separate `offerOnlyPool`** that only ever reaches
offer builders — `ResolveFromMergedCandidatesWithGateAndBasis` builds its
ranked/committed candidates exclusively from `pool`, never
`offerOnlyPool`, by explicit construction (`resolve.go:1926-1933`). This is
also why the harness's `expected_subject_in_pool` field can read **false**
for a repository/project/team subject that was genuinely retrieved and
offered: the "corroboration" trace event that field scans for is only
emitted for candidates in `pool`. `wrong_commit=0` on such a run proves no
*wrong* subject was affirmed — it says nothing about whether the *right*
subject was ever a reachable commit candidate, because an offer-only hit
was never in a position to become one. This split is the mechanism behind
CHAOS-4348 (project/team subjects showing 0/20 "in pool" on 2026-08-26,
`.remember/now.md:73`) and the reason CHAOS-4347's fact-routing work
started from a false premise of unreachability rather than the real
gap — missing facts, not missing pool membership (see diagram 4).

**In-flight fix (not yet merged as of this writing):** `lane-4347-project`
(branch `chaos-4348-project-team-pool`) is building two new search arms
scoped to repository/project/team (`graphrank/chaos4348_reachability.go`,
`falkorgraph/chaos4348_exact_name.go`) that merge into the real `pool`
through the existing identity-collision guard, plus confirms two more
contributing mechanisms not drawn above: ordinary unscoped `Search`/
`SearchQuestion` lose the fulltext ranking race for these kinds against
~34,000+ activity nodes sharing a token (`resolve.go` per-term loop,
~line 1713), and `IsObservationAttributionRelation`
(`graphrank/subject.go:208`) is a closed 2-member set that has nothing to
do with `BELONGS_TO_PROJECT`/`OWNED_BY_TEAM`, so observation-traversal
does not cover the project/team edges either. Update this diagram in the
same PR that merges that fix, per the update rule above.

---

## 3 — Subject hierarchy and graph data model

Node/edge shape verified live against the kiac trial graph (org
`70d529e0-3c06-4597-8480-794fd02328b6`, `GRAPH.QUERY acr-cf-fa7030e2106de7411bfbf8ebce74c620-e2`,
2026-08-26). All nodes carry label `Subject` with a `subject_kind` property;
all edges carry relationship type `Relates` with a `relation_type` property
(never a distinct Cypher relationship type per edge kind).

```mermaid
flowchart TD
  ORG["organization<br/>(1 node, live)<br/>ZERO edges to/from it in the live graph"]
  TEAM["team<br/>(3 nodes)"]
  PROJ["project<br/>(20 nodes, canonical_id project.v2:...)"]
  REPO["repository<br/>(11 nodes)"]
  WI["work_item<br/>(3327 nodes)"]
  PR["pull_request<br/>(2926 nodes)"]
  PRR["pull_request_review<br/>(1121 nodes)"]
  CI["ci_pipeline_run<br/>(27902 nodes)"]
  DEP["deployment<br/>(676 nodes)"]
  INC["incident<br/>(SupportedSubjectKind exists in code;<br/>0 nodes in this org's live graph)"]

  WI -->|"OWNED_BY_TEAM (3327)<br/>work_item_team_attributions,<br/>is_primary=1"| TEAM
  WI -->|"BELONGS_TO_PROJECT (3098)<br/>project_membership_presence view<br/>teams_projects_edges.go"| PROJ
  PROJ -->|"OWNED_BY_TEAM (3)<br/>team_project_ownership, collapsed<br/>teams_projects.go Trap C"| TEAM
  WI -->|"BELONGS_TO_REPOSITORY (6, rare)"| REPO
  PR -->|"BELONGS_TO_REPOSITORY (2926)"| REPO
  CI -->|"BELONGS_TO_REPOSITORY (27902)"| REPO
  DEP -->|"BELONGS_TO_REPOSITORY (676)"| REPO
  PRR -->|"BELONGS_TO_PULL_REQUEST (1121)"| PR
  WI -->|"PART_OF (2106) / RELATES_TO (657) /<br/>BLOCKS (505) / DUPLICATES (11)"| WI

  REPO -.->|"NO edge exists, either direction<br/>(0 live, by design -- 'project' here is<br/>work-tracking, not a repo group)"| PROJ
  REPO -.->|"NO edge exists, either direction (0 live)"| TEAM
  ORG -.->|"NO edge exists at all (0 live)"| TEAM

  classDef gap fill:#78350f,stroke:#f59e0b,color:#ffffff
  classDef fixed fill:#14532d,stroke:#22c55e,color:#ffffff
  class REPO,PROJ,TEAM,ORG gap
```

**Caption.** The hierarchy is **not** organization → team → project →
repository → activity. Live data shows organization has zero edges to
anything, and there is **no direct repository↔project or repository↔team
edge at all, by design**
(`docs/design/context-fabric-team-project-subjects.md` §9: "No new fact
providers... project gets zero fact-provider entries... `project` here is a
work-tracking project, Linear-shaped, not a repository group. There is no
direct project↔repository edge"). The only path from a project or team to
a repository-scoped activity kind (PR, review, CI run, deployment) is
**through `work_item`** — `project <-BELONGS_TO_PROJECT- work_item
-BELONGS_TO_REPOSITORY-> repository -BELONGS_TO_REPOSITORY<-
{pull_request, ci_pipeline_run, deployment}`
(`docs/design/context-fabric-fact-scope.md` §1) — and that chain is
explicitly an **activity proxy**, never an ownership claim: "repositories
with at least one project-linked work item," disclosed as such via
`FactScopeBasisActivityProxy`. Producers: work_item edges from
`internal/contextfabric/devhealthsource/teams_projects_edges.go`
(`querySubjectProjectMemberships`, `queryWorkItemTeams`); repository/PR/CI/
deployment/review edges from `devhealthsource/tables.go`; project→team from
`teams_projects.go`. `incident` is a `SubjectKind` the code supports
end-to-end but this org's live graph currently has zero incident nodes —
absence of evidence, not absence of a code path.

**Stale doc comment found (report only, no Go edit per this lane's scope):**
`internal/contextfabric/devhealthsource/teams_projects.go:54` and
`internal/contextfabric/devhealthsource/clickhouse.go:29` both assert,
in a CHAOS-3898-era comment, "live-verified zero project.v2:-shaped nodes
exist anywhere in that org's graph today." The live kiac graph now holds 20
project nodes, every one with a `project.v2:<provider>:<id>` canonical id
(e.g. `project.v2:linear:2fb2c2b3-c52a-441a-b5f6-0453ce894e32`) — a rebuild
has happened since that comment was written (the graph is on lifecycle
epoch 2). The comment is a historical decision record (why the version was
bumped) and is not wrong about *why*, but its present-tense "today" claim is
stale and could mislead a reader checking current graph state.

---

## 4 — Fact data model

```mermaid
erDiagram
    FACT_CAPABILITY ||--|| FACT_PROVIDER : "Capability() declares"
    FACT_PROVIDER ||--o{ CLICKHOUSE_TABLE : reads
    FACT_PROVIDER {
        FactKind kind
        SubjectKind[] supported_subject_kinds
        bool requires_evidence
    }
    DRIVER_CATEGORY ||--o| FACT_KIND : "contextFabricDriverCategoryFactKind (1:1 map)"
    FACT_PLANNER ||--|| FACT_PROVIDER : "planFactReads: prune / narrow / run"
    FACT_SCOPE_RESOLVER ||--|| FACT_PLANNER : "expands ReadSubjects before planning (CHAOS-4099)"
```

**19 registered `FactProvider`s** (`internal/contextfabric/devhealthfacts`,
wired at `NewProviders`, `providers.go:14-35`; composed at
`internal/runtime/hosted/open.go:597`):

**Updated 2026-08-26/27 (CHAOS-4347, `lane-4347-ch`, PR #300):** the
`metrics`/`continuous_integration`/`deployments` rows below are widened by
REAL table joins/reads, never by proxying one kind's data as another's —
see `metrics.go`'s package doc comment
(`internal/contextfabric/devhealthfacts/metrics.go:1-83`) and
`context-fabric-fact-scope.md` §11 for why this is architecturally
distinct from a `FactReadScopeResolver` expansion (the resolver grants a
disclosed READ permission onto a kind a capability does NOT itself
support; this widening gives the capability a genuine second/third
source for the SAME kind).

| FactKind | ClickHouse table | Supported subject kinds |
| --- | --- | --- |
| identity | `repos`, `work_items` | repository, work_item |
| membership | `repos`, `work_items` ⋈ `repos` | repository, work_item |
| status | `work_items` | work_item |
| work | `work_items` | work_item |
| actual_completion | `work_items` | work_item |
| blockers | `work_item_dependencies` | work_item |
| required_children | `work_item_dependencies` | work_item |
| pull_requests | `git_pull_requests` | pull_request |
| reviews | `git_pull_request_reviews` | pull_request_review |
| continuous_integration | `ci_pipeline_runs` (per-run status); **+`cicd_metrics_daily`** (repository aggregate, CHAOS-4347) | ci_pipeline_run, **repository** |
| deployments | `deployments` (per-deployment status/environment); **+`deploy_metrics_daily`** (repository aggregate, CHAOS-4347) | deployment, **repository** |
| incidents | `operational_incidents` | incident |
| metrics | `repo_metrics_daily`; **+`team_metrics_daily`** (team, direct); **+`team_project_ownership` ⋈ `team_metrics_daily`** (project, summed-counts rollup, CHAOS-4347) | repository, **team, project** |
| health | `compounding_risk_daily` | repository, team |
| workload | `capacity_forecasts` | team |
| investment | `investment_metrics_daily` | team |
| readiness | `estimate_coverage_metrics_daily` | team |
| operational_deficiencies | `recommendations_daily` | team |
| source_health | `backfill_log` | organization |

**`FactMetrics`'s project rollup never averages a rate across
differently-sized teams.** Additive counts (commits, after-hours/weekend
commit counts) are SUMMED across the project's current owning teams
(`team_project_ownership`, `valid_to IS NULL` / as-of the requested
instant); each team's own rate (e.g. `after_hours_commit_ratio`) rides
unmodified in a new per-team `team_breakdown` field instead, disclosed via
a `rollup_basis` field on the fact. This is what
`ContextFabricClaimedFact.Rows` / `ContextFabricProjectedFact.Rows`
(new, additive, `context_fabric_types.go` / `context_fabric_answer_projection.go`,
CHAOS-4347) exist for: a renderable table on a fact/claim whose evidence
is genuinely a set of rows, not a lossy single scalar. Deliberately does
NOT touch `ContextFabricScalarValue` itself (also the projection-write
contract's property-value type, which documents "nested objects and
arrays remain disallowed" as a deliberate invariant for THAT surface).
**Updated 2026-08-27 (CHAOS-4355): now wired into synthesis, still never
into prompts.** `MetricsProvider`'s project rollup remains the only producer
of a `Rows`-bearing fact today, but a driver's cited claim now DOES carry
it: `attachCanonicalRows` (`internal/contextfabric/model_runtime.go`), the
one place a `ClaimedFact.Rows` is ever set, copies it verbatim from the
canonical fact each claim cites, immediately after
`SynthesisDraft.ValidateAgainst` passes -- ValidateAgainst itself still
rejects any claim where the MODEL set Rows directly, unconditionally, so
the model is never the source. `answerprojection.Project` carries it
through unchanged (`project.go`'s `projectDrivers`, unchanged since #300).
The prompt itself was NOT changed -- the model is never told Rows exist,
so no new prompt version was needed. The sidecar's own answer-rendering
closure test (`internal/sidecar/render_answer_untrusted_test.go`) still
carves out the render-layer half of this: a claim can now carry Rows, but
nothing in `dev-health-web`/Ask Dev renders them yet (see
`context-fabric-fact-scope.md` for the exact projected shape a renderer
would consume).

**Gated off: `evidence` (1 kind, not 8).** `doc.go:37-46` and
`providers_test.go:48-52` name exactly one deliberately unregistered
`FactKind` — no ClickHouse table maps honestly to it (`report_provenance`
exists but is empty in every environment; no subject-ID convention for
`artifact_id`/`plan_id`). `internal/contracts/v1/context_fabric_types.go:313-332`
declares 20 `ContextFabricFactKind` members total: the 19 above + `evidence`.

**Stale claim found:** `internal/contextfabric/AGENTS.md`'s WHERE-TO-LOOK
table (line 45) reads "ClickHouse-backed `FactProvider`s; 8 fact kinds gated
off (no canonical source)." Only 1 is gated off in code today. The "8" most
likely conflates with the 8 unread CH tables below — report to team-lead;
not corrected in this PR (docs-only, different file, out of this page's
scope, flagging per this lane's discrepancy-reporting mandate).

**Update (CHAOS-4347, PR #300): 3 of the original 8 CH tables now have a
reading `FactProvider`** — `team_metrics_daily`, `cicd_metrics_daily`,
`deploy_metrics_daily` (see the widened table above). Their production
column types were verified LIVE off the kiac trial ClickHouse
(`system.columns`, `kubectl exec` into the `trial-clickhouse` pod, ns
`acr-trial-data`) and added to `devhealthschema`'s shared production
declaration, not inferred from the ops migration files alone.

**5 CH tables still have no reading `FactProvider`** (unchanged, repo-wide
`rg`, zero hits in any `internal/contextfabric/**/*.go` read path):
`dora_metrics_daily`, `work_item_metrics_daily`, `issue_type_metrics_daily`,
`user_metrics_daily`, `ai_impact_metrics_daily`. **Correction to this
page's own earlier text:** `dora_metrics_daily` is keyed by
**`repo_id`**, not `team_id` (`repo_id UUID, day Date, metric_name String,
value Float64` — verified against both the ops migration
(`023b_dora_metrics.sql`) and the live kiac schema) — an earlier framing
of this table as team-scoped was wrong; it was out of scope for the
metrics team/project widening for exactly that reason, not merely
unstarted. (One unrelated hit remains: `cicd_metrics_daily` also appears
in a trial-harness table-name classification map,
`internal/runtime/hosted/frontier_trial_live_test.go:1459`, explicitly
commented as not a read or a schema declaration — that reference is
unrelated to the new FactProvider read path above.)

**Category → FactKind is a 1:1 map today, and that is CHAOS-4347's other
half.** `contextFabricDriverCategoryFactKind`
(`internal/contracts/v1/context_fabric_types.go:483-498`) maps exactly 14 of
16 driver categories to exactly one `FactKind` each (`relationship` and
`narrative` map to none — no canonical fact backs either judgment kind).
Because the map is 1:1, `status` routes only to `FactStatus`
(work_item-only, per the table above) — a repository- or project-scoped
"status" question has no fact route by construction, not because the
subject is unreachable (diagram 3 shows it usually is reachable). Ruled
2026-08-26 (`cf-rulings.md:316`, chris): a new v1 `FactKind` (working name
`project_status`) or a widened `StatusProvider.SupportedSubjectKinds` is the
fix, lane's choice; approved in principle, implementation "Phase 2 held
pending trial-case validation" as of 17:42 PDT — **not yet in
`context_fabric_types.go` on `main`**.

**Correction (flagged by lane-4347-ch, not fixed in this PR — out of this
lane's scope): the paragraph above is now stale.** `lane-4335`'s CHAOS-4347
PR #298 (`internal/contextfabric/chaos4347_status_category_composition.go`)
merged as `1e9815c7`, ahead of this page's own cited baseline `569f0f39`.
It does not touch the 1:1 `contextFabricDriverCategoryFactKind` map itself,
but adds a composition step (`composeStatusCategoryRequirements`) between
that map and the providers: a resolved-subject-kind repository/team
`status` requirement now expands into repository→{metrics, health,
identity} / team→{health, workload, readiness} before planning, so
"status routes only to FactStatus" is no longer true post-resolution for
those two kinds (work_item stays 1:1). The `project_status`
new-FactKind-vs-widen-`StatusProvider` decision above is unaffected and
still open — that is a separate, still-unbuilt slice
(`lane-4347-project`, CHAOS-4348 reachability work is currently ahead of
it in that lane's queue).

`FactCapability.SupportedSubjectKinds` (`fact_registry.go:50-58`) is set
once, in each provider's own `Capability()` method — code-declared, never
model/phrasing-derived (`internal/contextfabric/AGENTS.md:49`).
`planFactReads` (`fact_planner.go:183`) compares resolved subjects against
that declared set and prunes/narrows before any provider runs; the
`FactReadScopeResolver` (`fact_scope.go`, CHAOS-4099, **activated** —
project and team policies both ruled and shipped, `context-fabric-fact-scope.md`
§9-10) sits one seam earlier, expanding a project/team origin to its
activity-proxy repositories/PRs/reviews so the honest "unexpanded, but
reachable" disclosure replaces a false "pruned, proven absent" one where a
verified typed chain exists.

---

## 5 — Two-turn trial harness measurement map

```mermaid
flowchart LR
  subgraph RES["Resolution hop"]
    A1["expected_subject_in_pool<br/>chaos3742_two_turn_confirmation_test.go:2309<br/>reads 'corroboration' trace -- TRACE-WIDE,<br/>true if reached POOL anywhere in the call<br/>(diagram 2: never true for offerOnlyPool hits)"]
    A2["expected_subject_rank /<br/>expected_subject_at_offer_boundary<br/>reads 'ranked_cut' trace (resolve.go:943-961)<br/>-- CALL-BOUNDARY scoped, after the final cut"]
  end
  subgraph COMMIT["Commit hop"]
    B1["committed_count<br/>= len(SubjectResolution.Committed)"]
    B2["wrong_commit<br/>twoTurnCommittedWrong -- FALSE when<br/>committed_count==0 too, not just when correct"]
  end
  subgraph WGATE["Regime-A window-gate hop"]
    C1["window_gated_count<br/>Turn2=clarification_required AND<br/>TierRoutedCorrectly AND committed_count==0"]
    C2["window_gated_offered / _silent /<br/>_already_widest_count<br/>PRs #292/#293/#294 -- partitions window_gated_count<br/>by whether window_expand was recommended"]
  end
  subgraph SYNTH["Synthesis/terminal hop"]
    D1["false_no_match<br/>expectID!='' AND status==no_match"]
  end
  subgraph MISSING["No field exists for this hop"]
    E1["canonical_facts_count -- NOT a real field.<br/>No per-case field measures fact-planning/<br/>ReadFacts output count anywhere in the harness."]
  end

  RES -->|"pool membership proves NEITHER<br/>offer-eligibility NOR commit"| COMMIT
  COMMIT -->|"wrong_commit=0 alone proves only<br/>'no incorrect commit', never 'reachable'"| WGATE
  WGATE -->|"fires only on the regime-A gate;<br/>says nothing about pool or commit"| SYNTH

  classDef gap fill:#78350f,stroke:#f59e0b,color:#ffffff
  classDef fixed fill:#14532d,stroke:#22c55e,color:#ffffff
  class A1,E1 gap
  class B2 fixed
```

**Caption.** Five field families, five different hops, and none of them
implies another. `expected_subject_in_pool` is the crux of the incident this
page exists to prevent a repeat of: it is scanned from a "corroboration"
trace event emitted only for candidates that reached the commit-eligible
`pool` (diagram 2) — a repository/project/team subject correctly retrieved
into the CHAOS-4271 `offerOnlyPool` reads **false** here even though it was
genuinely found and offered. `wrong_commit=0` is the second trap: the field
is `false` both when the commit was correct and when nothing committed at
all, so it certifies safety, never reachability. `window_gated_*` measures
a completely separate concern — whether the CHAOS-4040 regime-A gate fired
and disclosed a window-widening offer — and has no bearing on pool or
commit correctness. **`canonical_facts_count`, named in this diagram's
original brief, does not exist in the harness as a field** — no
`twoTurnCaseResult` field, no `cmd/acr-trial-merge-two-turn/main.go` report
field, projects fact-planning/`ReadFacts` output count. Flagged to
team-lead as aspirational, not a stale-vs-current discrepancy, since no
prior doc claimed it existed.

---

## Sources

- Live code via `codegraph_explore` / `rg` on `dev-health-acr`: `engine.go`
  (`Investigate`), `chaos4234_offers_only.go`, `chaos4038_kind_coverage.go`,
  `chaos4132_confirmed_kind_rescue.go`, `graphrank/resolve.go`,
  `graphrank/resolution.go`, `falkorgraph/vector.go`, `falkorgraph/reader.go`,
  `fact_planner.go`, `fact_registry.go`, `fact_scope.go`,
  `internal/contracts/v1/context_fabric_types.go`,
  `internal/contextfabric/devhealthfacts/*.go`,
  `internal/contextfabric/devhealthsource/teams_projects.go`,
  `internal/contextfabric/devhealthsource/teams_projects_edges.go`,
  `internal/contextfabric/devhealthsource/clickhouse.go`,
  `internal/runtime/hosted/chaos3742_two_turn_confirmation_test.go`,
  `internal/runtime/hosted/chaos4234_regime_a_harness_test.go`,
  `cmd/acr-trial-merge-two-turn/main.go`.
- Live kiac trial graph: `kubectl -n acr-trial-data exec pod/trial-falkordb-...
  -- redis-cli GRAPH.QUERY acr-cf-fa7030e2106de7411bfbf8ebce74c620-e2 "..."`,
  2026-08-26, counts only, org `70d529e0-3c06-4597-8480-794fd02328b6`.
- Design docs: `docs/design/context-fabric-team-project-subjects.md`,
  `docs/design/context-fabric-regime-a-offers.md`,
  `docs/design/context-fabric-fact-planning.md`,
  `docs/design/context-fabric-fact-scope.md`,
  `docs/design/context-fabric-vector-retrieval.md`,
  `internal/contextfabric/AGENTS.md`, `acr/AGENTS.md`.
- Session state (cited as session notes, not committed fact):
  `.remember/context-fabric-reset-handoff.md`,
  `.remember/context-fabric/cf-rulings.md`, `.remember/now.md`,
  `.remember/today-2026-08-26.md`, `.remember/retro-4061-diagrams-draft.md`
  (diagram-style precedent reused here).
