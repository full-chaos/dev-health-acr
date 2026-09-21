-- Client ID metadata document (CIMD) support for the MCP OAuth login
-- (draft-ietf-oauth-client-id-metadata-document). 0040 shipped
-- acr.oauth_authorization_requests.client_kind restricted to 'dynamic'; an
-- authorization request whose client_id is an HTTPS metadata document URL
-- records client_kind = 'metadata_document'. Such a client is never stored
-- in acr.oauth_clients: its metadata is fetched and validated per request.
--
-- 0040 is a shipped migration and is not edited. The inline CHECK it
-- declared on client_kind is unnamed, so PostgreSQL named it
-- oauth_authorization_requests_client_kind_check; it is dropped and
-- re-added here with the wider vocabulary. Re-running is a no-op.
ALTER TABLE acr.oauth_authorization_requests
    DROP CONSTRAINT IF EXISTS oauth_authorization_requests_client_kind_check;

ALTER TABLE acr.oauth_authorization_requests
    ADD CONSTRAINT oauth_authorization_requests_client_kind_check
    CHECK (client_kind IN ('dynamic', 'metadata_document'));
