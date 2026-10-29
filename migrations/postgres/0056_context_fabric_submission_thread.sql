-- CHAOS-11913 (Ask Dev plan §2.7.5, §2.5): the thread a submission belongs to.
--
-- A client mints one thread id per conversation and sends it on every turn
-- (X-ACR-Thread-ID). Admission stores it on the submission record, so the
-- status read can name it and a later thread index (Phase 4) can group a
-- principal's submissions by (org_id, owner, thread_id). NULL: the submission
-- named no thread. Not part of the request digest: a retry of the key keeps
-- the first admission's thread.
--
-- NOT VALID, as 0044 does: every existing row is NULL. No index yet: the
-- Phase 4 thread index adds the one it needs. No inline BEGIN/COMMIT (the
-- runner wraps the file). Idempotent.

ALTER TABLE acr.context_fabric_investigation_submissions
    ADD COLUMN IF NOT EXISTS thread_id TEXT;

ALTER TABLE acr.context_fabric_investigation_submissions
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_submissions_thread_id_shape;
ALTER TABLE acr.context_fabric_investigation_submissions
    ADD CONSTRAINT ck_acr_cf_investigation_submissions_thread_id_shape
        CHECK (thread_id IS NULL OR thread_id ~ '^[A-Za-z0-9._:-]{8,128}$')
        NOT VALID;
