-- CHAOS-3898 S2a (design brief §3.4, P3): re-key projection checkpoints from
-- (org_id, source) to (org_id, epoch, source). One shared cursor set per
-- (org, source) meant a pointer-only rollback retained the abandoned
-- epoch's cursor, permanently skipping every row that arrived during the
-- abandoned epoch's serving window once the org rolled back -- this closes
-- that by making a checkpoint describe exactly one graph.
--
-- epoch BIGINT NOT NULL DEFAULT 0: every EXISTING row adopts epoch 0 (the
-- legacy, unsuffixed graph key) automatically -- zero data migration, zero
-- behavior change. pgprojection.CheckpointStore's existing
-- LoadProjectionCheckpoint/CompareAndSwapProjectionCheckpoint continue to
-- operate at epoch 0 exactly as before; nothing in this migration requires
-- any caller to change.
--
-- No inline BEGIN/COMMIT: migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction.

ALTER TABLE acr.context_fabric_projection_checkpoints
    ADD COLUMN IF NOT EXISTS epoch BIGINT NOT NULL DEFAULT 0;

ALTER TABLE acr.context_fabric_projection_checkpoints
    ADD CONSTRAINT ck_acr_cf_projection_checkpoints_epoch_nonneg CHECK (epoch >= 0);

-- Drop the (org_id, source) primary key and replace it with
-- (org_id, epoch, source). Postgres names an inline "PRIMARY KEY (...)"
-- constraint "<table>_pkey" by default (0006's own declaration was
-- unnamed), so the generated name is exactly this.
ALTER TABLE acr.context_fabric_projection_checkpoints
    DROP CONSTRAINT IF EXISTS context_fabric_projection_checkpoints_pkey;

ALTER TABLE acr.context_fabric_projection_checkpoints
    ADD CONSTRAINT context_fabric_projection_checkpoints_pkey PRIMARY KEY (org_id, epoch, source);
