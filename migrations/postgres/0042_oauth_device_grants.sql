-- RFC 8628 device authorization grant for the OAuth login (see
-- internal/storage/oauth.go's OAuthDeviceGrant doc comment). POST
-- /device_authorization reuses a device_authorizations row (migration 0004)
-- for state and interval enforcement, exactly like /authorize's browser
-- consent step (migration 0040) -- but unlike that step, its raw device_code
-- and user_code ARE returned to the caller, per RFC 8628 section 3.2. This table
-- carries what the reused row does not: the client and the resource/scope
-- /device_authorization named, which /token needs when the client polls
-- back with only the device_code.
--
-- Operational note: this repository never grants runtime-role table
-- privileges from a migration -- grants are managed entirely out of band
-- (see acr-db-init.sh's runtime-acl mode). An operator upgrading to this
-- migration must additionally grant the runtime role SELECT and INSERT on
-- acr.oauth_device_grants.

CREATE TABLE IF NOT EXISTS acr.oauth_device_grants (
    device_code_hash TEXT PRIMARY KEY REFERENCES acr.device_authorizations (device_code_hash) ON DELETE CASCADE,
    client_id TEXT NOT NULL,
    client_kind TEXT NOT NULL,
    resource TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    CHECK (device_code_hash ~ '^[0-9a-f]{64}$'),
    CHECK (client_kind IN ('dynamic', 'metadata_document'))
);

CREATE INDEX IF NOT EXISTS ix_acr_oauth_device_grants_expiry
    ON acr.oauth_device_grants (expires_at);

COMMENT ON COLUMN acr.oauth_device_grants.device_code_hash IS
    'The reused device_authorizations row backing this /device_authorization request; unlike /authorize''s browser-consent row, its raw device_code and user_code ARE returned to the caller (RFC 8628).';
COMMENT ON COLUMN acr.oauth_device_grants.resource IS
    'RFC 8707 protected resource this device grant (and the credential it eventually issues) is bound to.';
