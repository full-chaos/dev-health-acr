-- CHAOS-4398 PR3 (R4 ruling): bind answer reuse to ONE MORE version
-- authority -- RANKING FORMULA VERSION (contextfabric.RankingFormulaVersion,
-- "cohort-ranking.v2" for the threshold change this ticket ships). This
-- dimension guards the cohort ranking formula RankCohort computes every
-- member's Score/RankingBasis/Outcome/MissingSignals from: signal weights,
-- the Score-nil-below-threshold cutoff, and the signal set itself.
--
-- Why reuse MUST be fenced on it, rather than left to age out. RankCohort
-- runs AFTER FindReusable (engine.go) -- a matching stored answer is served
-- verbatim, ranking table included, without RankCohort ever re-running. v2
-- of the formula (design doc §8) changed what Score/Outcome even MEAN for
-- the same signal weights: a member the OLD formula scored now maps to
-- insufficient_evidence/not_applicable with no Score at all, or vice versa.
-- Without this column a cohort question answered before a formula bump
-- would keep being served from cache, under the OLD formula's semantics,
-- for every repeat inside the staleness window -- the same class of bug
-- 0022's window_inference_version and 0031's commit_gate_version each
-- closed for their own decision.
--
-- Same shape as 0031's commit_gate_version addition (itself following
-- 0022's window_inference_version, 0018's identity_normalization_version,
-- and 0015's original five-column precedent): one dedicated, NULLABLE
-- column, a length check constraint, and a replacement reuse-key index
-- carrying it in the position FindReusable filters on. See 0015's own
-- header comment for the full "why nullable, why a dedicated column not
-- blended with an existing one, why the index is replaced not stacked"
-- reasoning -- unchanged here, extended by exactly one more dimension.
--
-- NULL is the pre-CHAOS-4398-PR3 row. FindReusable's conjunctive equality
-- can never match NULL against the non-empty deployment-current value, so
-- every row written before this migration is permanently excluded from
-- reuse on this dimension -- which is precisely the fence, achieved
-- without a backfill and without a destructive purge.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). Every ALTER below is
-- independently idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS ranking_formula_version TEXT;

-- 128 bounds this column the same generous width as every sibling reuse-key
-- version column (0015's own comment: real values are short literals, e.g.
-- "cohort-ranking.v2", well under 40 characters). NULL is exempted (that
-- row never participates in this dimension of reuse) and the empty string
-- is rejected, matching every prior reuse-column migration's NULL-sentinel
-- discipline.
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_ranking_formula_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_ranking_formula_version_length
        CHECK (ranking_formula_version IS NULL OR char_length(ranking_formula_version) BETWEEN 1 AND 128);

-- Replace 0031's ix_acr_cf_investigation_results_reuse_key_v8 with one
-- carrying the new dimension, in the position FindReusable filters on --
-- the old index is DROPPED rather than left beside the new one, mirroring
-- every prior reuse-key column migration's replace-don't-stack reasoning.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v8;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v9
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, identity_normalization_version, graph_epoch, window_inference_version, commit_gate_version, ranking_formula_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
