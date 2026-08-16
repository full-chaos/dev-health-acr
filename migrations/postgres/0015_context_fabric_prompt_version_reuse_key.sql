-- CHAOS-3862: bind answer reuse to five MORE version authorities the
-- ReuseKey doc comment classifies in full: INTERPRETATION and SYNTHESIS
-- PROMPT VERSION (round 1), and QUERY VERSION, CANONICAL SERVICE VERSION,
-- and MODEL-OUTPUT SCHEMA VERSION (round 2, sol review's class-close on
-- "version authority missing from ReuseKey" -- a defect class that had by
-- then hit this codebase three times independently: CHAOS-3833/3834's
-- embed retrieval identity, this ticket's own round 1, and this round).
--
-- Answer reuse runs BEFORE Interpret (Engine.tryReuse, AC-3782-1's
-- zero-model-call guarantee), and a hit is served without ever calling
-- Synthesize either -- so a reused answer skips BOTH model steps, not
-- just interpretation. Before this migration, the reuse key carried no
-- discriminator for any of these five: after an interpretation- or
-- synthesis-prompt bump, a ClickHouse query-shape change, a canonical
-- fact-registry contract bump, or a genkit model-output schema bump,
-- every already-stored answer for the identical question stayed fully
-- reusable for up to the configured staleness window. All five are
-- already recorded on a fresh result/receipt today (see
-- contextfabric/reuse_key_completeness_test.go for the durable,
-- reflection-based proof that this list is now exhaustive) -- none of
-- them were ever a conjunctive reuse discriminator.
--
-- Five dedicated columns, not fewer, mirroring 0014's embed-identity/
-- retrieval-policy-version split: a reuse miss must stay attributable to
-- ONE specific dimension, not a blended "something changed" signal, and
-- none of these compose into model_identity's ANY() membership test
-- (disjunctive; cannot carry a conjunctive constraint) or into each
-- other (see ReuseKey's doc comment for the full per-dimension
-- reasoning).
--
-- All five columns stay NULLABLE, like every prior reuse-key column
-- migration: a row saved with reuse disabled legitimately carries NULL
-- everywhere, and every PRE-migration row holds NULL in all five new
-- columns. NULL never satisfies an equality predicate, so a binary
-- running the new conjunctive lookup can never reuse a pre-change row --
-- fail-closed per REPLICA, exactly like every other reuse dimension this
-- table carries.
--
-- No inline BEGIN/COMMIT (sol review round 2 F3): migrations/postgres/
-- runner.go's applyMigration already wraps this entire file's SQL in its
-- own transaction (tx.ExecContext(ctx, migration.SQL) followed by the
-- schema_migrations history INSERT, then ONE tx.Commit()) -- an inline
-- COMMIT here would land the DDL as its own separate, premature commit
-- (Postgres has no true nested transactions; a COMMIT inside an
-- already-open transaction just ends it early), leaving the runner's own
-- subsequent history INSERT to run outside any transaction. A failure
-- between that inline COMMIT and the runner's own commit then leaves
-- schema@0015/history@0014 permanently: the DDL is committed, the retry
-- reapplies this same file from the top, and any NON-idempotent
-- statement in it (a plain ADD CONSTRAINT with no guard) fails forever
-- with "constraint already exists," wedging Runner.Up with no available
-- recovery short of manual intervention. Matches 0011's own shape (no
-- inline transaction control) -- 0013/0014 added one that this migration
-- does not repeat.
--
-- Every ALTER below is ALSO independently idempotent (IF NOT EXISTS /
-- DROP-before-ADD for named constraints, matching 0013's own
-- DROP CONSTRAINT IF EXISTS + ADD CONSTRAINT idiom), so even a
-- hypothetical partial application from an unrelated failure mode can
-- always be safely retried from the top -- belt and suspenders with the
-- transaction fix above, not a substitute for it.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS interpretation_prompt_version TEXT,
    ADD COLUMN IF NOT EXISTS synthesis_prompt_version TEXT,
    ADD COLUMN IF NOT EXISTS query_version TEXT,
    ADD COLUMN IF NOT EXISTS canonical_service_version TEXT,
    ADD COLUMN IF NOT EXISTS model_output_schema_version TEXT;

-- 128 bounds each column generously: every production value today is a
-- short dotted literal (e.g. "context-fabric-interpretation.v7",
-- "devhealthfacts.clickhouse.v1", "context-fabric-facts.v1",
-- "context-fabric-model-output.v1"), all well under 40 characters, with
-- wide headroom for growth. NULL is exempted (this row never
-- participates in this dimension of reuse) and the empty string is
-- rejected, matching every prior reuse-column migration's NULL-sentinel
-- discipline. Constraint names abbreviate ("interp"/"synth"/"canon_svc")
-- to stay under Postgres's 63-byte identifier limit -- the straightforward
-- "interpretation_prompt_version"-length name does NOT fit alongside this
-- table's existing "ck_acr_cf_investigation_results_" prefix and
-- "_length" suffix (68 bytes, silently truncated by Postgres rather than
-- rejected, which is worse: a name that looks right in this file would
-- not be the name actually in the schema).
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_interp_prompt_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_interp_prompt_version_length
        CHECK (interpretation_prompt_version IS NULL OR char_length(interpretation_prompt_version) BETWEEN 1 AND 128);

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_synth_prompt_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_synth_prompt_version_length
        CHECK (synthesis_prompt_version IS NULL OR char_length(synthesis_prompt_version) BETWEEN 1 AND 128);

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_query_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_query_version_length
        CHECK (query_version IS NULL OR char_length(query_version) BETWEEN 1 AND 128);

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_canon_svc_version_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_canon_svc_version_length
        CHECK (canonical_service_version IS NULL OR char_length(canonical_service_version) BETWEEN 1 AND 128);

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_model_output_schema_ver_length;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_model_output_schema_ver_length
        CHECK (model_output_schema_version IS NULL OR char_length(model_output_schema_version) BETWEEN 1 AND 128);

-- Replace 0014's ix_acr_cf_investigation_results_reuse_key_v3 with one
-- carrying all five new dimensions, in the position FindReusable filters
-- on. The old index is DROPPED rather than left beside the new one,
-- mirroring 0013's and 0014's replace-don't-stack reasoning verbatim: it
-- indexes a prefix of the same columns, so leaving it would cost writes
-- for nothing while offering the planner an index that cannot answer the
-- new predicates.
--
-- The trailing (created_at DESC, result_id DESC) and the partial WHERE
-- match 0011's reasoning exactly -- created_at is the staleness authority
-- and result_id the stable tie-break.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v3;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v4
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, interpretation_prompt_version, synthesis_prompt_version, query_version, canonical_service_version, model_output_schema_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;
