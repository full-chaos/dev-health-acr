-- OAuth 2.1 authorization-code login for the hosted MCP endpoint (see
-- internal/storage/oauth.go's package doc comment for the full flow). An
-- MCP client runs /authorize + /token against acr-api as its OAuth
-- authorization server, with PKCE. The browser consent step reuses the
-- existing device authorization record (acr.device_authorizations,
-- migration 0004): /authorize starts one purely to get an approval row,
-- and the token endpoint later redeems the approved record by its hash.
--
-- Operational note: this repository never grants runtime-role table
-- privileges from a migration -- grants are managed entirely out of band
-- (see acr-db-init.sh's runtime-acl mode). An operator upgrading to this
-- migration must additionally grant the runtime role SELECT, INSERT, and
-- UPDATE on acr.oauth_clients and acr.oauth_authorization_requests.

-- resource binds an OAuth-issued credential to the RFC 8707 protected
-- resource it was requested for. NULL for every other issuance path
-- (device flow without a resource indicator, self-service, workload
-- token exchange) -- see internal/storage/internal/credentiallifecycle
-- for the validation and provenance pairing.
ALTER TABLE acr.client_credentials
    ADD COLUMN IF NOT EXISTS resource TEXT
    CHECK (resource IS NULL OR char_length(resource) BETWEEN 1 AND 2048);

COMMENT ON COLUMN acr.client_credentials.resource IS
    'RFC 8707 protected resource an OAuth-issued credential is bound to. NULL for every other issuance path.';

-- acr.oauth_clients holds dynamically registered public clients (RFC 7591).
-- Only public clients exist here: registration never issues a client
-- secret.
CREATE TABLE IF NOT EXISTS acr.oauth_clients (
    client_id TEXT PRIMARY KEY,
    client_name TEXT NOT NULL DEFAULT '',
    redirect_uris JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (client_id ~ '^acrc_[0-9a-f]{32}$'),
    CHECK (jsonb_typeof(redirect_uris) = 'array')
);

COMMENT ON COLUMN acr.oauth_clients.client_id IS
    'Dynamic client identifier: acrc_ followed by 32 lowercase hex characters.';
COMMENT ON COLUMN acr.oauth_clients.redirect_uris IS
    'JSON array of absolute redirect URIs validated by ValidOAuthRedirectURI at registration time.';

-- acr.oauth_authorization_requests is one pending or completed /authorize
-- request. It carries what the reused device_authorizations row does not:
-- the client, its redirect URI, the PKCE challenge, the protected resource
-- the eventual token is bound to, and the one-time authorization code.
-- handle_hash and code_hash are SHA-256 hashes of high-entropy one-time
-- secrets; the raw values are never persisted (see storage.OAuthSecretHash).
CREATE TABLE IF NOT EXISTS acr.oauth_authorization_requests (
    handle_hash TEXT PRIMARY KEY,
    device_code_hash TEXT NOT NULL UNIQUE REFERENCES acr.device_authorizations (device_code_hash) ON DELETE CASCADE,
    client_id TEXT NOT NULL,
    client_kind TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    resource TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    code_hash TEXT UNIQUE,
    code_expires_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,
    CHECK (handle_hash ~ '^[0-9a-f]{64}$'),
    CHECK (client_kind IN ('dynamic')),
    CHECK (code_challenge ~ '^[A-Za-z0-9_-]{43}$'),
    CHECK (code_hash IS NULL OR code_hash ~ '^[0-9a-f]{64}$'),
    CHECK ((code_hash IS NULL) = (code_expires_at IS NULL)),
    CHECK (consumed_at IS NULL OR code_hash IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS ix_acr_oauth_authorization_requests_expiry
    ON acr.oauth_authorization_requests (expires_at);

COMMENT ON COLUMN acr.oauth_authorization_requests.device_code_hash IS
    'The reused device_authorizations row backing this /authorize request''s browser consent; its own raw device code is generated and discarded so the device grant can never redeem it.';
COMMENT ON COLUMN acr.oauth_authorization_requests.resource IS
    'RFC 8707 protected resource this authorization (and the credential it eventually issues) is bound to.';
COMMENT ON COLUMN acr.oauth_authorization_requests.code_hash IS
    'SHA-256 hash of the one-time authorization code. Set once by IssueAuthorizationCode; the raw code is never persisted.';
