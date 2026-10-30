-- CHAOS-11948 (Ask Dev plan §2.6, contract docs/design/ask-dev-conversation-contract.md):
-- the Ask Dev conversation (thread) store.
--
-- A thread is the submission records one owner admitted with one
-- X-ACR-Thread-ID (migration 0056), keyed exactly like the records' owner:
-- (org_id, owner_auth_method, owner_subject, thread_id). This row holds what
-- the turns do not: the first and latest turn times, the turn count, the
-- retention the latest naming turn set (and which turn set it), the expiry
-- (last_turn_at + 24 h for retention 0, + 30 days for 30), and the closure a
-- delete records. Admission writes it in the submission's own transaction,
-- under the organization's admission lock, so the count and the times never
-- drift from the turns.
--
-- A thread is closed when closed_at is set (deleted) or expires_at has
-- passed. A closed thread is not served, a new turn naming it is refused
-- (409 thread_closed), and a result only closed turns name is not served.
-- The row is content-free (no question, no answer) and stays until the
-- retention job removes the backing set after expires_at (design §6 T1).
--
-- question: the question a thread turn was admitted with (the caller's own
-- words), on thread turns only; the delete sets it to NULL at once.
--
-- No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

CREATE TABLE IF NOT EXISTS acr.context_fabric_threads (
    org_id                          TEXT        NOT NULL,
    owner_auth_method               TEXT        NOT NULL,
    owner_subject                   TEXT        NOT NULL,
    thread_id                       TEXT        NOT NULL,
    created_at                      TIMESTAMPTZ NOT NULL,
    last_turn_at                    TIMESTAMPTZ NOT NULL,
    turn_count                      INTEGER     NOT NULL,
    retention_days                  SMALLINT    NOT NULL,
    retention_set_by_admitted_at    TIMESTAMPTZ,
    retention_set_by_submission_id  TEXT,
    expires_at                      TIMESTAMPTZ NOT NULL,
    closed_at                       TIMESTAMPTZ,
    CONSTRAINT pk_acr_cf_threads PRIMARY KEY (org_id, owner_auth_method, owner_subject, thread_id),
    CONSTRAINT ck_acr_cf_threads_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_threads_owner_method CHECK (owner_auth_method IN ('credential', 'web_assertion')),
    CONSTRAINT ck_acr_cf_threads_owner_subject_length CHECK (char_length(owner_subject) BETWEEN 1 AND 512),
    CONSTRAINT ck_acr_cf_threads_thread_id_shape CHECK (thread_id ~ '^[A-Za-z0-9._:-]{8,128}$'),
    CONSTRAINT ck_acr_cf_threads_turn_count CHECK (turn_count BETWEEN 1 AND 1000000),
    CONSTRAINT ck_acr_cf_threads_retention CHECK (retention_days IN (0, 30)),
    CONSTRAINT ck_acr_cf_threads_retention_set_by CHECK ((retention_set_by_admitted_at IS NULL) = (retention_set_by_submission_id IS NULL)),
    CONSTRAINT ck_acr_cf_threads_times CHECK (last_turn_at >= created_at AND expires_at > last_turn_at)
);

-- The thread list: one owner's threads, newest last turn first.
CREATE INDEX IF NOT EXISTS ix_acr_cf_threads_owner_last_turn
    ON acr.context_fabric_threads (org_id, owner_auth_method, owner_subject, last_turn_at DESC, thread_id);

-- The retention job reads threads past their expiry.
CREATE INDEX IF NOT EXISTS ix_acr_cf_threads_expires_at
    ON acr.context_fabric_threads (expires_at);

ALTER TABLE acr.context_fabric_investigation_submissions
    ADD COLUMN IF NOT EXISTS question TEXT;

ALTER TABLE acr.context_fabric_investigation_submissions
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_submissions_question;
ALTER TABLE acr.context_fabric_investigation_submissions
    ADD CONSTRAINT ck_acr_cf_investigation_submissions_question
        CHECK (question IS NULL OR (thread_id IS NOT NULL AND char_length(question) BETWEEN 1 AND 8000))
        NOT VALID;

-- A thread's turns, in turn order.
CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_submissions_thread
    ON acr.context_fabric_investigation_submissions (org_id, owner_auth_method, owner_subject, thread_id, admitted_at, submission_id)
    WHERE thread_id IS NOT NULL;

-- The closed-thread rule reads the records that name a result as their final
-- result (the reserved id has its own unique index, 0055).
CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_submissions_result
    ON acr.context_fabric_investigation_submissions (org_id, result_id)
    WHERE result_id IS NOT NULL;

-- The organization write fence (0052), on the new table.
DROP TRIGGER IF EXISTS refuse_purged_org_insert ON acr.context_fabric_threads;
DROP TRIGGER IF EXISTS refuse_purged_org_update ON acr.context_fabric_threads;
CREATE TRIGGER refuse_purged_org_insert BEFORE INSERT ON acr.context_fabric_threads
    FOR EACH ROW EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
CREATE TRIGGER refuse_purged_org_update BEFORE UPDATE OF org_id ON acr.context_fabric_threads
    FOR EACH ROW WHEN (NEW.org_id IS DISTINCT FROM OLD.org_id) EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
