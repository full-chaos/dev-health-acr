-- The result's internal semantic snapshot: the accepted reading it was
-- produced under, persisted in the SAME row as the payload.
--
-- A window-only continuation continues the prior turn's whole validated
-- reading -- its accepted family, its normalized frame, the gate verdict on
-- that frame, the roles the frame offers, and the requirement declarations
-- planning consumed. The public payload carries none of that beyond the plan's
-- family and group axis, and both result schemas are closed, so the reading is
-- stored here, as server-only metadata beside the payload, the same choice
-- graph_epoch (0021) and parent_result_id (0037) made.
--
-- WRITTEN IN THE SAME INSERT AS THE PAYLOAD, never attached by a later UPDATE:
-- a row exists with its snapshot or does not exist, and a replay that would
-- change it is refused by the store.
--
-- NULLABLE with no default, and NULL means UNAVAILABLE: every row written
-- before this column, and every turn that ended before interpretation,
-- reads back with no snapshot. Nothing backfills it -- a reading reconstructed
-- after the fact from the payload is not the reading that was accepted.
--
-- The document's format, bounds and closed vocabularies are validated by the
-- application codec on write AND on read (it is the only writer, and a row that
-- fails the codec reads back unavailable). The database guarantees the shape
-- the codec keys on: an object that names its format. The key test is
-- explicit (`?`) because a CHECK passes on NULL, and `->` on a missing key
-- yields NULL: without it, `{}` would satisfy the constraint.
--
-- NOT VALID, as 0027 does: the constraint is enforced on every INSERT and
-- UPDATE from this point, and the one-time validating scan of existing rows is
-- skipped. Every existing row is NULL (the column was just added), so the scan
-- could prove nothing; it would only extend the ACCESS EXCLUSIVE window on a
-- table every turn writes.
--
-- No index: the snapshot is read by the table's existing (org_id, result_id)
-- point lookup and never searched.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration wraps
-- this file in its own transaction). Every statement is idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS semantic_state JSONB;

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_semantic_state_shape;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_semantic_state_shape
        CHECK (
            semantic_state IS NULL
            OR (jsonb_typeof(semantic_state) = 'object'
                AND semantic_state ? 'format_version'
                AND jsonb_typeof(semantic_state -> 'format_version') = 'string')
        )
        NOT VALID;
