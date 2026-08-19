-- CHAOS-3898 §2.1/§2.3: bind answer reuse to the organization's ACTIVE
-- graph-lifecycle epoch (contextfabric.ResolvedGraphBinding.Epoch,
-- OrgGraphLifecycle.ActiveEpoch -- see ResolvedGraphBinding's own doc
-- comment for why this is a THIRD, structurally distinct dimension from
-- invalidation_epoch/RebuildEpoch, migration 0009/0011's reuse-invalidation
-- counter). A stored answer's graph_epoch names the epoch its OWN graph
-- reads actually used (Engine.Investigate resolves the binding once, before
-- either graph call, and stamps Save with the SAME value); a build/flip
-- that moves an organization's active epoch must invalidate reuse for every
-- row generated under the epoch it moved away from, independent of and in
-- addition to the coarser invalidation_epoch fence.
--
-- Same shape as every reuse-key dimension since 0011: one dedicated,
-- NULLABLE column, a bounds check constraint, and a replacement reuse-key
-- index carrying it in the position FindReusable filters on. See 0015's
-- header comment for the full "why nullable, why a dedicated column, why
-- the index is replaced not stacked" reasoning -- unchanged here, extended
-- by exactly one more dimension. Unlike its TEXT-typed siblings, graph_epoch
-- is BIGINT (an epoch number, never a version string), so its guard is a
-- non-negativity check rather than a length check.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). Every ALTER below is
-- independently idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS graph_epoch BIGINT;

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_graph_epoch_nonneg;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_graph_epoch_nonneg
        CHECK (graph_epoch IS NULL OR graph_epoch >= 0);

-- Replace 0018's ix_acr_cf_investigation_results_reuse_key_v5 with one
-- carrying the new dimension, in the position FindReusable filters on --
-- the old index is DROPPED rather than left beside the new one, mirroring
-- every prior reuse-key column migration's replace-don't-stack reasoning.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v5;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v6
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, identity_normalization_version, graph_epoch, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
