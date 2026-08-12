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

**No canonical source exists yet in this repo for Team, Project, or
Decision/Document entities** — no ClickHouse table, no other adapter. Per
the handoff, I am not inventing one. `devhealthsource` defines the seam
(`TeamProjectSource`, doc-commented contract) and ships a stub that returns
zero rows, gated off by `ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED`
(default `false`). Enabling it is follow-up work once Dev Health Ops
publishes a canonical table/API for those kinds.

Approved episodes are **not** ClickHouse data — they're ACR's own
`acr.agent_episodes` (Postgres, `internal/storage.EpisodeStore`). That
interface has no incremental-scan method today (only point lookups), so I
need to add one narrow read method (`ListApprovedSince(ctx, orgID, cursor,
limit)` or similar) — this touches `internal/storage`, nominally owned by
the security/persistence lane. Flagging for awareness rather than blocking on
it; happy to route it through review if you'd rather own that edit.

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

## Open decisions to veto/confirm

1. Postgres advisory lock for cross-replica per-org single-flight, vs.
   in-process-only + `replicas: 1`.
2. Explicit org allowlist env var vs. auto-discovery from
   `client_credentials`.
3. Adding one narrow incremental-read method to `storage.EpisodeStore`
   (or a parallel projection-only interface instead, to avoid touching the
   shared interface at all).
4. `ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED` as the reserved name for the
   3754/3755 read-side flag.

Proceeding with implementation on the above defaults unless you say
otherwise.
