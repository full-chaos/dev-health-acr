-- Retire the context:admin scope from every stored scope column and keep it
-- out.
--
-- No route is gated by the scope any more, and the credential validators now
-- reject it. A stored credential or grant that still carried it would
-- authenticate but fail validation on self-rotate and self-revoke.
--
-- 1. An unredeemed device authorization (pending or approved) whose scope
--    would be EMPTY after the strip (empty tokens from repeated or trailing
--    spaces do not count) is deleted, with its OAuth grant or
--    request (ON DELETE CASCADE). These rows live ten minutes, and an empty
--    scope string means "scope omitted", which the OAuth exchange would
--    answer with the default scopes: the migration must never widen a grant
--    into the default pair.
-- 2. Every other stored value is stripped and keeps its remaining scopes.
-- 3. A CHECK on each column refuses the value from now on. An API pod still
--    running the previous release during a rolling upgrade cannot write the
--    scope back: its write fails instead.
--
-- Idempotent: a rerun matches no row. The row counts are logged with RAISE
-- NOTICE; a database without the scope (prod and bigboy at the time of
-- writing) logs zeros.
DO $$
DECLARE
    abandoned_devices    bigint;
    credentials_stripped bigint;
    devices_stripped     bigint;
    grants_stripped      bigint;
    requests_stripped    bigint;
BEGIN
    DELETE FROM acr.device_authorizations d
     WHERE d.state IN ('pending', 'approved')
       AND (
            (d.authorized_scopes @> '["context:admin"]'::jsonb
             AND (d.authorized_scopes - 'context:admin') = '[]'::jsonb)
         OR EXISTS (
                SELECT 1 FROM acr.oauth_device_grants g
                 WHERE g.device_code_hash = d.device_code_hash
                   AND 'context:admin' = ANY (string_to_array(g.scope, ' '))
                   AND array_remove(array_remove(string_to_array(g.scope, ' '), 'context:admin'), '') = ARRAY[]::text[])
         OR EXISTS (
                SELECT 1 FROM acr.oauth_authorization_requests r
                 WHERE r.device_code_hash = d.device_code_hash
                   AND 'context:admin' = ANY (string_to_array(r.scope, ' '))
                   AND array_remove(array_remove(string_to_array(r.scope, ' '), 'context:admin'), '') = ARRAY[]::text[])
       );
    GET DIAGNOSTICS abandoned_devices = ROW_COUNT;

    UPDATE acr.client_credentials
       SET scopes = scopes - 'context:admin'
     WHERE scopes @> '["context:admin"]'::jsonb;
    GET DIAGNOSTICS credentials_stripped = ROW_COUNT;

    UPDATE acr.device_authorizations
       SET authorized_scopes = authorized_scopes - 'context:admin'
     WHERE authorized_scopes @> '["context:admin"]'::jsonb;
    GET DIAGNOSTICS devices_stripped = ROW_COUNT;

    UPDATE acr.oauth_device_grants
       SET scope = array_to_string(array_remove(array_remove(string_to_array(scope, ' '), 'context:admin'), ''), ' ')
     WHERE 'context:admin' = ANY (string_to_array(scope, ' '));
    GET DIAGNOSTICS grants_stripped = ROW_COUNT;

    UPDATE acr.oauth_authorization_requests
       SET scope = array_to_string(array_remove(array_remove(string_to_array(scope, ' '), 'context:admin'), ''), ' ')
     WHERE 'context:admin' = ANY (string_to_array(scope, ' '));
    GET DIAGNOSTICS requests_stripped = ROW_COUNT;

    RAISE NOTICE 'context:admin retired: device_authorizations deleted=%, stripped client_credentials=%, device_authorizations=%, oauth_device_grants=%, oauth_authorization_requests=%',
        abandoned_devices, credentials_stripped, devices_stripped, grants_stripped, requests_stripped;
END
$$;

ALTER TABLE acr.client_credentials
    DROP CONSTRAINT IF EXISTS ck_acr_client_credentials_no_retired_context_admin;
ALTER TABLE acr.client_credentials
    ADD CONSTRAINT ck_acr_client_credentials_no_retired_context_admin
        CHECK (NOT (scopes @> '["context:admin"]'::jsonb));

ALTER TABLE acr.device_authorizations
    DROP CONSTRAINT IF EXISTS ck_acr_device_authorizations_no_retired_context_admin;
ALTER TABLE acr.device_authorizations
    ADD CONSTRAINT ck_acr_device_authorizations_no_retired_context_admin
        CHECK (NOT (authorized_scopes @> '["context:admin"]'::jsonb));

ALTER TABLE acr.oauth_device_grants
    DROP CONSTRAINT IF EXISTS ck_acr_oauth_device_grants_no_retired_context_admin;
ALTER TABLE acr.oauth_device_grants
    ADD CONSTRAINT ck_acr_oauth_device_grants_no_retired_context_admin
        CHECK (NOT ('context:admin' = ANY (string_to_array(scope, ' '))));

ALTER TABLE acr.oauth_authorization_requests
    DROP CONSTRAINT IF EXISTS ck_acr_oauth_authorization_requests_no_retired_context_admin;
ALTER TABLE acr.oauth_authorization_requests
    ADD CONSTRAINT ck_acr_oauth_authorization_requests_no_retired_context_admin
        CHECK (NOT ('context:admin' = ANY (string_to_array(scope, ' '))));
