-- CHAOS-3833: bind answer reuse to the EMBED RETRIEVAL IDENTITY and the
-- RETRIEVAL POLICY VERSION (embed-text spec v2 §4, review P1-2 closure).
--
-- Today the reuse key carries no embed discriminator at all. A Layer-B/C
-- semantic change (a new embed-text composition, a rune-cap or body-gate
-- flip) moves the node-side identity stamp -- so the read fence degrades
-- the organization to lexical until the prescribed rebuild -- but nothing
-- in a STORED answer's key changes, so during the deploy->rebuild window
-- (and for pre-deploy rows generally) reuse can keep serving answers that
-- were derived from old-text vectors. The rebuild epoch only bumps when
-- the operator eventually rebuilds; the key must move WITH the deploy.
--
-- These are two DEDICATED, equality-compared conjunctive dimensions, NOT
-- entries appended to the disjunctive model-identity chain. The chain's
-- members are ALTERNATIVES (`model_identity = ANY(...)`, see
-- contextfabric.ReuseKey.ModelIdentities): appending an `embed:` entry to
-- the lookup array changes nothing for old rows, because a row stored
-- under a bare LLM identity still matches any array that CONTAINS that
-- identity -- the embed tag would never be compared at all. A disjunctive
-- dimension cannot carry a conjunctive constraint (spec §4, review round
-- 2's sole surviving finding).
--
-- Two columns, not one: a reuse miss must stay attributable to a specific
-- dimension (embed-text change vs retrieval-policy change) -- the same
-- per-condition-diagnosability rule that keeps TimeAxisKey out of
-- QuestionHash (contextfabric.ReuseKey's doc comment). The policy version
-- moves when tau/K/HNSW defaults change, which reinterprets EXISTING
-- vectors: no node stamp moves, so without its own persisted dimension a
-- stored answer derived under the old policy would remain reusable.
--
-- Both columns stay NULLABLE, like 0011's reuse columns and unlike 0013's
-- time_axis_key: a row saved with reuse disabled legitimately carries NULL
-- everywhere, and every PRE-migration row holds NULL in both new columns.
-- NULL never satisfies an equality predicate, so a binary running the new
-- conjunctive lookup can never reuse a pre-change row -- fail-closed
-- per REPLICA. Per FLEET it is gated by the two-phase rollout in the
-- deployment runbook (docs/operations.md): an undrained pre-0014 binary
-- still runs the predicate-less query and would happily reuse pre-change
-- answers, because the migration framework deliberately tolerates an older
-- binary against a newer schema mid-rollout (migrations/postgres/verify.go).
-- Semantic (Layer-B/C) configuration must not activate until this
-- migration is applied AND no predicate-less replica serves traffic.

BEGIN;

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS embed_retrieval_identity TEXT,
    ADD COLUMN IF NOT EXISTS retrieval_policy_version TEXT;

-- 640, following 0011's model_identity arithmetic: the identity half is
-- "<provider>/<model>" bounded at 513 (256 + '/' + 256), then '#' plus a
-- bounded canonical composition-tag literal (template version, rune cap,
-- body gate, prefix selector -- e.g. "t2:r2000:b1:pnone"), which 126
-- spare characters cover with room for every declared component. The
-- no-embedder sentinel is the literal 'none' -- never the empty string,
-- matching 0011's no-empty-string discipline and pginvestigation's
-- NULL-sentinel rule for "this row never participates in reuse".
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_embed_retrieval_identity_length
        CHECK (embed_retrieval_identity IS NULL OR char_length(embed_retrieval_identity) BETWEEN 1 AND 640),
    ADD CONSTRAINT ck_acr_cf_investigation_results_retrieval_policy_version_length
        CHECK (retrieval_policy_version IS NULL OR char_length(retrieval_policy_version) BETWEEN 1 AND 64);

-- Replace 0013's ix_acr_cf_investigation_results_reuse_key_v2 with one
-- carrying both new dimensions, in the position FindReusable filters on.
-- The old index is DROPPED rather than left beside the new one, mirroring
-- 0013's replace-don't-stack reasoning verbatim: it indexes a prefix of
-- the same columns, so leaving it would cost writes for nothing while
-- offering the planner an index that cannot answer the new predicates.
--
-- The trailing (created_at DESC, result_id DESC) and the partial WHERE
-- match 0011's reasoning exactly -- created_at is the staleness authority
-- and result_id the stable tie-break.
DROP INDEX IF EXISTS acr.ix_acr_cf_investigation_results_reuse_key_v2;

CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key_v3
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, time_axis_key, embed_retrieval_identity, retrieval_policy_version, created_at DESC, result_id DESC)
    WHERE question_hash IS NOT NULL;

COMMIT;
