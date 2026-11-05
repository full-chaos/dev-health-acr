-- The revoke workload role (POST /internal/v1/credentials/{credential_id}/revoke).
--
-- A workload binding's role is a closed set; revoke carries the single scope
-- credential_lifecycle:revoke (internal/auth/workload_scopes.go) and no other,
-- so the on-behalf revoke reaches no workload that holds read, ops or
-- reconcile. Binding rows stay provisioned out of band.
--
-- The migration writes no data and needs no grant. lock_timeout: the
-- constraint swap takes an ACCESS EXCLUSIVE lock on a small table; past 5s
-- behind a long holder the migration fails (SQLSTATE 55P03) and the roll can
-- retry. Idempotent.
SET LOCAL lock_timeout = '5s';

ALTER TABLE acr.workload_bindings
    DROP CONSTRAINT IF EXISTS ck_acr_workload_bindings_role;
ALTER TABLE acr.workload_bindings
    ADD CONSTRAINT ck_acr_workload_bindings_role CHECK (role IN ('read', 'ops', 'reconcile', 'revoke'));
