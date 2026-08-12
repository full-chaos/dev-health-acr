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
WorkItem, PullRequest, Deployment, Incident entity kinds and the explicit
relationship projections (from the `work_graph_*` edge tables and
`work_item_dependencies`). `operational_incidents.is_deleted` is an existing
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
