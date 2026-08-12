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

-- CHAOS-3753 codex round-2 findings K5/K6 (amended into this migration
-- while still unreleased, per ruling -- not a separate 0009).
--
-- K5: an application-side "SET updated_at = NOW()" is not guaranteed
-- strictly monotonic across two writes to the SAME row. Two transitions
-- landing in the same wall-clock instant (e.g. Redact() immediately
-- followed by PurgeExpiredForPrincipal()) could produce equal updated_at
-- values, and ListSince's strict "(updated_at > since) OR (updated_at =
-- since AND episode_id > after)" predicate can only ever surface ONE of
-- two same-timestamp transitions on a single-row table (episode_id is
-- unchanged between them, so it never satisfies the tiebreaker) -- the
-- second transition would be silently invisible forever, the same failure
-- shape C4 fixed for CreatedAt, reintroduced one layer down.
--
-- K6: an application-side bump only protects callers that remember to set
-- it. A coexisting older binary (a rolling deploy, or this migration
-- applied ahead of the code that knows about updated_at) issuing the
-- pre-C4 UPDATE shape -- setting redaction_state without touching
-- updated_at at all -- would leave that transition permanently invisible
-- to ListSince too, regardless of which binary wrote it.
--
-- A single trigger closes both at the source, independent of the writer:
-- on ANY UPDATE to acr.agent_episodes, unconditionally set updated_at to
-- a value strictly greater than the row's previous updated_at.
-- clock_timestamp() (not now(), which is fixed for an entire transaction
-- and would not advance between two updates in one transaction) combined
-- with GREATEST(..., OLD.updated_at + smallest representable increment)
-- guarantees strict monotonicity even when the wall clock has not visibly
-- ticked between two writes, or has even moved backward. This makes
-- updated_at's monotonicity a property of the table, not of any
-- particular writer's care in setting it -- EpisodeStore's own explicit
-- "updated_at = NOW()" in Redact()/PurgeExpiredForPrincipal() is kept
-- (harmlessly redundant with the trigger, not removed) as defense in
-- depth: it costs nothing and keeps the intent visible at the call site.
CREATE OR REPLACE FUNCTION acr.agent_episodes_bump_updated_at() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + INTERVAL '1 microsecond');
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_agent_episodes_bump_updated_at ON acr.agent_episodes;
CREATE TRIGGER trg_agent_episodes_bump_updated_at
    BEFORE UPDATE ON acr.agent_episodes
    FOR EACH ROW
    EXECUTE FUNCTION acr.agent_episodes_bump_updated_at();
