# Context Fabric projection worker (CHAOS-3753, Reset 1A)

Short design note. Covers: what the production `ProjectionSource` reads, where
checkpoints persist, and the worker/binary shape. Written before bulk
implementation per the handoff; open decisions are called out for veto.

## 1. ProjectionSource: what it reads

ACR's only existing canonical-data path is `internal/contextpacket`'s
ClickHouse read boundary (`ClickHouseQueryClient`, `ReadPlan`, the
`SourceQueryCatalogV1` pattern). There is no outbox and no other ingest path
in this repo, and none should be invented. The production `ProjectionSource`
(new package `internal/contextfabric/devhealthsource`) reuses that same
ClickHouse client and binding style, org-scoped, cursor-paginated on
`(updated_at/last_synced/observed_at, id)`.

Confirmed real, queryable per-org tables (already read by `contextpacket`):
`repos`, `work_items`, `work_item_dependencies`, `git_pull_requests`,
`git_pull_request_reviews`, `ci_pipeline_runs`, `deployments`,
`operational_incidents`, `work_graph_edges`,
`work_graph_deployment_incident_edges`,
`work_graph_pr_review_outcome_edges`. These back the required Repository,
WorkItem, PullRequest, Deployment, Incident entity kinds, the
`PullRequestReview`/`CIRun` kinds added post-review (codex finding C7,
below), and the explicit relationship projections (from the `work_graph_*`
edge tables and `work_item_dependencies`). `operational_incidents.is_deleted`
is an existing
soft-delete column and is the tombstone signal for incidents; the same
pattern (an `is_active`/`*_at` marker where a table has one) drives
tombstones elsewhere — commits are immutable and never tombstone.

Organization: there is no `orgs` table. The org entity is synthesized
one-per-org from the principal's `OrgID` the worker is already scoped to,
not queried.

**Flagged for 1B/1C org-level authorization review:** the synthesized
Organization entity's `AuthorizationScope` has no dedicated organization
field to use (the contract only has `RepositorySlugs`/`ProjectIDs`/`TeamIDs`),
so it's placed in `ProjectIDs` under a reserved namespace prefix
(`organizationScopePrefix`, `IsReservedAuthorizationScopeID`,
`clickhouse.go`). Every other entity/relationship this source produces uses
`RepositorySlugs` instead (proved never to collide by
`TestOnlyTheOrganizationEntityPopulatesProjectIDs`), so there is no
collision risk from anything in this repository today. But this is a
**convention, not a contract-enforced guarantee**: a future real Project ID
provider (the still-unimplemented `TeamsProjectsSource`) could in principle
be handed a value equal to the reserved prefix by its upstream data source,
which would incorrectly inherit organization-wide authorization once
filtering exists. `TeamsProjectsSource`'s doc comment obligates any real
implementation to check `IsReservedAuthorizationScopeID` and reject (not
rename) a collision, but the durable fix is a dedicated organization-scope
field on `ContextFabricAuthorizationScope` (a contract change) once Reset
1B/1C actually builds authorization filtering — please treat this as an
open item for that design, not a closed one.

**No canonical source exists yet in this repo for Team, Project, or
Decision/Document entities** — no ClickHouse table, no other adapter. Per
the handoff, I am not inventing one. `devhealthsource` defines the seam
(`TeamProjectSource`, doc-commented contract) and ships a stub that returns
zero rows, gated off by `ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED`
(default `false`). Enabling it is follow-up work once Dev Health Ops
publishes a canonical table/API for those kinds.

Approved episodes are **not** ClickHouse data — they're ACR's own
`acr.agent_episodes` (Postgres, `internal/storage.EpisodeStore`). Per the
ruling below, `EpisodeStore` gained one narrow read method,
`ListSince(ctx, orgID, since, afterEpisodeID, limit)`, implemented in both
`internal/storage/postgres` and `internal/storage/memory` with tests in
each — the projection worker is just one more `EpisodeStore` caller, the
same way `internal/episode.Service` is. "Approved" here means durably
created (`CreateIdempotent`) and not yet redacted/purged; a later
`Redact` or `no_persist` retention is projected as a tombstone.

Full-snapshot rebuild is not a separate `ProjectionSource` method: a
checkpoint reset to the zero cursor signals "start of full snapshot" to
`devhealthsource`, which then emits `FullSnapshot: true` batches until it has
enumerated every entity kind, setting `CompleteEnumeration: true` on the
last one — matching `ContextFabricProjectionBatch.Validate()`'s existing
`FullSnapshot ⇒ CompleteEnumeration` rule. No interface change needed.

## 2. Checkpoints: Postgres

New migration `0006_context_fabric_projection_checkpoints.sql`, table
`acr.context_fabric_projection_checkpoints` (`org_id, source` primary key;
`cursor, source_version, backend_watermark, updated_at`). Production
`ProjectionCheckpointStore` lives in `internal/storage/postgres`, following
the existing CAS convention (`UPDATE ... WHERE org_id = $1 AND source = $2
AND cursor = $3`, checked via `RowsAffected`; `INSERT ... ON CONFLICT DO
NOTHING` for the first-ever checkpoint) — same shape as
`device_authorization_transition.go` / `credentials.go`. A 0-row update maps
to `contextfabric.ErrProjectionConflict`, matching `ProjectionWorker`'s
existing contract.

## 3. Worker / binary shape

New `cmd/acr-projector` binary (`serve`/`version`, same flag/signal/logging
skeleton as `cmd/acr-api`). It owns its own lifecycle independent of
`acr-api` (Helm: separate Deployment; Compose: separate service), matching
"independent lifecycle/deployment control" in the ticket.

Inside it, a new hosting-composition package (`internal/contextfabric/
projectionrun`, *not* the domain `contextfabric` package — this is
scheduling/queue/DB concern, kept out of `projector.go` per
`internal/contextfabric/AGENTS.md`) runs a **Coordinator**:

- one goroutine pool bounded by configured concurrency; work items are
  `(orgID, sourceName)` pairs drawn from configured sources
  (`dev_health_clickhouse`, `dev_health_episodes`, ...) × the org set;
- **single-flight per organization (the amendment):** before running any
  source's `ProjectionWorker.RunOnce` for an org, the coordinator takes a
  Postgres advisory lock (`pg_try_advisory_lock`, keyed by a hash of
  `orgID`) and holds it for that org/source's whole run, released after.
  This is a two-layer guard: an in-process per-org mutex avoids pointless
  goroutine contention, and the advisory lock makes the guarantee hold
  across `acr-projector` replicas too (I'd rather not assume replicas=1 by
  convention — a second replica during a rolling deploy would otherwise
  reintroduce exactly the interleaving ADR 0007 warns about). Open to doing
  in-process-only + `replicas: 1` + a Helm anti-affinity/PDB instead if you'd
  rather not add the advisory-lock dependency — flagging as the one
  correctness-critical design choice to veto.
- retries: bounded exponential backoff with jitter per `(org, source)` on
  `ErrUnavailable`/transient errors, tracked in-memory; `ErrProjectionConflict`
  is not retried in the same tick (another writer moved the cursor; picked up
  next poll); failures in one `(org, source)` never block others (failure
  isolation);
- cancellation: `signal.NotifyContext` at the binary, threaded through every
  `RunOnce`/advisory-lock call;
- readiness: `GET /healthz` / `/readyz` on a small HTTP server, reusing
  `api.ReadinessCheck`/`api.CheckFunc` from `internal/api` rather than a new
  shape — checks Postgres, ClickHouse, and (if configured) Zep;
- telemetry: reuses `internal/observability` hooks — operation, org (not
  logged raw; same hashing/redaction posture as existing telemetry),
  source, batch size, duration, watermark/lag, outcome. No entity content,
  ever.

Org set to project: **proposing an explicit allowlist env var**
(`ACR_CONTEXT_FABRIC_PROJECTOR_ORGS`) for this reset rather than
auto-discovering from `acr.client_credentials` `DISTINCT org_id` — smaller
blast radius while this is new, with auto-discovery as an easy follow-up.
Flagging as the other open call.

## 4. Independent enable/disable

- `ACR_CONTEXT_FABRIC_PROJECTION_ENABLED` (default `false`) — `acr-projector`'s
  own master switch; off means the coordinator loop never starts (readiness
  server still runs, reports disabled-not-unhealthy).
- Graph-*read* enablement is Reset 1B/1C's (`GraphReader`/hosted composition)
  flag to define, but since both lanes are in flight concurrently, I'm
  reserving the name now so we don't collide: propose
  `ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED`. 3753 does not wire it to
  anything; 3753 only guarantees projection's flag has no effect on it and
  vice versa. Worth a one-line sync with whoever's on 3754.
- Rollback = scale `acr-projector` to zero / flip its flag off; `zepgraph`
  and canonical sources are untouched; existing `ProjectionBackend.
  PurgeOrganization` + checkpoint reset gives org-scoped rebuild.

## Rebuild atomicity (codex finding C2, post-review)

`PurgeOrganization` (the backend) and resetting every source's checkpoint
(Postgres) are two separate durable systems with no shared transaction.
The first implementation purged first and reset checkpoints second with
nothing recording that a rebuild was underway; a crash between the two
left a checkpoint still pointing at a real cursor while the graph behind
it was gone, and the next ordinary tick would run *incremental* projection
against that purged graph -- applying only the delta, not a full replay,
silently losing the organization's history while looking like a normal
successful tick.

Fixed with a durable rebuild marker
(`acr.context_fabric_projection_rebuild_markers`, migration 0007;
`internal/contextfabric/projectionrun.RebuildMarker`,
`pgprojection.RebuildMarkerStore`): `BeginRebuild` (idempotent insert)
commits *before* the purge; `CompleteRebuild` (idempotent delete) runs only
after every checkpoint is confirmed reset. `Coordinator.runOrg` checks the
marker before running any ordinary per-source tick for an organization; if
present, it resumes the exact same idempotent purge-then-reset-then-clear
sequence (`performRebuild`) instead of projecting, and skips ordinary
projection for that tick regardless of outcome. Every step in the sequence
is independently idempotent (purge per ADR 0007, checkpoint reset is a
no-op when already `""`, marker insert/delete are `ON CONFLICT`/no-op-on-absence),
so resuming from an unknown crash point is always safe to just redo from
the top rather than requiring point-in-time crash detection.

Proven with a fault-injection probe
(`TestCoordinatorRefusesIncrementalProjectionAfterACrashBetweenPurgeAndReset`):
drive `BeginRebuild` + `PurgeOrganization` directly (simulating a crash
before the reset step), then tick, and assert the source is never asked
for a batch against the stale checkpoint -- confirmed to fail without the
fix (temporarily disabling the marker check reproduces the exact bug), and
to pass with it.

## Keyset pagination, oversized organizations, and tenant-scoped joins (codex findings C5/C6/W1, post-review)

**C5 — pagination tiebreaker.** Every table's cursor pagination compares
`(timestampExpr, rowKeyExpr)` in strict lexicographic order (`ts > since OR
(ts = since AND rowKey > after)`), so that a page boundary landing inside a
group of rows sharing one timestamp — common: bulk syncs land many rows in
the same second — still emits each row exactly once. The first
implementation hardcoded the tiebreaker to a bare `id`, which — because
nearly every query joins `repos AS r` — silently resolved to `repos.id`
instead of the entity's own identifier for six of seven tables (`repos`
itself was the one table it happened to get right). `sincePredicate`/
`orderBy` now take an explicit `rowKeyExpr` per table, and every
`candidate.sortKey` was corrected to match exactly what that expression
produces. Proven by
`TestClickHouseProjectionSourceKeysetPaginationSurvivesTiedTimestamps`,
which deliberately probes a *joined* table (`work_items`), not `repos`,
since a repos-only probe would not have caught the original bug.

**Honest limitation: event-time watermarks miss backfilled/corrected rows.**
This cursor is `(observed/updated timestamp, id)`, not a monotonic sequence
number. If a source system backfills or corrects a row so its timestamp
column moves *backward* relative to a watermark this source has already
passed — a corrected `updated_at`, a late-arriving webhook that predates
rows already projected — that row will not be picked up by any future
incremental batch; only a full rebuild (`acr-projector rebuild --org`)
re-observes it. This is a real, currently-accepted gap, not a hidden one:
closing it properly needs either a strictly-monotonic ingest-side sequence
column (not currently in ClickHouse schema) or a small trailing
re-scan window, and is out of scope for Reset 1A.

**C6 — oversized organizations.** `fullSnapshot()` used to return a hard,
permanent error when any table exceeded its per-query cap
(`snapshotPerQueryCap`, 150 rows) — and since a never-before-projected
organization always starts at a zero cursor (always routed through
`fullSnapshot()`), any organization above that size could never complete
initial projection; every tick re-attempted and failed the identical
oversized single-batch snapshot. It now falls back to the same bounded
per-tick paging path `incremental()` already used (extracted into a shared
`pagedBatch`), completing catch-up across ordinary ticks instead of
erroring forever. Proven by
`TestClickHouseProjectionSourceFullSnapshotPagesToCompletionWhenOversized`.

**W1 — tenant-scoped joins.** Six `INNER JOIN repos AS r` clauses compared
only `r.id = <table>.repo_id`, without also requiring `r.org_id =
<table>.org_id`. Two independently-synced repositories that happen to
share an `id` across two different organizations — a real risk with
non-UUID source ids, replayed syncs, or seed data — could join to the
*wrong* tenant's repository row, attaching a foreign slug and
authorization scope. All six joins now also require organization equality.
Proven against a real ClickHouse instance (the package's `fakeClient`
cannot execute an actual SQL join, so it could not have caught this bug —
see the fake's `cursorOf` doc comment, and W2 below) by
`TestClickHouseProjectionSourceScopesTheRepositoryJoinByOrganization`,
which seeds two organizations' `repos` rows sharing one `id` and asserts
the projected work item never picks up the wrong tenant's slug.

All three fixes were verified probe-first: each new test was confirmed to
fail (with the original bug's exact symptom) against a temporary revert of
its fix, then to pass again once the fix was restored.

## Revocations must reach the graph (codex finding C4, post-review)

`EpisodeStore.ListSince` ordered and paginated on `CreatedAt`, which never
changes. `devhealthsource.episodeCandidate`'s tombstone conversion (any
`RedactionState != "active"` becomes a `ProjectionTombstone`) was always
correct, but `ListSince` never handed it a row whose state changed *after*
a caller's checkpoint had already advanced past that row's `CreatedAt`
position -- a `Redact()` or `PurgeExpiredForPrincipal()` call happening
post-projection silently never reached the graph, even though the contract
doc comment claimed otherwise ("a caller can detect and propagate the
state change").

Fixed with a genuine last-modification watermark: `acr.agent_episodes`
gained an `updated_at` column (migration `0008`), set equal to `created_at`
at insert time (same `NOW()`, same statement) and bumped by both `Redact()`
and `PurgeExpiredForPrincipal()`. `ListSince` now orders/paginates on
`(updated_at, episode_id)` instead of `(created_at, episode_id)` in both
`internal/storage/postgres` and `internal/storage/memory` (mirroring the
existing dual-implementation requirement), and
`storage.EpisodeProjectionRecord` carries the new `UpdatedAt` field.
`devhealthsource.episodeCandidate`'s `observedAt`/tombstone `EffectiveAt`
now use `UpdatedAt`, not `CreatedAt` -- using the wrong column here would
be the same C5-class bug (the candidate's cursor position must match
exactly what the next `ListSince` call filters on, or the cursor never
converges with the row it's meant to track).

**Honest backfill limitation**: rows already purged
(`redaction_state = 'purged_tombstone'`) before this migration have no
historical modification timestamp to recover -- that absence is exactly
what C4 identified. The migration backfills `updated_at =
COALESCE(redacted_at, created_at)` for existing rows, which is the best
available signal; only prospectively (state transitions after the
migration applies) does the fix fully close the gap.

Proven probe-first at three layers, each confirmed to fail against a
temporary revert and pass again with the fix restored:
- `internal/storage/postgres`:
  `TestEpisodeStore_ListSinceSurfacesARedactionThatHappensAfterTheWatermarkAlreadyPassedTheRow`
  and the `...Purge...` counterpart, against real Postgres.
- `internal/storage/memory`: the same two probes, against the in-memory
  store.
- `internal/contextfabric/devhealthsource`:
  `TestEpisodesProjectionSourceRedactionAfterProjectionSurfacesAsATombstoneInTheNextBatch`
  drives `EpisodesProjectionSource.NextProjectionBatch` twice against a real
  `memory.EpisodeStore` -- project, redact, then resume from the
  already-advanced checkpoint and assert the next batch contains the
  tombstone, exactly the scenario the review specified.

## PR reviews and CI runs (codex finding C7, post-review)

The original design note listed `git_pull_request_reviews` and
`ci_pipeline_runs` among the "confirmed real, queryable per-org tables,"
but no batch ever actually emitted either -- the design doc's stated scope
didn't match the shipped coverage. Per the review's stated preference
("implement unless there's a real blocker; reviews/CI runs are core
work-graph signal"), both are now implemented, not descoped: two new
entity kinds, `ContextFabricSubjectPullRequestReview`
(`pull_request_review`) and `ContextFabricSubjectCIRun`
(`ci_pipeline_run`), added additively to the v1 contract (JSON Schema +
Go, both directions, per contracts/AGENTS.md) -- an additive enum widening
stays v1 per that doc's own rule ("narrowed values...require a new major
version"; this is the opposite).

`queryPullRequestReviews`/`queryCIRuns` (`devhealthsource/tables.go`) reuse
the exact JOIN shape `internal/contextpacket/source_queries.go` already
uses for `pull_request_reviews.v1`/`ci_pipeline_runs.v1` rather than
inventing a new one -- both tables lack their own `org_id` column (per
that existing, already-production query, which scopes entirely through
the `repos` join), so W1's "`org_id` in every join" house rule has nothing
to add here that the column doesn't already lack elsewhere. A review
projects as an entity plus a `BELONGS_TO_PULL_REQUEST` relationship; a CI
run projects as an entity plus the existing `BELONGS_TO_REPOSITORY`
relationship. Neither table has a known soft-delete signal (unlike
`operational_incidents.is_deleted`), so neither tombstones, matching the
existing precedent for every other non-incident table.

Proven by `TestClickHouseProjectionSourceProjectsPullRequestReviewsAndCIRuns`,
confirmed to fail (no review/CI-run entities projected) against a
temporary removal from `entityTables` and pass again with it restored.

## Rulings (2026-08-12, team-lead)

1. **Postgres advisory lock: approved.** Cross-replica correctness beats a
   `replicas: 1` convention. Caveat, as directed: `pg_try_advisory_lock`'s
   two-int32 key is `(advisoryLockClassID, hashtext(orgID))` — a 32-bit
   `hashtext` collision between two *different* organizations' IDs would
   over-serialize them (they'd contend for the same lock slot and one would
   just wait/retry next tick) but can never *under*-serialize two orgs into
   running concurrently while believing they're isolated; it fails toward
   extra safety, not toward the race the amendment exists to prevent.
2. **Org selection: explicit allowlist, confirmed — no auto-discovery.**
   `ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS` stays the only source of the org
   set for this reset. **Named follow-up:** an entitlement- or
   `client_credentials`-driven auto-discovery mode, so an org doesn't need a
   manual allowlist edit to get projected — out of scope here because it
   would project every org with credentials regardless of whether that org
   opted into Context Fabric.
3. **`storage.EpisodeStore` extended directly — no parallel interface.**
   Implemented as described above: `ListSince` on the shared interface,
   both backends, with tests following each store's existing conventions
   (the memory store's injected-clock pattern for ordering/pagination
   tests; the postgres store's real-`testcontainers` integration tests,
   seeded through the real `CreateIdempotent`/`Redact` writers).
4. **`ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED`: approved and relayed to 3754.**

Migration-numbering caution: verified `origin/main`'s migration set was
still `0001`–`0005` immediately before implementation began (no other lane
had landed a new one), so `0006_context_fabric_projection_checkpoints.sql`
did not need renumbering.
