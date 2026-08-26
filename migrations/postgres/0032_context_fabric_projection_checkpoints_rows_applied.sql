-- CHAOS-4305: give the projection checkpoint its own durable, monotonic
-- rows-applied counter, written in the SAME UPDATE/INSERT statement that
-- already advances the cursor (pgprojection.CheckpointStore's
-- CompareAndSwapProjectionCheckpoint/...ForEpoch).
--
-- Why: runBuildPair (projectionrun/coordinator.go) previously seeded each
-- tick's row-count accumulator (`priorRows`) from
-- cf_build_source_progress.rows_projected -- a SEPARATE table, written by a
-- SEPARATE statement (pglifecycle.Store.RecordSourceProgress), AFTER the
-- checkpoint has already durably advanced. If RecordSourceProgress fails
-- for the rest of a drain (including the finalizing retry after the loop),
-- the checkpoint has still moved past the unrecorded batches -- so the next
-- tick's priorRows read the last successfully-written (now stale) total and
-- could never recover the lost count: a permanent undercount of genuinely
-- applied rows (CHAOS-4305).
--
-- rows_applied closes this by riding in the checkpoint's OWN CAS statement:
-- it cannot diverge from the cursor it travels with, so a reader (RunOnce,
-- via ProjectionRun.RowsApplied) always sees the true cumulative count
-- regardless of whether cf_build_source_progress's own write ever succeeds.
-- cf_build_source_progress.rows_projected is unchanged and still written as
-- before -- it remains the completion-mode-adjacent display value: only
-- FUTURE-tick correctness (the actual undercount risk) now derives from
-- this column instead.
--
-- BIGINT NOT NULL DEFAULT 0: every existing row starts at 0 by default,
-- matching what a genuinely fresh checkpoint's accumulator would read. The
-- legacy epoch-0 path gains rows_applied bookkeeping too (RunOnce is one
-- shared code path for both the steady-state and build paths) but nothing
-- reads it there today -- inert, not a behavior change.
--
-- Codex review caught a real rollout hazard the DEFAULT 0 alone leaves open:
-- an ALREADY-OPEN build epoch's checkpoint rows can have a non-empty cursor
-- (rows genuinely applied under the pre-CHAOS-4305 binary) with no
-- rows_applied history at all, since the column didn't exist yet. Left at 0,
-- runBuildPair's new checkpoint-derived `total` would UNDERCOUNT that
-- in-flight build's remaining ticks -- reintroducing, one time, during THIS
-- migration's own rollout window, exactly the kind of undercount this
-- ticket exists to close (display-only: Flip is unaffected either way, it
-- never reads rows_projected/rows_applied). The backfill below closes that
-- window: cf_build_source_progress and context_fabric_projection_checkpoints
-- share the identical (org_id, epoch, source) key (both re-keyed together
-- in migration 0020), so every open build-epoch checkpoint row recovers
-- whatever total the pre-migration binary had last durably recorded there --
-- the best available truth, strictly no worse than the write it replaces,
-- and an exact match for a build with no RecordSourceProgress failures in
-- its history. Legacy epoch-0 rows have no cf_build_source_progress
-- counterpart (runPair never calls RecordSourceProgress) and are left at 0,
-- consistent with "nothing reads it there today".
--
-- No inline BEGIN/COMMIT: migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction.

ALTER TABLE acr.context_fabric_projection_checkpoints
    ADD COLUMN IF NOT EXISTS rows_applied BIGINT NOT NULL DEFAULT 0;

UPDATE acr.context_fabric_projection_checkpoints AS cp
SET rows_applied = bsp.rows_projected
FROM acr.context_fabric_graph_build_source_progress AS bsp
WHERE cp.org_id = bsp.org_id AND cp.epoch = bsp.epoch AND cp.source = bsp.source
  AND cp.rows_applied = 0 AND bsp.rows_projected > 0;

ALTER TABLE acr.context_fabric_projection_checkpoints
    DROP CONSTRAINT IF EXISTS ck_acr_cf_projection_checkpoints_rows_applied_nonneg;
ALTER TABLE acr.context_fabric_projection_checkpoints
    ADD CONSTRAINT ck_acr_cf_projection_checkpoints_rows_applied_nonneg CHECK (rows_applied >= 0);
