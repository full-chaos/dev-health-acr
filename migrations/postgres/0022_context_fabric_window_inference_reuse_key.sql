-- CHAOS-3900 W1: bind answer reuse to ONE MORE version authority --
-- WINDOW INFERENCE VERSION (contextfabric.WindowInferenceVersion, "win_v1"
-- for slice 1). This dimension guards an INFERRED evidence window's own
-- rules: the window class vocabulary, the class-to-default table, and the
-- proposal-only temporal-expression binder's post-pass. A future
-- tightening of any of those changes what inferred window an otherwise-
-- identical question would receive, which changes what the answer actually
-- covers -- so a stored answer produced under the OLD rules must not be
-- silently replayed as if produced under the new ones.
--
-- Same shape as 0018's identity_normalization_version addition (itself
-- following 0015's five-column precedent): one dedicated, NULLABLE column,
-- a length check constraint, and a replacement reuse-key index carrying it
-- in the position FindReusable filters on. See 0015's own header comment
-- for the full "why nullable, why a dedicated column not blended with an
-- existing one, why the index is replaced not stacked" reasoning --
-- unchanged here, extended by exactly one more dimension.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). Every ALTER below is
-- independently idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS window_inference_version TEXT;

-- 128 bounds this column the same generous width as every sibling reuse-
-- key version column (0015's own comment: real values are short dotted
-- literals, e.g. "win_v1", well under 40 characters). NULL is exempted
-- (this row never participates in this dimension of reuse) and the empty
-- string is rejected, matching every prior reuse-column migration's
-- NULL-sentinel discipline.
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_window_inference_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_window_inference_version_length
        CHECK (window_inference_version IS NULL OR char_length(window_inference_version) BETWEEN 1 AND 128);

-- Replace 0021's ix_acr_cf_investigation_results_reuse_key_v6 (CHAOS-3898
-- §2.3 graph_epoch) with one carrying the new dimension, in the position
-- FindReusable filters on -- the old index is DROPPED rather than left
-- beside the new one, mirroring every prior reuse-key column migration's
-- replace-don't-stack reasoning.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v6;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v7
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, identity_normalization_version, graph_epoch, window_inference_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
