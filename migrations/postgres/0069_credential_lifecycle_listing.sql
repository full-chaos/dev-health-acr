-- CHAOS-11200: the read-only credential listing for reconciliation
-- (GET /internal/v1/credentials/lifecycle).
--
-- 1. The reconcile workload role. A workload binding's role is a closed set;
--    reconcile carries the single scope credential_lifecycle:read
--    (internal/auth/workload_scopes.go) and no other, so the listing reaches no
--    workload that holds read or ops. Binding rows stay provisioned out of band.
--
-- 2. The index that serves the listing's keyset walk. UNIQUE (aggregate_id,
--    revision) does not lead with org_id, so a page of one organization on a
--    table of many would read other organizations' rows. The walk is
--    DISTINCT aggregate_id for one org after a cursor, in byte order
--    (COLLATE "C", so the order is the same on every database); the index is
--    declared with that collation. The statement is
--    internal/storage/postgres/outbox_list.go CredentialLifecycleListSQL, and
--    its plan is pinned by TestCredentialLifecycleList_TheKeysetPageUsesTheOrgAggregateIndexOnATableOfManyOrganizations.
--
-- The migration writes no data and needs no grant: the runtime role already
-- holds SELECT on acr.credential_lifecycle_outbox (internal/dbacl/runtime_grants.go).
--
-- lock_timeout: both statements take a table lock (ACCESS EXCLUSIVE for the
-- constraint, SHARE for the index build); past 5s behind a long holder the
-- migration fails (SQLSTATE 55P03) and the roll can retry. The outbox holds one
-- row per credential mutation, so the build is short. Idempotent.
SET LOCAL lock_timeout = '5s';

ALTER TABLE acr.workload_bindings
    DROP CONSTRAINT IF EXISTS workload_bindings_role_check,
    DROP CONSTRAINT IF EXISTS ck_acr_workload_bindings_role;
ALTER TABLE acr.workload_bindings
    ADD CONSTRAINT ck_acr_workload_bindings_role CHECK (role IN ('read', 'ops', 'reconcile'));

CREATE INDEX IF NOT EXISTS ix_acr_credential_lifecycle_outbox_org_aggregate
    ON acr.credential_lifecycle_outbox (org_id, aggregate_id COLLATE "C");
