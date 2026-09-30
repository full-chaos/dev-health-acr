-- 0045 (CHAOS-7249): index the OAuth purge's idle-client predicate.
--
-- The purge loop (internal/storage/postgres/oauth_purge.go) selects idle
-- dynamic clients with `created_at < now - idle ORDER BY created_at, client_id
-- LIMIT n`, and its remaining-eligible probe counts the same rows with a
-- LIMIT. Without an index on created_at both read every row of
-- acr.oauth_clients on every tick (a LIMIT caps the rows returned, not the rows
-- examined), and unauthenticated dynamic client registration is what grows that
-- table. With it they read only rows older than the idle window, oldest first,
-- and stop at the batch limit, so registrations inside the window are never
-- read at all.
--
-- acr.oauth_authorization_requests is already indexed for its purge predicate
-- (ix_acr_oauth_authorization_requests_expiry, migration 0040).
--
-- A plain CREATE INDEX takes a SHARE lock on acr.oauth_clients for its build,
-- which blocks registrations for that time; the table holds one small row per
-- registered client, so the build is milliseconds.
CREATE INDEX IF NOT EXISTS ix_acr_oauth_clients_created
    ON acr.oauth_clients (created_at, client_id);
