-- 0046 (CHAOS-7229): index the OAuth purge's device authorization predicate.
--
-- The purge loop (internal/storage/postgres/oauth_purge.go) selects device
-- authorizations in ANY state with `expires_at < now - grace ORDER BY expires_at,
-- device_code_hash LIMIT n`, and its remaining-eligible probe counts the same
-- rows with a LIMIT. The only existing index on expires_at,
-- ix_acr_device_authorizations_expiry (migration 0004), is partial (pending and
-- approved rows), so it cannot serve a predicate that also purges denied,
-- expired and redeemed rows; without this index both read every row of the
-- table on every tick. A LIMIT caps the rows a statement returns, not the rows
-- it reads.
--
-- A plain CREATE INDEX takes a SHARE lock on acr.device_authorizations for its
-- build, which blocks device authorization writes (sign-in starts, approvals,
-- redemptions) for that time; the table holds one small row per sign-in
-- attempt within the purge grace, so the build is milliseconds.
CREATE INDEX IF NOT EXISTS ix_acr_device_authorizations_expires_all
    ON acr.device_authorizations (expires_at, device_code_hash);
