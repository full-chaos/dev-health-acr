-- Durable "rebuild owed" flag on the projection checkpoint. A source refused
-- for a version mismatch owes a rebuild; the flag used to live only in the
-- projector process, so a restart forgot it and a dormant source with a stale
-- version showed up nowhere until another refusal.
--
-- The flag rides in the row it describes, keyed (org_id, epoch, source). The
-- checkpoint CAS that advances the cursor also writes rebuild_owed = false, so
-- an applied batch clears it in the same statement.
--
-- Additive: NOT NULL DEFAULT false leaves every existing row as "nothing owed".
-- A binary that predates this column ignores it.
--
-- No inline BEGIN/COMMIT: migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction.

ALTER TABLE acr.context_fabric_projection_checkpoints
    ADD COLUMN IF NOT EXISTS rebuild_owed BOOLEAN NOT NULL DEFAULT false;
