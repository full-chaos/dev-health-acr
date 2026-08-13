# Context Fabric: team and project as first-class subject kinds (CHAOS-3802)

Short design note. Covers: which canonical ClickHouse tables each kind reads,
the id-space and ReplacingMergeTree traps proved against live data, validity
and authorization semantics, `canonical_id`/`search_text` composition, and an
explicit list of what this issue does **not** do.

Every row count below was read from live ClickHouse (`dev-health-clickhouse-1`,
database `default`) against the ground-truth org
`70d529e0-3c06-4597-8480-794fd02328b6`. Every column type came from
`system.columns`, not from guessing.

## 0. Where the code goes

`internal/contextfabric/devhealthsource/teams_projects.go` already exists as a
documented, fail-loud stub behind `ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED`
(`internal/config/projector.go`), already registered as its own named source in
`cmd/acr-projector/runtime.go`. This issue fills that seam in. It does **not**
touch `ClickHouseProjectionSource`.

That placement is deliberate, and it is what makes the acceptance criterion
("an org rebuild picks the new kinds up with no second migration") free:

- `projectionrun` checkpoints are keyed `(org_id, source)`. `dev_health_teams_projects`
  has never written a checkpoint row, so its first enabled tick reads
  `Cursor == ""` and takes the `fullSnapshot` path. No migration, no backfill.
- Keeping the work-item→team / work-item→project edges here rather than in
  `ClickHouseProjectionSource` avoids bumping `ClickHouseSourceVersion` to v4,
  which would force `ErrProjectionSourceVersionChanged` and a full rebuild on
  every already-projected org for content none of them asked for.

One shared-code note (added during implementation): the paging behavior
`ClickHouseProjectionSource` already had is not simple — it carries CHAOS-3753's
C6 (refusing an oversized org leaves it permanently stuck), K4 (an aggregate
candidate count can exceed the contract bound with no single table truncated),
and K2 (a page boundary must never split one source row's candidates) fixes.
Rather than re-derive those in a second source, both sources now instantiate one
`sourcePlan` (`assemble.go`); the only per-source variation is data — table set,
batch identity, an optional once-per-from-scratch seed, an optional observer.
This is the one structural change in an otherwise additive issue, taken because
duplicating three hard-won pagination fixes is a worse risk than the rebase
surface of three small function bodies.

The graph write path needs no changes at all. `falkorgraph`'s
`ownedSubjectMergeCypher` / `subjectMergeAttrs` / `entitySearchText` /
`kindLabel` are all generic over `SubjectKind`; `team` and `project` land as
`:Subject:Team` / `:Subject:Project` with `search_text` populated the moment
they are emitted as owned entities. Same for CHAOS-3778's embed projection —
it hangs off owned entities, so new kinds flow through it on rebase with no
per-kind code.

## 1. Sources, verified

### 1.1 `teams` — the Team subject

`ReplacingMergeTree(updated_at) ORDER BY (org_id, id)`. `FINAL` collapses
cleanly to one row per `(org_id, id)`.

Live types: `id String`, `team_uuid UUID`, `name String`,
`description Nullable(String)`, `provider String`,
`native_team_key Nullable(String)`, `parent_team_id Nullable(String)`,
`project_keys Array(String)`, `is_active UInt8`, `updated_at DateTime64(6)`,
`last_synced DateTime64(6)`, `org_id String`.

Note `updated_at` carries **no timezone qualifier** (`DateTime64(6)`, not
`DateTime64(6,'UTC')`). This is fine and already precedented: `work_items.updated_at`
is `DateTime64(3)` and the existing `sincePredicate` binds `{since:DateTime64(6,'UTC')}`
against it today. DateTime64's timezone is display metadata; comparison is on
ticks. Cursor uses `updated_at`, not `last_synced` (finer, and it is the real
change time).

Ground truth org holds 3 teams: `gh:ops-team` (github), `gl:full.chaos` (gitlab),
`CHAOS` (linear).

### 1.2 `projects` — the Project subject

`ReplacingMergeTree(updated_at) ORDER BY (org_id, provider, id)`.

Live types: `id String`, `org_id String`, `provider String`,
`project_key Nullable(String)`, `name String`, `is_active UInt8`,
`state LowCardinality(String)`, `target_date Nullable(Date)`, `url String`,
`team_ids Array(String)`, `team_keys Array(String)`,
`updated_at DateTime64(3,'UTC')`.

56 raw rows for the ground-truth org, **20 after `FINAL`**. The dedup key
includes `provider`, so `id` is only unique per provider in principle — checked
across all orgs: zero `(org_id, id)` pairs carry more than one provider, so
`project:<projects.id>` is a safe canonical id today. The query still groups
defensively rather than assuming it.

## 2. Id-space traps (all three verified, all three cost a wedge if ignored)

**Trap A — `projects.team_ids` is the provider's native id space, not ours.**
The Linear projects carry `team_ids = ['ca148f86-321e-401b-b028-ad695901823f']`.
The Linear team's `teams.id` is `CHAOS` and its `teams.team_uuid` is
`3d89b2cf-643a-5ae1-8d2a-3f07d91a121d`. The array value matches **neither**.
`projects.team_ids` is unusable for a project→team edge. `projects.team_keys`
(`['CHAOS']`) does match `teams.id`, but it is empty for both gitlab projects
(coverage: 17 of 20 projects have a `team_keys` entry, and all 17 are linear).

**Trap B — `team_project_ownership.project_id` is not `projects.id`.**
Of 3 distinct `project_id` values, only 1 resolves against `projects.id`. The
gitlab rows carry `full.chaos/chaos-ops` where `projects.id` is
`70d529e0-…:gitlab:71133891`. The `project_key` column *does* join cleanly:
3 of 3 distinct `project_key` values resolve to a `projects` row. **Join on
`project_key`, never on `project_id`.**

**Trap C — `team_project_ownership` is append-per-sync, and `FINAL` does not
save you.** Its ORDER BY is
`(org_id, provider, project_id, team_id, source, valid_from)` — `valid_from` is
part of the dedup key, so `FINAL` returns **616 rows for 3 real edges**, every
one of them with `valid_to IS NULL` (one edge alone has 608 open windows). Read
naively, that emits 616 relationship candidates carrying 3 distinct
`RelationshipID`s, `ContextFabricProjectionBatch.Validate()` rejects the batch
("relationship IDs must be unique within a batch"), and the org's projection
wedges permanently. The query must collapse to one row per
`(provider, project_id, team_id, source)` with `min(valid_from)` /
`argMax(valid_to, valid_from)` before a candidate is ever built.

**Non-trap, resolved — `work_unit_membership`.** The mission flagged the
`work_unit_id` (SHA space) vs `work_item_id` (`linear:CHAOS-xxxx` space) hazard.
Checked live: this table's `node_type` values are `issue` / `pr` / `commit` and
its `category_kind` values are `theme` / `subcategory`. It is work-unit *theme
clustering*, not team or project membership. It is not a source for either kind
and this issue does not read it. The id-space question is moot.

## 3. Membership and attribution semantics, with counts

| Edge | Source | Live evidence |
| --- | --- | --- |
| `work_item` → `project` | `work_items.project_id` INNER JOIN `projects FINAL` on `(org_id, id)` | 3086 of 3304 work items carry a non-empty `project_id`; 18 distinct values, **16 resolve**; **3080 of 3086 rows** join. `work_items.project_key` is empty on every row (0 of 3304) — do not use it. |
| `work_item` → `team` | `work_item_team_attributions` where `is_primary = 1 AND team_id IS NOT NULL` | 3304 distinct work items, **all 3304 resolve** against `work_items`. **Zero** work items carry more than one primary team — the edge is genuinely 1:1. |
| `project` → `team` | `team_project_ownership`, collapsed per Trap C, joined to `projects` on `project_key` and to `teams` on `teams.id` | 3 real edges. `team_id` values `{CHAOS, gl:full.chaos}` are 2-for-2 against `teams.id`. |

The two dangling `project_id` values (6 work-item rows) are dropped by the INNER
JOIN — the same discipline `queryWorkItemHierarchy` already applies to an
unresolvable `parent_id`, and for the same reason: a dangling endpoint would be
a lie in the graph, not a fact.

## 4. Canonical id shapes — pinned, not chosen

**`team:` + `teams.id`.** This is not a free choice. `devhealthfacts` already
minted `teamPrefix = "team:"` (`workload.go:16`, CHAOS-3780), and
`subjectIndex` strips exactly that prefix to feed `team_id IN {ids:Array(String)}`
against the team fact tables. Proved live that those tables speak `teams.id`:
`capacity_forecasts` distinct `team_id` = `{CHAOS}`;
`estimate_coverage_metrics_daily` = `{gl:full.chaos, CHAOS}`. Both are subsets
of `teams.id` = `{gh:ops-team, gl:full.chaos, CHAOS}`. Emitting
`team:CHAOS` lights up all five existing team fact providers with **zero new
fact-provider code**.

**`project:` + `projects.id`.** No prefix precedent exists (nothing reads
project-scoped facts), so this follows the same convention. It is the only id
space `work_items.project_id` joins.

## 5. `search_text` composition

`falkorgraph.entitySearchText` is `Label + "\n" + Aliases + "\n" + PreviousNames`
for owned entities. So:

- **Team** — `Label` = `teams.name` (`Ops Team`, `fullchaos`, `Fullchaos`).
  `Aliases` = unique non-empty of `{teams.id, native_team_key}`.
  `ProviderIDs` = `{provider: teams.id}` (mirrors `queryRepositories`).
  `Properties` = `is_active`, and `description` when non-empty.
- **Project** — `Label` = `projects.name`. `Aliases` = `{project_key}` when
  non-empty. `ProviderIDs` = `{provider: projects.id}`. `Properties` = `state`,
  `is_active`, `url` when non-empty.

Raw UUIDs stay out of `Aliases` — they are lexical noise, and exact identity
already resolves through `ResolveDeps.ExactHint` on `canonical_id`.

## 6. Validity semantics

Owned-write discipline per CHAOS-3785 R3-1: the owned path asserts
`ValidFrom`/`ValidTo` **either way**, so a nil actively clears a window some
earlier stub write may have seeded. Referenced endpoints stay window-free.

- **Team / Project entities.** Neither table has a validity column; `is_active`
  is the only lifecycle signal. Active rows: `ValidFrom = nil, ValidTo = nil`,
  explicitly asserted through the owned path. Inactive rows (`is_active = 0`):
  `ValidTo = updated_at`. A tombstone would be wrong — the two inactive
  projects in the ground-truth org are `state = completed`, which is a finished
  project, not a deleted one.
- **`project` → `team` edge.** This is the one place with real windows:
  `ValidFrom = min(valid_from)` over open windows (honestly "ownership has been
  observed continuously since"), `ValidTo = argMax(valid_to, valid_from)`.
- **`work_item` → project/team edges.** Window-free; neither source table
  carries validity.

Flagged for the CHAOS-3781 rebase: these are additive per-kind declarations, not
a new axis. If 3781 lands a different convention for "no validity column
exists", this note's §6 is the only thing that changes.

## 7. Authorization scope

- **Team entity** → `TeamIDs: [teams.id]`. The contract field exists and no
  current producer populates it; nothing to reserve or collide with.
- **Project entity** → `ProjectIDs: [projects.id]`, and this **must** call
  `IsReservedAuthorizationScopeID` first and reject (never silently rename) a
  collision. That is the reservation obligation already written into
  `teams_projects.go`'s doc comment and enforced at the contract boundary by
  `ContextFabricEntityProjection.Validate()`. No live project id falls in the
  reserved namespace today; the guard ships regardless.
- **`work_item` → project/team edges** → `workItemAuthorization(repoID, repoSlug)`,
  deriving the repo from `work_items`' own `repo_id` via `LEFT JOIN repos`. Not
  from `work_item_team_attributions.repo_id`: 5077 of its 5089 rows carry the
  zero UUID, exactly the CHAOS-3785 trap.
- Every join carries `org_id` equality. No exceptions.

## 8. Decisions (ruled 2026-08-13)

**D1 — relationship vocabulary. RULED: add the members.**
`ContextFabricRelationshipType` is a closed enum with no team/project
containment member, so `BELONGS_TO_PROJECT` and `OWNED_BY_TEAM` are added as
additive v1 values, following CHAOS-3779's precedent (`BLOCKS` / `PART_OF` /
`RELATES_TO` / `DUPLICATES` went in the same way). This costs the full
contract-first unit: Go types, JSON Schema, OpenAPI + YAML mirror, MCP copies,
golden fixtures, parity tests.

Overloading the existing `PART_OF` was considered and **rejected**: the closed
vocabulary exists so semantics stay distinct, and `graphrank`'s traversal
corroboration must be able to tell hierarchy from containment. Saving contract
churn by blurring that is the wrong trade.

Two declared contact points this creates:

- **CHAOS-3746** (ships before this issue) carries a vocabulary parity test that
  compares the enum member-for-member. This branch re-pins that test when it
  rebases onto 3746's merged state. **That failure is intended behavior**, not a
  regression — budget for it.
- **dev-health-web** picks up the two new enum members at its next ACR contract
  pin bump, through the CHAOS-3791 codegen machinery. Nothing to do here; named
  so nobody is surprised.

**D2 — flag default. RULED: flip to `true` in this change.**
The fail-loud stub was the only justification for `false`; once the source is
implemented, a default-off feature whose acceptance criterion requires it on is
a dead guard.

Carried requirement (composition-root reachability, a prior wave's lesson): the
flag must be verified to gate **only** this source at the composition root, with
no short-circuit upstream of the flag check that would make the flag dead in
either direction.

**D3 — `is_active = 0` → `ValidTo = updated_at`. CONFIRMED**, not a tombstone
(§6). Completed is finished, not deleted; a tombstone would erase exactly the
history the CHAOS-3781 temporal axis exists to answer over.

## 9. What this issue does NOT do

- **No new fact providers, no new `FactKind`.** `project` gets **zero**
  fact-provider entries: no project-scoped canonical fact read exists.
  (`project_declared_state_floor` / `_history` are real and project-scoped, but
  map to no `FactKind`; minting one is inventing a fact.)
- **No capability-matrix change for `team` either.** All five team providers —
  `workload`, `investment`, `readiness`, `operational_deficiencies`, `health` —
  already declare `SubjectTeam`. They have been dark only because no team
  subject ever resolved. Mission item (3) is therefore an honest near-no-op:
  the matrix is already correct, and projecting the subject is what activates it.
  Verification, not extension.
- **No person/member subject kind and no `team_memberships` edges.** No such
  subject kind exists, and the table dedups to 4 distinct `(team_id, member_id)`
  pairs behind 1526 rows anyway.
- **No `work_unit_membership` read** (§2).
- **No embedding work.** CHAOS-3778's embed projection is generic over owned
  entities; new kinds inherit it on rebase.
- **No changes to `ClickHouseProjectionSource` or `ClickHouseSourceVersion`** (§0).
