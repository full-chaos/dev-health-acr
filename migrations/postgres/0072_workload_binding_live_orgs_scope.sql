-- One revoke binding for every live organization (org_scope = 'live_orgs').
--
-- A workload binding names one organization. A workload that acts for every
-- organization (go-api's on-behalf revoke, POST
-- /internal/v1/credentials/{credential_id}/revoke) needed one row per
-- organization, and a new organization had no row until an operator wrote one.
-- A live_orgs row has no org_id: the token exchange binds the credential to the
-- organization the request selects, and only when Dev Health answers, read
-- fresh, that the organization exists (internal/auth/workload_grant_resolver.go).
--
-- The constraints keep the row to that one use:
-- - ck_acr_workload_bindings_org_scope: org_scope is 'org' (with an org_id) or
--   'live_orgs' (no org_id).
-- - ck_acr_workload_bindings_live_orgs_revoke: a live_orgs row has the revoke
--   role and no repository scope; the resolver also grants it the revoke scope
--   only, whatever the row holds.
-- - uq_acr_workload_bindings_live_orgs_identity: at most one enabled live_orgs
--   row per workload identity. A per-org row keeps uq_acr_workload_bindings_identity_org
--   (migration 0070); its org_id is never null.
--
-- Rows stay provisioned out of band; the runtime role keeps SELECT only.
-- Existing rows get org_scope 'org' and keep their org_id, so the migration
-- changes no answer. lock_timeout: the column and constraint changes take an
-- ACCESS EXCLUSIVE lock on a small table; past 5s behind a long holder the
-- migration fails (SQLSTATE 55P03) and the roll can retry. Idempotent.
SET LOCAL lock_timeout = '5s';

ALTER TABLE acr.workload_bindings
    ADD COLUMN IF NOT EXISTS org_scope TEXT NOT NULL DEFAULT 'org';

ALTER TABLE acr.workload_bindings
    ALTER COLUMN org_id DROP NOT NULL;

ALTER TABLE acr.workload_bindings
    DROP CONSTRAINT IF EXISTS ck_acr_workload_bindings_org_scope;
ALTER TABLE acr.workload_bindings
    ADD CONSTRAINT ck_acr_workload_bindings_org_scope CHECK (
        (org_scope = 'org' AND org_id IS NOT NULL)
        OR (org_scope = 'live_orgs' AND org_id IS NULL)
    );

ALTER TABLE acr.workload_bindings
    DROP CONSTRAINT IF EXISTS ck_acr_workload_bindings_live_orgs_revoke;
ALTER TABLE acr.workload_bindings
    ADD CONSTRAINT ck_acr_workload_bindings_live_orgs_revoke CHECK (
        org_scope <> 'live_orgs' OR (role = 'revoke' AND repository_scopes = '[]'::jsonb)
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_acr_workload_bindings_live_orgs_identity
    ON acr.workload_bindings (trust_domain, namespace, service_account_name, service_account_uid)
    WHERE org_scope = 'live_orgs' AND disabled_at IS NULL;
