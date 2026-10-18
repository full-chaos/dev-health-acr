-- Strip the retired context:admin scope from every stored scope column.
--
-- No route is gated by the scope any more, and the credential validators now
-- reject it. A stored credential or grant that still carried it would
-- authenticate but fail validation on self-rotate and self-revoke. Removing
-- it here leaves the row valid with its remaining scopes, so no row can carry
-- the value once this migration has run.
--
-- Idempotent: a row without the scope is not matched, so a rerun touches zero
-- rows. The row counts are logged with RAISE NOTICE; a database without the
-- scope (prod and bigboy at the time of writing) logs four zeros.
DO $$
DECLARE
    credentials_stripped bigint;
    devices_stripped     bigint;
    grants_stripped      bigint;
    requests_stripped    bigint;
BEGIN
    UPDATE acr.client_credentials
       SET scopes = scopes - 'context:admin'
     WHERE scopes @> '["context:admin"]'::jsonb;
    GET DIAGNOSTICS credentials_stripped = ROW_COUNT;

    UPDATE acr.device_authorizations
       SET authorized_scopes = authorized_scopes - 'context:admin'
     WHERE authorized_scopes @> '["context:admin"]'::jsonb;
    GET DIAGNOSTICS devices_stripped = ROW_COUNT;

    UPDATE acr.oauth_device_grants
       SET scope = array_to_string(array_remove(string_to_array(scope, ' '), 'context:admin'), ' ')
     WHERE 'context:admin' = ANY (string_to_array(scope, ' '));
    GET DIAGNOSTICS grants_stripped = ROW_COUNT;

    UPDATE acr.oauth_authorization_requests
       SET scope = array_to_string(array_remove(string_to_array(scope, ' '), 'context:admin'), ' ')
     WHERE 'context:admin' = ANY (string_to_array(scope, ' '));
    GET DIAGNOSTICS requests_stripped = ROW_COUNT;

    RAISE NOTICE 'context:admin stripped: client_credentials=%, device_authorizations=%, oauth_device_grants=%, oauth_authorization_requests=%',
        credentials_stripped, devices_stripped, grants_stripped, requests_stripped;
END
$$;
