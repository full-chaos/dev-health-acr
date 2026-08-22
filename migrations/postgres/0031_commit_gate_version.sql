-- CHAOS-4085: bind answer reuse to ONE MORE version authority -- COMMIT
-- GATE VERSION (contextfabric.CommitGateVersion, "cg_v2" for the gate this
-- ticket ships). This dimension guards WHICH SUBJECTS AN ANSWER IS ALLOWED
-- TO COMMIT TO: the resolution-time refusal of the vector-margin rescue for
-- a tied top under a truncated search, and the post-synthesis affirmation
-- gate that retracts a statistically-committed subject the synthesized
-- answer does not actually support.
--
-- Why reuse MUST be fenced on it, rather than left to age out. Answer reuse
-- (CHAOS-3782) serves a stored InvestigationResult verbatim, and the lookup
-- that finds it runs BEFORE Interpret and BEFORE synthesis -- so a row
-- written under the OLD gate is served with its OLD Committed list fully
-- intact, having never passed through the new gate at any point. Without
-- this column the exact wrong commit CHAOS-4085 exists to refuse would keep
-- being served from cache, indefinitely, for every repeat of the same
-- question inside the staleness window. A gate that a cache can bypass is
-- not a gate.
--
-- Same shape as 0022's window_inference_version addition (itself following
-- 0018's identity_normalization_version and 0015's original five-column
-- precedent): one dedicated, NULLABLE column, a length check constraint,
-- and a replacement reuse-key index carrying it in the position
-- FindReusable filters on. See 0015's own header comment for the full "why
-- nullable, why a dedicated column not blended with an existing one, why
-- the index is replaced not stacked" reasoning -- unchanged here, extended
-- by exactly one more dimension.
--
-- NULL is the pre-CHAOS-4085 row. FindReusable's conjunctive equality can
-- never match NULL against the non-empty deployment-current value, so every
-- row written before this migration is permanently excluded from reuse on
-- this dimension -- which is precisely the fence, achieved without a
-- backfill and without a destructive purge.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). Every ALTER below is
-- independently idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS commit_gate_version TEXT;

-- 128 bounds this column the same generous width as every sibling reuse-key
-- version column (0015's own comment: real values are short literals, e.g.
-- "cg_v2", well under 40 characters). NULL is exempted (that row never
-- participates in this dimension of reuse) and the empty string is
-- rejected, matching every prior reuse-column migration's NULL-sentinel
-- discipline.
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_commit_gate_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_commit_gate_version_length
        CHECK (commit_gate_version IS NULL OR char_length(commit_gate_version) BETWEEN 1 AND 128);

-- Replace 0022's ix_acr_cf_investigation_results_reuse_key_v7 with one
-- carrying the new dimension, in the position FindReusable filters on --
-- the old index is DROPPED rather than left beside the new one, mirroring
-- every prior reuse-key column migration's replace-don't-stack reasoning.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v7;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v8
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, identity_normalization_version, graph_epoch, window_inference_version, commit_gate_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
