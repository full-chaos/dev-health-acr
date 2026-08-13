-- CHAOS-3781: bind answer reuse to the requested TIME AXIS.
--
-- Migration 0011 keyed reuse on (org, question_hash, contract_version,
-- projection_version, model_identity). That was sound while every stored
-- result was implicitly a current-axis answer -- non-current axes were
-- refused outright, so a historical answer could never be stored.
--
-- CHAOS-3781 makes historical answers storable, and the identical question
-- text asked "as of March" and "as of June" would collapse onto ONE key,
-- serving a June answer for a March question. That is a silent wrong
-- answer, strictly worse than the refusal CHAOS-3781 removed.
--
-- See contextfabric.ReuseKey.TimeAxisKey and TimeAxisKeyFor for the key's
-- canonicalization, and in particular for why a current-axis question must
-- map to a fixed literal rather than to the wall clock.

BEGIN;

-- DEFAULT 'current' backfills every existing row CORRECTLY, not merely
-- conveniently: every row already in this table was produced while
-- non-current axes were refused, so each one genuinely IS a current-axis
-- answer. No historical row can exist yet.
--
-- NOT NULL is therefore safe here, unlike migration 0011's reuse columns,
-- which had to stay nullable because a row may legitimately have been
-- saved with reuse disabled. This column describes the QUESTION, which
-- every row has, not the reuse machinery's state at save time.
ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS time_axis_key TEXT NOT NULL DEFAULT 'current';

-- The key is built from a closed axis vocabulary plus epoch-nanosecond
-- integers, so it is short and bounded. The CHECK guards against a caller
-- that bypasses TimeAxisKeyFor; an empty string in particular must never
-- be stored, because TimeAxisKeyFor returns "" precisely for a malformed
-- historical context that must never become reusable.
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_time_axis_key;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_time_axis_key
        CHECK (char_length(time_axis_key) BETWEEN 1 AND 128);

-- Replace 0011's ix_acr_cf_investigation_results_reuse_key with one
-- carrying the new dimension, in the same position FindReusable filters
-- on. The old index is DROPPED rather than left beside the new one: it
-- indexes a prefix of the same columns, so leaving it would cost writes
-- for nothing while offering the planner an index that cannot answer the
-- axis predicate at all.
--
-- The trailing (created_at DESC, result_id DESC) and the partial WHERE
-- match 0011's reasoning exactly -- see that migration's comment for why
-- created_at is the staleness authority and result_id the stable
-- tie-break.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v2
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;

COMMIT;
