-- CHAOS-11950 (Ask Dev plan item I-32a, contract
-- docs/design/ask-dev-conversation-contract.md §10): Ask Dev answer feedback.
--
-- One row per result: whether the result's owner found it correct and useful
-- ('yes', 'no' or 'unset'), and when that was last set. No free text. The
-- feedback routes reach a row only through its result row
-- (acr.context_fabric_investigation_results): the caller's organization, the
-- result's submission owner (ownerMatchSQL over submission_owner_*), and a
-- result no closed thread alone names (design §6 T6).
--
-- The row is in the result's backing set (plan §2.6, D-22): the foreign key
-- removes it with its result (the retention job, I-30, and any other delete
-- of a result row), and the organization purge lists it before the results.
--
-- No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

CREATE TABLE IF NOT EXISTS acr.context_fabric_result_feedback (
    result_id   TEXT        NOT NULL,
    org_id      TEXT        NOT NULL,
    correct     TEXT        NOT NULL,
    useful      TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    CONSTRAINT pk_acr_cf_result_feedback PRIMARY KEY (result_id),
    CONSTRAINT fk_acr_cf_result_feedback_result FOREIGN KEY (result_id)
        REFERENCES acr.context_fabric_investigation_results (result_id) ON DELETE CASCADE,
    CONSTRAINT ck_acr_cf_result_feedback_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_result_feedback_correct CHECK (correct IN ('yes', 'no', 'unset')),
    CONSTRAINT ck_acr_cf_result_feedback_useful CHECK (useful IN ('yes', 'no', 'unset')),
    CONSTRAINT ck_acr_cf_result_feedback_times CHECK (updated_at >= created_at)
);

-- The organization purge deletes by org_id.
CREATE INDEX IF NOT EXISTS ix_acr_cf_result_feedback_org
    ON acr.context_fabric_result_feedback (org_id);

-- The organization write fence (0052), on the new table.
DROP TRIGGER IF EXISTS refuse_purged_org_insert ON acr.context_fabric_result_feedback;
DROP TRIGGER IF EXISTS refuse_purged_org_update ON acr.context_fabric_result_feedback;
CREATE TRIGGER refuse_purged_org_insert BEFORE INSERT ON acr.context_fabric_result_feedback
    FOR EACH ROW EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
CREATE TRIGGER refuse_purged_org_update BEFORE UPDATE OF org_id ON acr.context_fabric_result_feedback
    FOR EACH ROW WHEN (NEW.org_id IS DISTINCT FROM OLD.org_id) EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
