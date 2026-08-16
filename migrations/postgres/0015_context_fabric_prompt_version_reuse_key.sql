-- CHAOS-3862: bind answer reuse to the INTERPRETATION and SYNTHESIS PROMPT
-- VERSIONS.
--
-- Answer reuse runs BEFORE Interpret (Engine.tryReuse, AC-3782-1's
-- zero-model-call guarantee), and a hit is served without ever calling
-- Synthesize either -- so a reused answer skips BOTH model steps, not
-- just interpretation. Before this migration, the reuse key carried no
-- prompt-version discriminator at all: after any interpretation- or
-- synthesis-prompt bump (e.g. interpretation v6->v7, sol review of
-- 3a244ac), every already-stored answer for the identical question stayed
-- fully reusable for up to the configured staleness window, so a prompt
-- deploy was not a clean cutover for reused answers. Prompt version IS
-- recorded on every fresh execution's ModelExecutionReceipt already --
-- it simply was never a conjunctive reuse discriminator.
--
-- Two dedicated columns, not one, mirroring 0014's embed-identity/
-- retrieval-policy-version split (and NOT folded into model_identity,
-- whose ANY() membership test is disjunctive and cannot carry a second,
-- unrelated conjunctive constraint -- see
-- contextfabric.ReuseKey.InterpretationPromptVersion's doc comment): a
-- reuse miss must stay attributable to interpretation vs. synthesis
-- specifically, not a single blended "some prompt changed" signal.
--
-- Both columns stay NULLABLE, like 0011's reuse columns and 0014's
-- retrieval columns: a row saved with reuse disabled legitimately carries
-- NULL everywhere, and every PRE-migration row holds NULL in both new
-- columns. NULL never satisfies an equality predicate, so a binary
-- running the new conjunctive lookup can never reuse a pre-change row --
-- fail-closed per REPLICA, exactly like every other reuse dimension this
-- table carries.

BEGIN;

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS interpretation_prompt_version TEXT,
    ADD COLUMN IF NOT EXISTS synthesis_prompt_version TEXT;

-- 128 bounds a prompt-version string generously: production values today
-- are short dotted literals like "context-fabric-interpretation.v7" (34
-- chars) and "context-fabric-synthesis.v9" (28 chars), with wide headroom
-- for future growth. NULL is exempted (this row never participates in
-- this dimension of reuse) and the empty string is rejected, matching
-- 0014's no-empty-string discipline and pginvestigation's NULL-sentinel
-- rule for "this row never participates in reuse".
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_interpretation_prompt_version_length
        CHECK (interpretation_prompt_version IS NULL OR char_length(interpretation_prompt_version) BETWEEN 1 AND 128),
    ADD CONSTRAINT ck_acr_cf_investigation_results_synthesis_prompt_version_length
        CHECK (synthesis_prompt_version IS NULL OR char_length(synthesis_prompt_version) BETWEEN 1 AND 128);

-- Replace 0014's ix_acr_cf_investigation_results_reuse_key_v3 with one
-- carrying both new dimensions, in the position FindReusable filters on.
-- The old index is DROPPED rather than left beside the new one, mirroring
-- 0013's and 0014's replace-don't-stack reasoning verbatim: it indexes a
-- prefix of the same columns, so leaving it would cost writes for nothing
-- while offering the planner an index that cannot answer the new
-- predicates.
--
-- The trailing (created_at DESC, result_id DESC) and the partial WHERE
-- match 0011's reasoning exactly -- created_at is the staleness authority
-- and result_id the stable tie-break.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v3;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v4
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;

COMMIT;
