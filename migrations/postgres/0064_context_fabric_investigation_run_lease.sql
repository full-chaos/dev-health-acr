-- CHAOS-11949 (Ask Dev plan §2.7.9, INV-7): an Ask Dev investigation runs under
-- an ACR-owned lease, with a Postgres event log, attach and cancel.
--
-- The lease lives on the submission record (migration 0055). A replica that
-- admits a streamed run with ACR_CONTEXT_FABRIC_RUN_LEASE_ENABLED on writes,
-- in the admission transaction:
--
--   lease_holder      the replica that runs it (its instance id);
--   lease_expires_at  when the lease ends unless the holder renews it;
--   run_request_id    the X-Request-ID of the run, the request_id of every
--                     event of its stream (a terminal written by another
--                     replica carries it too).
--
-- The holder renews the lease while the run lives. A running record whose
-- lease ended is taken over by any replica: one conditional UPDATE sets it
-- abandoned and names the taker as lease_holder, and the run_lost terminal is
-- written to the event log in the same transaction. Exactly one taker wins
-- (the row lock decides); the old holder's next renewal finds the record no
-- longer its own and cancels its run.
--
-- cancel_requested_at is t_req of an explicit cancel (the owner-checked
-- cancel route). The first request sets it; a repeat keeps it. The holder
-- reads it and cancels its run (t_eff, logged).
--
-- Records admitted with the flag off have no lease (all four NULL) and behave
-- exactly as before.
--
-- The event log, acr.context_fabric_investigation_run_events, holds one run's
-- stream: every non-terminal event (accepted, stage, heartbeat) as its data
-- payload, byte for byte, and ONE terminal marker by reference -- the final
-- result id, "unsaved", or the error.v1 envelope. No result content is ever
-- stored here (plan N2): an attach reads a result through the stored-result
-- gate. The log is bounded: the writer keeps the latest
-- ContextFabricInvestigationRunEventLogMaxEvents non-terminal events of a run
-- and never deletes the terminal. Its rows belong to the submission record
-- (foreign key on the reserved result id, ON DELETE CASCADE), so whatever
-- removes a record -- the retention job, a thread's backing-set removal, the
-- organization purge -- removes its log.
--
-- No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

ALTER TABLE acr.context_fabric_investigation_submissions
    ADD COLUMN IF NOT EXISTS lease_holder TEXT,
    ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS run_request_id TEXT,
    ADD COLUMN IF NOT EXISTS cancel_requested_at TIMESTAMPTZ;

ALTER TABLE acr.context_fabric_investigation_submissions
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_submissions_lease;
ALTER TABLE acr.context_fabric_investigation_submissions
    ADD CONSTRAINT ck_acr_cf_investigation_submissions_lease CHECK (
        (lease_holder IS NULL AND lease_expires_at IS NULL AND run_request_id IS NULL AND cancel_requested_at IS NULL)
        OR (char_length(lease_holder) BETWEEN 1 AND 200
            AND lease_expires_at IS NOT NULL
            AND char_length(run_request_id) BETWEEN 1 AND 128)
    );

-- The takeover reads running records whose lease ended.
CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_submissions_lease
    ON acr.context_fabric_investigation_submissions (lease_expires_at)
    WHERE state = 'running' AND lease_expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS acr.context_fabric_investigation_run_events (
    org_id              TEXT        NOT NULL,
    reserved_result_id  TEXT        NOT NULL,
    seq                 BIGINT      NOT NULL,
    event_type          TEXT        NOT NULL,
    -- The last stage started as of this event ('' when none started), so a
    -- terminal written later, by any replica, names it as the stream grammar
    -- requires.
    last_stage_started  TEXT        NOT NULL,
    -- The event's data payload (non-terminal events only), compact JSON,
    -- exactly as the stream wrote it. TEXT, not JSONB: replay is byte equal.
    data                TEXT,
    -- The terminal marker (terminal rows only).
    terminal_kind       TEXT,
    terminal_result_id  TEXT,
    terminal_error      TEXT,
    written_at          TIMESTAMPTZ NOT NULL,
    CONSTRAINT pk_acr_cf_investigation_run_events PRIMARY KEY (org_id, reserved_result_id, seq),
    CONSTRAINT fk_acr_cf_investigation_run_events_submission FOREIGN KEY (org_id, reserved_result_id)
        REFERENCES acr.context_fabric_investigation_submissions (org_id, reserved_result_id) ON DELETE CASCADE,
    CONSTRAINT ck_acr_cf_investigation_run_events_seq CHECK (seq BETWEEN 1 AND 9007199254740991),
    CONSTRAINT ck_acr_cf_investigation_run_events_type CHECK (event_type IN ('accepted', 'stage', 'heartbeat', 'result', 'error')),
    CONSTRAINT ck_acr_cf_investigation_run_events_stage CHECK (last_stage_started IN ('', 'interpreting', 'resolving_subjects', 'reading_facts', 'synthesizing', 'validating')),
    CONSTRAINT ck_acr_cf_investigation_run_events_shape CHECK (
        (event_type IN ('accepted', 'stage', 'heartbeat')
            AND data IS NOT NULL AND char_length(data) BETWEEN 2 AND 65536
            AND terminal_kind IS NULL AND terminal_result_id IS NULL AND terminal_error IS NULL)
        OR (event_type = 'result' AND data IS NULL AND terminal_error IS NULL
            AND ((terminal_kind = 'result' AND char_length(terminal_result_id) BETWEEN 8 AND 256)
              OR (terminal_kind = 'unsaved' AND terminal_result_id IS NULL)))
        OR (event_type = 'error' AND data IS NULL AND terminal_kind = 'error' AND terminal_result_id IS NULL
            AND char_length(terminal_error) BETWEEN 2 AND 16384)
    )
);

-- One terminal per run, whoever writes it (the holder, or the replica that
-- took over a lost run).
CREATE UNIQUE INDEX IF NOT EXISTS ux_acr_cf_investigation_run_events_terminal
    ON acr.context_fabric_investigation_run_events (org_id, reserved_result_id)
    WHERE terminal_kind IS NOT NULL;

-- The organization write fence (0052), on the new table.
DROP TRIGGER IF EXISTS refuse_purged_org_insert ON acr.context_fabric_investigation_run_events;
DROP TRIGGER IF EXISTS refuse_purged_org_update ON acr.context_fabric_investigation_run_events;
CREATE TRIGGER refuse_purged_org_insert BEFORE INSERT ON acr.context_fabric_investigation_run_events
    FOR EACH ROW EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
CREATE TRIGGER refuse_purged_org_update BEFORE UPDATE OF org_id ON acr.context_fabric_investigation_run_events
    FOR EACH ROW WHEN (NEW.org_id IS DISTINCT FROM OLD.org_id) EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();

COMMENT ON COLUMN acr.context_fabric_investigation_submissions.lease_holder IS
    'CHAOS-11949: the replica that holds the run lease, or that took over the lost run. NULL: no lease (connection-owned run).';
COMMENT ON COLUMN acr.context_fabric_investigation_submissions.lease_expires_at IS
    'CHAOS-11949: when the run lease ends unless renewed.';
COMMENT ON COLUMN acr.context_fabric_investigation_submissions.run_request_id IS
    'CHAOS-11949: the request id of the leased run, carried by every event of its stream.';
COMMENT ON COLUMN acr.context_fabric_investigation_submissions.cancel_requested_at IS
    'CHAOS-11949: t_req of the first explicit cancel of the run.';
COMMENT ON TABLE acr.context_fabric_investigation_run_events IS
    'CHAOS-11949: the bounded event log of a leased Ask Dev run; non-terminal events verbatim and one terminal marker by reference; no result content.';
