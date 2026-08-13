-- CHAOS-3782: answer reuse from the immutable Context Fabric result store
-- (TRD §19.7). Two additive pieces, neither of which touches the
-- immutable `payload` column on acr.context_fabric_investigation_results:
--
-- 1. Reuse-key columns on the results table itself. Populated ONCE, at
--    INSERT time, by the same Save call that writes the immutable
--    payload -- never updated afterward. result_id stays random and
--    first-insert-wins is unchanged; these columns are purely an indexed
--    projection of facts the payload already carries (question,
--    Versions.ContractVersion, Versions.ProjectionVersion,
--    Versions.ModelIdentity, Coverage.Sources[].Watermark), so a reuse
--    lookup does not need to unmarshal and validate every candidate
--    row's payload just to filter candidates out.
--
--    source_watermarks snapshots, at save time, the CURRENT
--    backend_watermark of every source configured for the organization's
--    graph projection (acr.context_fabric_projection_checkpoints) -- not
--    only the sources this particular question happened to touch. TRD
--    §19.7.3 condition 3 requires "every source the stored result used";
--    nothing today attributes which projection sources fed a given
--    answer's graph context, so binding to the org's FULL configured
--    source set is the conservative, fail-closed reading: it can only
--    ever invalidate MORE eagerly than a precise per-question binding
--    would, never less. See D15 (TRD §19.2) -- this conservatism is the
--    load-bearing half of the staleness bound, alongside the age window.
--
-- 2. A separate, small, MUTABLE table recording the most recent completed
--    projection rebuild per organization. A rebuild can, in principle,
--    reproduce the exact same backend_watermark string it purged (the
--    cursor's event-time semantics do not guarantee a different value),
--    so watermark equality ALONE cannot prove a stored result survived a
--    rebuild -- TRD §19.7.3's own text names this. invalidated_at is
--    compared against a candidate row's generated_at at lookup time
--    (candidate eligible only if generated_at > invalidated_at); nothing
--    on the immutable results table is ever rewritten to invalidate it.
CREATE TABLE IF NOT EXISTS acr.context_fabric_reuse_invalidations (
    org_id         TEXT NOT NULL PRIMARY KEY,
    invalidated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ck_acr_cf_reuse_invalidations_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256)
);

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS question_hash       TEXT,
    ADD COLUMN IF NOT EXISTS contract_version     TEXT,
    ADD COLUMN IF NOT EXISTS projection_version   TEXT,
    ADD COLUMN IF NOT EXISTS model_identity        TEXT,
    ADD COLUMN IF NOT EXISTS source_watermarks     JSONB;

ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_question_hash_length
        CHECK (question_hash IS NULL OR char_length(question_hash) = 64),
    ADD CONSTRAINT ck_acr_cf_investigation_results_contract_version_length
        CHECK (contract_version IS NULL OR char_length(contract_version) BETWEEN 1 AND 256),
    ADD CONSTRAINT ck_acr_cf_investigation_results_projection_version_length
        CHECK (projection_version IS NULL OR char_length(projection_version) BETWEEN 1 AND 256),
    ADD CONSTRAINT ck_acr_cf_investigation_results_model_identity_length
        CHECK (model_identity IS NULL OR char_length(model_identity) BETWEEN 1 AND 256);

-- Reuse candidate lookup: given (org, canonicalized-question hash,
-- contract version, projection version, model identity), find the most
-- recently generated eligible row first. A row with a NULL question_hash
-- (every row written before this migration shipped) never participates --
-- the partial index excludes it, and Engine's reuse gate treats "no
-- candidate found" identically to "reuse disabled for this call", which
-- is the fail-closed default in either case.
CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_reuse_key
    ON acr.context_fabric_investigation_results
        (org_id, question_hash, contract_version, projection_version, model_identity, generated_at DESC)
    WHERE question_hash IS NOT NULL;
