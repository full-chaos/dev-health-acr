-- Bind answer reuse to ONE MORE version authority: GRAPH WALK VERSION
-- (contextfabric.GraphWalkVersion, "graph-walk.v1" as of this migration), the
-- rules that pick which graph edges fit an investigation's edge budget. A
-- stored answer read under other rules must miss, not be replayed as if a
-- fresh read had produced it.
--
-- Same shape as 0039's ownership_routing_version: one dedicated, NULLABLE
-- column, a length check constraint, and a replacement reuse-key index that
-- carries it in the position FindReusable filters on. NULL is every row saved
-- before this migration; FindReusable's equality never matches NULL, so those
-- rows are excluded from reuse on this dimension without a backfill.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- wraps this file in its own transaction). Every statement is idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS graph_walk_version TEXT;

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_graph_walk_version_len;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_graph_walk_version_len
        CHECK (graph_walk_version IS NULL OR char_length(graph_walk_version) BETWEEN 1 AND 128);

DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v11;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v12
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, identity_normalization_version, graph_epoch, window_inference_version, commit_gate_version, ranking_formula_version, question_family_version, ownership_routing_version, graph_walk_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
