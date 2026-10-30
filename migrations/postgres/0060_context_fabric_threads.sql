-- CHAOS-11948 (Ask Dev plan §2.6, contract docs/design/ask-dev-conversation-contract.md):
-- the Ask Dev conversation (thread) store.
--
-- A thread is the submission records one owner admitted with one
-- X-ACR-Thread-ID (migration 0056). Its row is keyed like the records' legacy
-- owner, (org_id, owner_auth_method, owner_subject, thread_id), and carries the
-- canonical owner beside it (owner_user_id, owner_surface; CHAOS-12091,
-- migration 0059): a caller reaches a row when the legacy pair is the same OR
-- the canonical user and surface are the same, so a rotated credential of the
-- same user on the same surface reaches the user's thread, and the user's
-- browser and MCP surfaces never reach each other's (chris decision 5b). When
-- more than one row matches, the caller's own legacy pair wins, then the
-- earliest created. A turn records the legacy pair of the row it joined
-- (thread_owner_auth_method, thread_owner_subject on the submission; a record
-- without them belongs to the row of its own pair). This row holds what
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
    owner_user_id                   TEXT,
    owner_surface                   TEXT,
    CONSTRAINT pk_acr_cf_threads PRIMARY KEY (org_id, owner_auth_method, owner_subject, thread_id),
    CONSTRAINT ck_acr_cf_threads_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_threads_owner_method CHECK (owner_auth_method IN ('credential', 'web_assertion')),
    CONSTRAINT ck_acr_cf_threads_owner_subject_length CHECK (char_length(owner_subject) BETWEEN 1 AND 512),
    CONSTRAINT ck_acr_cf_threads_thread_id_shape CHECK (thread_id ~ '^[A-Za-z0-9._:-]{8,128}$'),
    CONSTRAINT ck_acr_cf_threads_turn_count CHECK (turn_count BETWEEN 1 AND 1000000),
    CONSTRAINT ck_acr_cf_threads_retention CHECK (retention_days IN (0, 30)),
    CONSTRAINT ck_acr_cf_threads_retention_set_by CHECK ((retention_set_by_admitted_at IS NULL) = (retention_set_by_submission_id IS NULL)),
    CONSTRAINT ck_acr_cf_threads_times CHECK (last_turn_at >= created_at AND expires_at > last_turn_at),
    CONSTRAINT ck_acr_cf_threads_canonical_owner CHECK (
        (owner_user_id IS NULL AND owner_surface IS NULL)
        OR (char_length(owner_user_id) BETWEEN 1 AND 200 AND owner_surface IN ('browser', 'mcp'))
    )
);

-- One thread per (organization, canonical owner, thread id), beside the
-- legacy primary key (as 0059 does for the submission records).
CREATE UNIQUE INDEX IF NOT EXISTS ux_acr_cf_threads_canonical_owner
    ON acr.context_fabric_threads (org_id, owner_user_id, owner_surface, thread_id)
    WHERE owner_user_id IS NOT NULL;

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

-- The legacy pair of the thread row a turn joined; NULL on a record admitted
-- before this column (its own pair names its row).
ALTER TABLE acr.context_fabric_investigation_submissions
    ADD COLUMN IF NOT EXISTS thread_owner_auth_method TEXT,
    ADD COLUMN IF NOT EXISTS thread_owner_subject TEXT;
ALTER TABLE acr.context_fabric_investigation_submissions
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_submissions_thread_owner;
ALTER TABLE acr.context_fabric_investigation_submissions
    ADD CONSTRAINT ck_acr_cf_investigation_submissions_thread_owner
        CHECK ((thread_owner_auth_method IS NULL) = (thread_owner_subject IS NULL)
               AND (thread_owner_auth_method IS NULL OR thread_id IS NOT NULL))
        NOT VALID;

-- A thread's turns, in turn order.
CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_submissions_thread
    ON acr.context_fabric_investigation_submissions (org_id, thread_id, admitted_at, submission_id)
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

-- Thread turns admitted before this table existed (thread_id on the
-- submission records since 0056) get their thread row, as AdvanceThread would
-- have built it: one row per legacy pair, first and latest turn, the count,
-- retention 0 (no earlier turn could name one), expiry 24 hours after the
-- latest turn. Without it their conversation would be unknown, and a later
-- turn would start the count at 1 beside turns it does not count. The row's
-- canonical owner is its records' (0059); when two legacy rows map to one
-- canonical thread (a rotated credential), only the earliest created takes
-- it and the others keep their legacy owner (as 0059 does). An id the 0056
-- check (NOT VALID) would refuse is left out. Idempotent: an existing row is
-- kept.
WITH grouped AS (
    SELECT org_id, owner_auth_method, owner_subject, thread_id,
           min(admitted_at) AS created_at, max(admitted_at) AS last_turn_at,
           LEAST(count(*), 1000000)::integer AS turn_count,
           CASE WHEN count(DISTINCT (owner_user_id, owner_surface)) = 1 AND bool_and(owner_user_id IS NOT NULL)
                THEN min(owner_user_id) END AS user_id,
           CASE WHEN count(DISTINCT (owner_user_id, owner_surface)) = 1 AND bool_and(owner_user_id IS NOT NULL)
                THEN min(owner_surface) END AS surface
      FROM acr.context_fabric_investigation_submissions
     WHERE thread_id IS NOT NULL AND thread_id ~ '^[A-Za-z0-9._:-]{8,128}$'
     GROUP BY org_id, owner_auth_method, owner_subject, thread_id
), ranked AS (
    SELECT grouped.*,
           CASE WHEN user_id IS NULL THEN 1
                ELSE row_number() OVER (PARTITION BY org_id, user_id, surface, thread_id
                                        ORDER BY created_at, owner_auth_method, owner_subject) END AS rank
      FROM grouped
)
INSERT INTO acr.context_fabric_threads
    (org_id, owner_auth_method, owner_subject, thread_id, created_at, last_turn_at, turn_count, retention_days, expires_at,
     owner_user_id, owner_surface)
SELECT org_id, owner_auth_method, owner_subject, thread_id, created_at, last_turn_at, turn_count, 0,
       last_turn_at + interval '24 hours',
       CASE WHEN rank = 1 THEN user_id END, CASE WHEN rank = 1 THEN surface END
  FROM ranked
    ON CONFLICT DO NOTHING;
