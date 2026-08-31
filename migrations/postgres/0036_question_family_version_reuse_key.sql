-- CHAOS-4634 (S4, per the CHAOS-4632/S2 note deferred to this slice): bind
-- answer reuse to ONE MORE version authority -- QUESTION FAMILY TABLE
-- VERSION (contextfabric.QuestionFamilyTableVersion, "question-family.v1"
-- as of this migration). This dimension guards the family definition
-- table (chaos4632_question_family_registry.go) GateOffersByFamily reads
-- to decide which structure_needs axes a turn-1 disclosure carries:
-- ApplicableAxes, AskOrder, RequireDrivers, RequireRanking, RenderKinds,
-- Budget, and the precedence table that resolves a family in the first
-- place.
--
-- Why reuse MUST be fenced on it, rather than left to age out. S4 is the
-- FIRST slice where the family affects an answer at all -- CHAOS-4632 (S2)
-- shipped the resolver shadow-only, gating nothing, so the fence was
-- provably a no-op there and adding it then would have cold-cached every
-- stored answer for zero safety benefit. tryReuse runs BEFORE Interpret
-- (engine.go) -- a matching stored answer is served verbatim, structure_needs
-- included, without GateOffersByFamily ever re-running. A future edit to
-- the family table (widening or narrowing ApplicableAxes for a family) must
-- not keep serving a pre-edit disclosure from cache for the identical
-- question -- the same class of bug 0022's window_inference_version and
-- 0031's commit_gate_version each closed for their own decision.
--
-- Same shape as 0035's ranking_formula_version addition (itself following
-- 0031's commit_gate_version, 0022's window_inference_version, 0018's
-- identity_normalization_version, and 0015's original five-column
-- precedent): one dedicated, NULLABLE column, a length check constraint,
-- and a replacement reuse-key index carrying it in the position
-- FindReusable filters on. See 0015's own header comment for the full "why
-- nullable, why a dedicated column not blended with an existing one, why
-- the index is replaced not stacked" reasoning -- unchanged here, extended
-- by exactly one more dimension.
--
-- NULL is the pre-CHAOS-4634 row (every row through 0035, including every
-- row CHAOS-4632/S2 itself wrote -- S2 never persisted this dimension).
-- FindReusable's conjunctive equality can never match NULL against the
-- non-empty deployment-current value, so every row written before this
-- migration is permanently excluded from reuse on this dimension -- which
-- is precisely the fence, achieved without a backfill and without a
-- destructive purge.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). Every ALTER below is
-- independently idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS question_family_version TEXT;

-- 128 bounds this column the same generous width as every sibling reuse-key
-- version column (0015's own comment: real values are short literals, e.g.
-- "question-family.v1", well under 40 characters). NULL is exempted (that
-- row never participates in this dimension of reuse) and the empty string
-- is rejected, matching every prior reuse-column migration's NULL-sentinel
-- discipline.
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_question_family_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_question_family_version_length
        CHECK (question_family_version IS NULL OR char_length(question_family_version) BETWEEN 1 AND 128);

-- Replace 0035's ix_acr_cf_investigation_results_reuse_key_v9 with one
-- carrying the new dimension, in the position FindReusable filters on --
-- the old index is DROPPED rather than left beside the new one, mirroring
-- every prior reuse-key column migration's replace-don't-stack reasoning.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v9;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v10
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, identity_normalization_version, graph_epoch, window_inference_version, commit_gate_version, ranking_formula_version, question_family_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
