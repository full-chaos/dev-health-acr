-- CHAOS-5783 follow-up: bind answer reuse to ONE MORE version authority --
-- OWNERSHIP ROUTING VERSION (contextfabric.OwnershipRoutingVersion,
-- "ownership-routing.v1" as of this migration). This dimension guards
-- falkorgraph's own routing gate for a repository-anchored team count:
-- whether the member set comes from the repository's declared ownership
-- signal (authorization_repositories) or from hopWalk's graph-proximity
-- traversal, and which committed subject's walk-or-census contribution
-- the routed pool admits.
--
-- Why reuse MUST be fenced on it, rather than left to age out. Routing
-- rules for this exact pairing can change without changing the question
-- hash, the anchor kind, or any existing reuse dimension -- a stored
-- answer computed under different (e.g. hopWalk-only) rules must not keep
-- being served from cache for every repeat of the same question inside
-- the staleness window, disagreeing with what a fresh read now measures --
-- same class of bug 0022's window_inference_version and 0031's
-- commit_gate_version each closed for their own decision.
--
-- Same shape as 0036's question_family_version addition (itself following
-- 0035's ranking_formula_version, 0031's commit_gate_version, 0022's
-- window_inference_version, 0018's identity_normalization_version, and
-- 0015's original five-column precedent): one dedicated, NULLABLE column,
-- a length check constraint, and a replacement reuse-key index carrying
-- it in the position FindReusable filters on. See 0015's own header
-- comment for the full "why nullable, why a dedicated column not blended
-- with an existing one, why the index is replaced not stacked" reasoning
-- -- unchanged here, extended by exactly one more dimension.
--
-- NULL is every pre-migration row (every row through 0038: none of them
-- persisted this dimension). FindReusable's
-- conjunctive equality can never match NULL against the non-empty
-- deployment-current value, so every row written before this migration is
-- permanently excluded from reuse on this dimension -- which is precisely
-- the fence, achieved without a backfill and without a destructive purge.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). Every ALTER below is
-- independently idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS ownership_routing_version TEXT;

-- 128 bounds this column the same generous width as every sibling reuse-key
-- version column (0015's own comment: real values are short literals, e.g.
-- "ownership-routing.v1", well under 40 characters). NULL is exempted (that
-- row never participates in this dimension of reuse) and the empty string
-- is rejected, matching every prior reuse-column migration's NULL-sentinel
-- discipline.
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_ownership_routing_version_len;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_ownership_routing_version_len
        CHECK (ownership_routing_version IS NULL OR char_length(ownership_routing_version) BETWEEN 1 AND 128);

-- Replace 0036's ix_acr_cf_investigation_results_reuse_key_v10 with one
-- carrying the new dimension, in the position FindReusable filters on --
-- the old index is DROPPED rather than left beside the new one, mirroring
-- every prior reuse-key column migration's replace-don't-stack reasoning.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v10;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v11
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, identity_normalization_version, graph_epoch, window_inference_version, commit_gate_version, ranking_formula_version, question_family_version, ownership_routing_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
