-- CHAOS-4013: RFC 8693 workload token exchange for machine auth.
--
-- acr.workload_bindings is the declarative, server-side-only grant map:
-- {trust domain, namespace, service account name, service account uid} ->
-- {binding_id, org_id, role, repository_scopes}. There is no HTTP CRUD
-- surface for this table by design (the ratified design brief's "do NOT
-- build" list) -- rows are provisioned out of band by an operator. A
-- validated k8s TokenReview identity is looked up against this EXACT
-- tuple; it is never resolved from anything request-supplied.
CREATE TABLE IF NOT EXISTS acr.workload_bindings (
    binding_id TEXT PRIMARY KEY,
    org_id UUID NOT NULL,
    trust_domain TEXT NOT NULL,
    namespace TEXT NOT NULL,
    service_account_name TEXT NOT NULL,
    service_account_uid TEXT NOT NULL,
    role TEXT NOT NULL,
    repository_scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    created_by TEXT NOT NULL,
    disabled_at TIMESTAMPTZ,
    CHECK (role IN ('read', 'ops')),
    CHECK (jsonb_typeof(repository_scopes) = 'array'),
    UNIQUE (trust_domain, namespace, service_account_name, service_account_uid)
);

CREATE INDEX IF NOT EXISTS ix_acr_workload_bindings_org_active
    ON acr.workload_bindings (org_id, disabled_at);

-- workload_binding_id distinguishes a credential row minted by workload
-- token exchange from every other issuance path (device flow, self-
-- service, rotation), and is what authentication uses as Principal.Subject
-- for such a row instead of credential_id (see
-- internal/auth/middleware.go) -- a workload re-exchanges a fresh
-- credential row roughly every 10 minutes, so quotas must key on the
-- stable binding, not the churning row. ON DELETE RESTRICT: a binding must
-- be disabled (see acr.workload_bindings.disabled_at), never deleted,
-- while it still has live or historical credential rows referencing it.
ALTER TABLE acr.client_credentials
    ADD COLUMN IF NOT EXISTS workload_binding_id TEXT REFERENCES acr.workload_bindings(binding_id) ON DELETE RESTRICT;

-- Supports NewWorkloadCredentialPurger's bounded batch DELETE of expired
-- workload rows -- see that function's doc comment for why these rows need
-- their own cleanup sweep (10-minute TTL, re-exchanged continuously) that
-- ordinary long-lived credentials never needed.
CREATE INDEX IF NOT EXISTS ix_acr_client_credentials_workload_cleanup
    ON acr.client_credentials (expires_at)
    WHERE workload_binding_id IS NOT NULL;
