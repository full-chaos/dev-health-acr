-- CHAOS-12320: a dynamic (RFC 7591) client that completed a grant is never
-- purged. last_granted_at is the time of its latest token exchange, written in
-- the same transaction as the credential redeem
-- (internal/storage/postgres/device_authorization_redeem.go). The idle-client
-- purge (internal/storage/postgres/oauth_purge.go) deletes only rows where it
-- is NULL: a registration no signed-in user ever consented to, so the
-- CHAOS-6191 bound on unauthenticated registrations stands.
--
-- Why: a dynamic client's credential lives 30 days with no refresh, so the next
-- sign-in reuses the client_id the MCP client stored. The purge used to delete
-- the client minutes after that credential ended, and the sign-in then failed
-- with unknown_client.
--
-- Backfill: every client with a surviving OAuth credential (acr.client_credentials
-- keeps oauth_user rows; only browser and workload rows are ever deleted) or a
-- surviving redeemed device authorization reached through its request or
-- device grant. A client already purged has nothing to backfill and registers
-- again.
--
-- Runtime grant: none new. The runtime role already holds UPDATE on
-- acr.oauth_clients (internal/dbacl/runtime_grants.go).
--
-- lock_timeout: ADD COLUMN takes an ACCESS EXCLUSIVE lock on acr.oauth_clients;
-- past 5s behind a long holder the migration fails (SQLSTATE 55P03) and the roll
-- can retry. SET LOCAL scopes it to this file's transaction (the runner applies
-- each file in one transaction, runner.go applyMigration). The column is
-- nullable with no default, so the ALTER rewrites nothing.
SET LOCAL lock_timeout = '5s';

ALTER TABLE acr.oauth_clients
    ADD COLUMN IF NOT EXISTS last_granted_at TIMESTAMPTZ;

COMMENT ON COLUMN acr.oauth_clients.last_granted_at IS
    'Time of the client''s latest completed token exchange. NULL = never granted; only such a client is purged when idle (CHAOS-12320).';

UPDATE acr.oauth_clients AS c
SET last_granted_at = granted.at
FROM (
    SELECT source.client_id, max(source.at) AS at
    FROM (
    SELECT cc.client_id, max(cc.created_at) AS at
    FROM acr.client_credentials cc
    WHERE cc.client_id IS NOT NULL
    GROUP BY cc.client_id
    UNION ALL
    SELECT r.client_id, max(d.redeemed_at)
    FROM acr.oauth_authorization_requests r
    JOIN acr.device_authorizations d ON d.device_code_hash = r.device_code_hash
    WHERE d.redeemed_at IS NOT NULL
    GROUP BY r.client_id
    UNION ALL
    SELECT g.client_id, max(d.redeemed_at)
    FROM acr.oauth_device_grants g
    JOIN acr.device_authorizations d ON d.device_code_hash = g.device_code_hash
    WHERE d.redeemed_at IS NOT NULL
    GROUP BY g.client_id
    ) AS source
    GROUP BY source.client_id
) AS granted
WHERE granted.client_id = c.client_id;
