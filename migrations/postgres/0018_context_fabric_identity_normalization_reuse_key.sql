-- CHAOS-3884: bind answer reuse to ONE MORE version authority --
-- IDENTITY NORMALIZATION VERSION (graphrank.IdentityNormalizationVersion,
-- "identity_norm_v1" for slice 1: TrimSpace+ToLower, no NFC). The alias
-- projection design defers NFC-before-lowercase normalization for the
-- identity-matching path (repository/project bare-name and provider-variant
-- aliases) on the grounds that it is safe to defer ONLY because a future
-- tightening changes what two differently-cased/differently-composed
-- aliases normalize to -- which changes the identity fast path's own
-- commit decision -- and this column is what makes that change invalidate
-- rather than silently revalidate an already-stored answer.
--
-- Same shape as 0015's five-column addition (interpretation/synthesis
-- prompt version, query/canonical-service/model-output-schema version):
-- one dedicated, NULLABLE column, a length check constraint, and a
-- replacement reuse-key index carrying it in the position FindReusable
-- filters on. See 0015's own header comment for the full "why nullable,
-- why a dedicated column not blended with an existing one, why the index
-- is replaced not stacked" reasoning -- unchanged here, extended by
-- exactly one more dimension.
--
-- No inline BEGIN/COMMIT (see 0015's own note: migrations/postgres/
-- runner.go's applyMigration already wraps this file in its own
-- transaction). Every ALTER below is independently idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS identity_normalization_version TEXT;

-- 128 bounds this column the same generous width as every sibling reuse-
-- key version column (0015's own comment: real values are short dotted
-- literals, e.g. "identity_norm_v1", well under 40 characters). NULL is
-- exempted (this row never participates in this dimension of reuse) and
-- the empty string is rejected, matching every prior reuse-column
-- migration's NULL-sentinel discipline.
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_identity_norm_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_identity_norm_version_length
        CHECK (identity_normalization_version IS NULL OR char_length(identity_normalization_version) BETWEEN 1 AND 128);

-- Replace 0015's ix_acr_cf_investigation_results_reuse_key_v4 with one
-- carrying the new dimension, in the position FindReusable filters on --
-- the old index is DROPPED rather than left beside the new one, mirroring
-- every prior reuse-key column migration's replace-don't-stack reasoning.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v4;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v5
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, identity_normalization_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
