CREATE TABLE acr.device_authorizations (
    device_code_hash TEXT PRIMARY KEY,
    user_code_hash TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL,
    authorized_org_id UUID,
    authorized_repository_scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
    authorized_scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
    approving_subject TEXT,
    approving_authentication_method TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    poll_interval_seconds INTEGER NOT NULL,
    last_poll_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    redeemed_at TIMESTAMPTZ,
    redeemed_credential_id TEXT UNIQUE REFERENCES acr.client_credentials (credential_id),
    issuance_provenance TEXT NOT NULL,
    CHECK (device_code_hash ~ '^[0-9a-f]{64}$'),
    CHECK (user_code_hash ~ '^[0-9a-f]{64}$'),
    CHECK (state IN ('pending', 'approved', 'denied', 'expired', 'redeemed')),
    CHECK (jsonb_typeof(authorized_repository_scopes) = 'array'),
    CHECK (jsonb_typeof(authorized_scopes) = 'array'),
    CHECK (poll_interval_seconds = 5),
    CHECK (expires_at = created_at + INTERVAL '10 minutes'),
    CHECK (approving_authentication_method IS NULL OR approving_authentication_method = 'web_assertion'),
    CHECK (state NOT IN ('approved', 'redeemed') OR approved_at IS NOT NULL),
    CHECK ((state = 'redeemed') = (redeemed_at IS NOT NULL)),
    CHECK ((state = 'redeemed') = (redeemed_credential_id IS NOT NULL)),
    CHECK (issuance_provenance = 'device_authorization')
);

CREATE INDEX ix_acr_device_authorizations_expiry
    ON acr.device_authorizations (expires_at)
    WHERE state IN ('pending', 'approved');

COMMENT ON COLUMN acr.device_authorizations.device_code_hash IS
    'SHA-256 hash of the high-entropy device code. The raw code is never persisted.';
COMMENT ON COLUMN acr.device_authorizations.user_code_hash IS
    'SHA-256 hash of the user code. The raw code is never persisted.';
