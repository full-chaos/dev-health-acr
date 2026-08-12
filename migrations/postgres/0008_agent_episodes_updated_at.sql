-- CHAOS-3753 codex finding C4: EpisodeStore.ListSince (internal/storage,
-- both Postgres and memory implementations) watermarked on created_at,
-- which never changes. A post-projection Redact() or PurgeExpired* state
-- transition therefore never re-crossed a checkpoint that had already
-- passed the row's created_at position, so revocations never reached the
-- graph -- the projection worker's tombstone conversion in
-- devhealthsource.episodeCandidate was correct, but ListSince never handed
-- it the changed row to convert.
--
-- updated_at is a genuine last-modification watermark: set equal to
-- created_at at insert time (same statement, same NOW() -- see
-- EpisodeStore.CreateIdempotent), and bumped to NOW() by both Redact() and
-- PurgeExpiredForPrincipal(). ListSince now orders and paginates on
-- (updated_at, episode_id) instead of (created_at, episode_id), so a state
-- change after the row was already projected once produces a fresh,
-- reachable watermark position.
--
-- Backfill note: rows purged (redaction_state = 'purged_tombstone') before
-- this migration have no historical modification timestamp to recover --
-- that absence is exactly what C4 identified. COALESCE(redacted_at,
-- created_at) is the best available signal for existing rows; only
-- prospectively (state transitions after this migration applies) does the
-- fix fully close the gap. This is a one-time, honestly-accepted backfill
-- limitation, not a design choice for new data.
ALTER TABLE acr.agent_episodes ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;

UPDATE acr.agent_episodes SET updated_at = COALESCE(redacted_at, created_at) WHERE updated_at IS NULL;

ALTER TABLE acr.agent_episodes ALTER COLUMN updated_at SET DEFAULT NOW();
ALTER TABLE acr.agent_episodes ALTER COLUMN updated_at SET NOT NULL;

CREATE INDEX IF NOT EXISTS ix_acr_agent_episodes_org_updated
    ON acr.agent_episodes (org_id, updated_at, episode_id);
