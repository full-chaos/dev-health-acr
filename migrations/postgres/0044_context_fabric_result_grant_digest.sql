-- CHAOS-7145: the computing principal's grant digest, persisted beside the
-- result. A stored result is read by id (investigation_result,
-- parent_result_id); a repository-restricted reader is served only a result
-- computed under its own grant, so the row records which grant computed it.
--
-- Value: 'g:<32 hex>' for a repository-restricted computing principal (the
-- same digest CHAOS-7127 keys answer reuse with), 'open' for an unrestricted
-- or universal one. NULL means "saved before this column": nothing backfills
-- it, because the grant that computed an old row cannot be reconstructed. The
-- application refuses a restricted reader a NULL row (fail closed) and leaves
-- unrestricted readers unchanged.
--
-- NOT VALID, as 0027/0038 do: enforced on every INSERT/UPDATE from now on; the
-- validating scan of existing rows (all NULL) is skipped to keep the ACCESS
-- EXCLUSIVE window short. No index: read by the existing point lookup only.
-- No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS grant_digest TEXT;

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_grant_digest_shape;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_grant_digest_shape
        CHECK (grant_digest IS NULL OR grant_digest = 'open' OR grant_digest ~ '^g:[0-9a-f]{32}$')
        NOT VALID;
