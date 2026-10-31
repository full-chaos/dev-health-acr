-- Not a numbered migration and no history row: the runner runs this after it
-- applies the pending files, on the same connection, under the same advisory
-- lock, in one transaction, and only when version 63 is applied and an
-- operator row exists (CHAOS-12185). Operator is a class with no writer; the
-- previous release still writes it for a CLI credential during a rollback
-- window, and 0063 relabelled the rows only once.
--
-- The immutability trigger refuses a class change, so it is off for this one
-- UPDATE. The transaction never leaves it disabled. The runner reads the row
-- count from the transaction-local setting. No inline BEGIN/COMMIT.

ALTER TABLE acr.client_credentials DISABLE TRIGGER trg_client_credentials_provenance_immutable;

DO $$
DECLARE
    relabelled bigint;
BEGIN
    UPDATE acr.client_credentials SET client_class = 'org_bearer' WHERE client_class = 'operator';
    GET DIAGNOSTICS relabelled = ROW_COUNT;
    PERFORM set_config('acr.reconcile_client_class_count', relabelled::text, true);
END
$$;

ALTER TABLE acr.client_credentials ENABLE TRIGGER trg_client_credentials_provenance_immutable;
