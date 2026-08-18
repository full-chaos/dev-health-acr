-- CHAOS-3898 S2a: the per-org graph lifecycle row (design brief v4.1 §3.1,
-- §3.5) plus the two tables it composes with: durable per-epoch retire
-- records (v4.1 F3 -- the fix for "rollback marked the newer epoch abandoned
-- but nothing could retire it") and per-(org, epoch, source) build
-- completion progress (§3.3/§5b cf_build_source_progress).
--
-- This migration ships NO behavior change: nothing yet reads or writes these
-- tables in production composition (S2a wires the CAS machinery and the
-- KeyResolver's byte-identical epoch-0 fallback; S2 performs the first real
-- flip). Every existing organization is absent from this table until its
-- first build begins, and absence is the correct "never migrated, serving
-- the legacy key" state -- see acr.context_fabric_graph_lifecycle's own
-- comment below.
--
-- No inline BEGIN/COMMIT: migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction (0015's convention).

-- One row per organization. Absence of a row means "never migrated": the
-- organization is served by the legacy, unsuffixed graph key
-- (falkorgraph.graphKey(prefix, orgID), epoch 0 by convention -- design
-- brief §3.1) with no build ever having been attempted. This is why
-- adopting the pointer requires zero data migration for an existing
-- organization: the KeyResolver's default answer for an absent row IS the
-- pre-CHAOS-3898 behavior.
--
-- CAS discipline (design brief §3.5): every lifecycle transition is
--   UPDATE acr.context_fabric_graph_lifecycle
--   SET ... WHERE org_id = $1 AND status = $expected AND active_epoch = $expected
-- so exactly one concurrent transition wins any race; the loser observes
-- zero rows affected and must re-read before retrying. (status, active_epoch)
-- together are the row's optimistic-concurrency version, not a separate
-- counter column -- both change on every transition this table's own state
-- machine defines (begin_build changes status only; flip/rollback/
-- begin_retire change both), so together they are always a fresh version
-- for the next racer to fail against.
CREATE TABLE IF NOT EXISTS acr.context_fabric_graph_lifecycle (
    org_id                TEXT PRIMARY KEY,
    -- The epoch currently SERVED to readers. 0 is the legacy, unsuffixed
    -- key; N>=1 is graphKey(prefix, orgID) + "-eN" (falkorgraph, S2a).
    active_epoch          BIGINT NOT NULL DEFAULT 0,
    -- The allocator (design brief P3/v4): monotonic, independent of
    -- active_epoch. begin_build always allocates last_allocated_epoch + 1
    -- and durably increments this counter -- never active_epoch + 1 -- so a
    -- rolled-back epoch's key/checkpoints/retire-record are never reused by
    -- a later build. A build/rollback/build cycle therefore always yields
    -- active_epoch+2 relative to where it started, never a repeat of +1.
    last_allocated_epoch  BIGINT NOT NULL DEFAULT 0,
    -- serving | building | grace. See this migration's header comment and
    -- pglifecycle's doc comment for the full transition table; "retiring"
    -- is deliberately NOT a status here -- epoch disposal after grace is
    -- tracked entirely by context_fabric_graph_epoch_retirements below, so
    -- the org returns to 'serving' the instant grace ends (by rollback OR
    -- by begin_retire), never blocking ordinary operation on teardown.
    status                TEXT NOT NULL DEFAULT 'serving'
        CHECK (status IN ('serving', 'building', 'grace')),
    -- Set by begin_build, cleared by flip. The epoch a build is currently
    -- targeting; NULL whenever status != 'building'.
    target_epoch          BIGINT,
    -- Set by flip (copied from the pre-flip active_epoch), cleared by
    -- rollback/begin_retire. The OLD epoch still retained during grace,
    -- pending either a rollback (restores it) or a begin_retire (retires
    -- it). NULL whenever status != 'grace'.
    grace_epoch           BIGINT,
    -- Frozen by begin_build from the coordinator's configured source set;
    -- read by the flip gate to decide whether EVERY required source has
    -- reported completion (context_fabric_graph_build_source_progress
    -- below). NULL whenever status != 'building'. JSONB (a JSON array of
    -- strings), matching this repository's existing convention for a
    -- stored string list (internal/storage/postgres's device
    -- authorization scopes/hints) rather than a native Postgres TEXT[] --
    -- the pgx stdlib driver this repository uses does not support
    -- scanning a TEXT[] column back into a Go []string through
    -- database/sql's generic Scan.
    required_sources      JSONB,
    -- Set by flip (now + the operator-configured grace window, design
    -- brief D11), cleared by rollback/begin_retire. NULL whenever
    -- status != 'grace'.
    grace_deadline         TIMESTAMPTZ,
    updated_at             TIMESTAMPTZ NOT NULL,
    CONSTRAINT ck_acr_cf_graph_lifecycle_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_graph_lifecycle_epochs_nonneg CHECK (active_epoch >= 0 AND last_allocated_epoch >= 0 AND active_epoch <= last_allocated_epoch),
    CONSTRAINT ck_acr_cf_graph_lifecycle_target_epoch CHECK (
        (status = 'building' AND target_epoch IS NOT NULL AND target_epoch > active_epoch)
        OR (status != 'building' AND target_epoch IS NULL)
    ),
    CONSTRAINT ck_acr_cf_graph_lifecycle_grace_epoch CHECK (
        (status = 'grace' AND grace_epoch IS NOT NULL AND grace_deadline IS NOT NULL AND grace_epoch <> active_epoch)
        OR (status != 'grace' AND grace_epoch IS NULL AND grace_deadline IS NULL)
    )
);

-- Durable per-epoch retire records (v4.1 F3). Every non-serving,
-- non-currently-building epoch has EXACTLY one path to deletion through a
-- row here, created either by begin_retire (reason=grace_expired, the OLD
-- epoch a normal flip left behind) or by rollback (reason=rollback_abandoned,
-- the NEWER epoch the rollback just abandoned) -- so repeated build/rollback
-- cycles cannot accumulate undeletable graphs or checkpoint sets (the v4
-- residual this fixes). State transitions on a row here are their own CAS
-- (draining -> deleting -> deleted); the retire executor is the only writer
-- that ever moves state to 'deleted', and it is also the only caller
-- permitted to issue GRAPH.DELETE for the epoch's key.
CREATE TABLE IF NOT EXISTS acr.context_fabric_graph_epoch_retirements (
    org_id       TEXT NOT NULL,
    epoch        BIGINT NOT NULL,
    reason       TEXT NOT NULL CHECK (reason IN ('grace_expired', 'rollback_abandoned')),
    -- Drain clock start: begin_retire time for grace_expired, the ROLLBACK
    -- time for rollback_abandoned (design brief v4.1 F3 -- NOT flip time).
    drain_start  TIMESTAMPTZ NOT NULL,
    state        TEXT NOT NULL DEFAULT 'draining' CHECK (state IN ('draining', 'deleting', 'deleted')),
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, epoch),
    CONSTRAINT ck_acr_cf_graph_epoch_retirements_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_graph_epoch_retirements_epoch_nonneg CHECK (epoch >= 0)
);

CREATE INDEX IF NOT EXISTS ix_acr_cf_graph_epoch_retirements_draining
    ON acr.context_fabric_graph_epoch_retirements (drain_start)
    WHERE state = 'draining';

-- Per-(org, epoch, source) build completion progress (design brief §3.3,
-- item 5, §5b cf_build_source_progress): the four completion shapes a
-- source reports (paged_final, empty_first_tick, disabled_at_freeze,
-- cursor_exhausted) plus the "pending" default a required source starts in
-- when begin_build freezes required_sources. The flip gate (pglifecycle)
-- requires every row named in the lifecycle row's required_sources to be
-- present here for the CURRENT target_epoch with a completion_mode other
-- than 'pending' before it will CAS status building -> grace; a source that
-- cannot report exhaustion stays 'pending' forever, which is the deliberate
-- fail-closed behavior (design brief §9 item 3: "MUST return to review", not
-- silently treated as complete).
CREATE TABLE IF NOT EXISTS acr.context_fabric_graph_build_source_progress (
    org_id           TEXT NOT NULL,
    epoch            BIGINT NOT NULL,
    source           TEXT NOT NULL,
    completion_mode  TEXT NOT NULL DEFAULT 'pending'
        CHECK (completion_mode IN ('pending', 'paged_final', 'empty_first_tick', 'disabled_at_freeze', 'cursor_exhausted')),
    rows_projected   BIGINT NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, epoch, source),
    CONSTRAINT ck_acr_cf_graph_build_source_progress_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_graph_build_source_progress_source_length CHECK (char_length(source) BETWEEN 1 AND 128),
    CONSTRAINT ck_acr_cf_graph_build_source_progress_rows_nonneg CHECK (rows_projected >= 0)
);
