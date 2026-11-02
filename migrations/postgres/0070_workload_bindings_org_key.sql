-- One workload identity bound in several organizations.
--
-- acr.workload_bindings was UNIQUE (trust_domain, namespace,
-- service_account_name, service_account_uid): one Kubernetes identity, one
-- binding, one organization. The key now also holds org_id, so the same
-- identity may carry one binding per organization. The token exchange picks
-- among them with an organization selector (internal/auth/workload_grant_resolver.go).
--
-- The old key was created inline in 0030 and has a server-generated name, so
-- it is found by its column set and dropped by the name the catalog reports.
-- No data changes; every existing row already satisfies the wider key.
-- Idempotent.
SET LOCAL lock_timeout = '5s';

DO $$
DECLARE
    old_name text;
BEGIN
    FOR old_name IN
        SELECT c.conname
        FROM pg_constraint c
        WHERE c.conrelid = 'acr.workload_bindings'::regclass
          AND c.contype = 'u'
          AND c.conname <> 'uq_acr_workload_bindings_identity_org'
          AND (SELECT array_agg(a.attname::text ORDER BY a.attname)
               FROM unnest(c.conkey) AS k(attnum)
               JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum)
              = ARRAY['namespace', 'service_account_name', 'service_account_uid', 'trust_domain']
    LOOP
        EXECUTE format('ALTER TABLE acr.workload_bindings DROP CONSTRAINT %I', old_name);
    END LOOP;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'acr.workload_bindings'::regclass
          AND conname = 'uq_acr_workload_bindings_identity_org'
    ) THEN
        ALTER TABLE acr.workload_bindings
            ADD CONSTRAINT uq_acr_workload_bindings_identity_org
            UNIQUE (trust_domain, namespace, service_account_name, service_account_uid, org_id);
    END IF;
END
$$;
