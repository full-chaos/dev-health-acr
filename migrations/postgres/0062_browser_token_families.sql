-- CHAOS-12124: browser token families and their refresh tokens.
--
-- A browser-class credential's code exchange starts a family: its first
-- access credential (acr.client_credentials.family_id, migration 0058) and an
-- opaque, single-use refresh token, stored here by its SHA-256 hash only. A
-- refresh locks the family row (both replicas), consumes the presented token
-- and issues a successor pair; the family's absolute end (expires_at) is
-- never extended. A consumed token presented again within the grace window,
-- while its successor is still unused, is answered with the same successor:
-- the successor's tokens are derived from the presented token and the
-- rotation seed stored on the consumed row, so no plaintext token is kept.
-- Any other reuse revokes the family: the family row and every member
-- credential.
--
-- Rollback: new tables only; the previous release neither reads nor writes
-- them. No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

CREATE TABLE IF NOT EXISTS acr.browser_token_families (
    family_id TEXT PRIMARY KEY,
    org_id UUID NOT NULL,
    client_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoked_reason TEXT,
    CONSTRAINT ck_acr_browser_token_families_shape CHECK (
        char_length(family_id) BETWEEN 1 AND 256
        AND char_length(client_id) BETWEEN 1 AND 2048
        AND expires_at > created_at
        AND ((revoked_at IS NULL AND revoked_reason IS NULL)
             OR (revoked_at IS NOT NULL AND revoked_reason IS NOT NULL AND revoked_reason IN ('reuse', 'sign_out', 'member_revoked')))
    )
);

CREATE TABLE IF NOT EXISTS acr.browser_refresh_tokens (
    token_hash TEXT PRIMARY KEY,
    family_id TEXT NOT NULL REFERENCES acr.browser_token_families (family_id) ON DELETE CASCADE,
    credential_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    successor_hash TEXT,
    rotation_seed BYTEA,
    CONSTRAINT ck_acr_browser_refresh_tokens_shape CHECK (
        char_length(token_hash) = 64
        AND ((consumed_at IS NULL AND successor_hash IS NULL AND rotation_seed IS NULL)
             OR (consumed_at IS NOT NULL AND successor_hash IS NOT NULL AND rotation_seed IS NOT NULL
                 AND char_length(successor_hash) = 64 AND octet_length(rotation_seed) = 32))
    )
);

-- The organization write fence (0052), on the org-keyed new table; the org
-- purge deletes a family and its refresh tokens go by cascade.
DROP TRIGGER IF EXISTS refuse_purged_org_insert ON acr.browser_token_families;
DROP TRIGGER IF EXISTS refuse_purged_org_update ON acr.browser_token_families;
CREATE TRIGGER refuse_purged_org_insert BEFORE INSERT ON acr.browser_token_families
    FOR EACH ROW EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
CREATE TRIGGER refuse_purged_org_update BEFORE UPDATE OF org_id ON acr.browser_token_families
    FOR EACH ROW WHEN (NEW.org_id IS DISTINCT FROM OLD.org_id) EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();

CREATE INDEX IF NOT EXISTS ix_acr_browser_refresh_tokens_family
    ON acr.browser_refresh_tokens (family_id);
CREATE INDEX IF NOT EXISTS ix_acr_client_credentials_family
    ON acr.client_credentials (org_id, family_id) WHERE family_id IS NOT NULL;

COMMENT ON TABLE acr.browser_token_families IS
    'CHAOS-12124: browser-class token families; the row lock every refresh of a family takes. expires_at is the absolute end (never extended).';
COMMENT ON TABLE acr.browser_refresh_tokens IS
    'CHAOS-12124: browser refresh tokens by SHA-256 hash; single use. rotation_seed lets a grace replay derive the same successor without a stored plaintext.';
