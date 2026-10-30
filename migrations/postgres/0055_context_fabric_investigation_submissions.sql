-- CHAOS-11912 (Ask Dev plan §2.7.5, INV-1, INV-2): one submit, one
-- execution, bound to its owner.
--
-- A request that names X-ACR-Submission-ID is admitted by inserting one row
-- here, keyed (org_id, owner, submission_id). The INSERT comes first and the
-- primary key decides every race, on every replica: exactly one concurrent
-- admission of a key inserts; the others read the row it wrote and never
-- execute. The row is bound to the request's semantic digest, so a key can
-- only retry the identical request.
--
-- States: running (the run is live), done (result_id names the final
-- result), done_unsaved (an answer was served and not stored), abandoned (the
-- run ended with no result, or ran past expires_at = budget + 30 s). No state
-- lets the key execute again.
--
-- reserved_result_id is the result id the run saves under. The result
-- store's Save completes the running (or abandoned) row whose
-- reserved_result_id is the saved id and whose owner is the saving
-- principal IN THE SAME STATEMENT as the result insert, and records the owner
-- on the result row (submission_owner_*). A result with an owner is served
-- to that owner only (StoredResultGate.Authorize); a result with none keeps
-- the organization-bearer semantics of a result id.
--
-- org_id is TEXT like the results table's. No FK to the results table: a
-- record exists before its result, and a done_unsaved or abandoned record
-- never has one. The organization write fence (0052) covers the new table,
-- and the organization purge deletes it (orgpurge purgeTables).
--
-- No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

CREATE TABLE IF NOT EXISTS acr.context_fabric_investigation_submissions (
    org_id             TEXT        NOT NULL,
    owner_auth_method  TEXT        NOT NULL,
    owner_subject      TEXT        NOT NULL,
    submission_id      TEXT        NOT NULL,
    request_digest     TEXT        NOT NULL,
    reserved_result_id TEXT        NOT NULL,
    state              TEXT        NOT NULL,
    result_id          TEXT,
    admitted_at        TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL,
    expires_at         TIMESTAMPTZ NOT NULL,
    CONSTRAINT pk_acr_cf_investigation_submissions PRIMARY KEY (org_id, owner_auth_method, owner_subject, submission_id),
    CONSTRAINT ck_acr_cf_investigation_submissions_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_investigation_submissions_owner_method CHECK (owner_auth_method IN ('credential', 'web_assertion')),
    CONSTRAINT ck_acr_cf_investigation_submissions_owner_subject_length CHECK (char_length(owner_subject) BETWEEN 1 AND 512),
    CONSTRAINT ck_acr_cf_investigation_submissions_submission_id_shape CHECK (submission_id ~ '^[A-Za-z0-9._:-]{8,128}$'),
    CONSTRAINT ck_acr_cf_investigation_submissions_digest_shape CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT ck_acr_cf_investigation_submissions_reserved_length CHECK (char_length(reserved_result_id) BETWEEN 8 AND 256),
    CONSTRAINT ck_acr_cf_investigation_submissions_state CHECK (state IN ('running', 'done', 'done_unsaved', 'abandoned')),
    CONSTRAINT ck_acr_cf_investigation_submissions_result CHECK ((state = 'done') = (result_id IS NOT NULL)),
    CONSTRAINT ck_acr_cf_investigation_submissions_expiry CHECK (expires_at > admitted_at)
);

-- The reserved id is unique per organization: Save finds the record it
-- completes through this index.
CREATE UNIQUE INDEX IF NOT EXISTS ux_acr_cf_investigation_submissions_reserved
    ON acr.context_fabric_investigation_submissions (org_id, reserved_result_id);

-- The sweeper reads running records only.
CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_submissions_running
    ON acr.context_fabric_investigation_submissions (expires_at)
    WHERE state = 'running';

-- The owner a result's submission recorded. Both NULL: the result completed
-- no submission. NOT VALID, as 0044 does: every existing row is NULL.
ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS submission_owner_auth_method TEXT,
    ADD COLUMN IF NOT EXISTS submission_owner_subject TEXT;

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_submission_owner_pair;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_submission_owner_pair
        CHECK ((submission_owner_auth_method IS NULL) = (submission_owner_subject IS NULL))
        NOT VALID;

-- The organization write fence (0052), on the new table.
DROP TRIGGER IF EXISTS refuse_purged_org_insert ON acr.context_fabric_investigation_submissions;
DROP TRIGGER IF EXISTS refuse_purged_org_update ON acr.context_fabric_investigation_submissions;
CREATE TRIGGER refuse_purged_org_insert BEFORE INSERT ON acr.context_fabric_investigation_submissions
    FOR EACH ROW EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
CREATE TRIGGER refuse_purged_org_update BEFORE UPDATE OF org_id ON acr.context_fabric_investigation_submissions
    FOR EACH ROW WHEN (NEW.org_id IS DISTINCT FROM OLD.org_id) EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
