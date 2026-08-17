-- CHAOS-3859 (capture-only phase, chris-ratified 2026-08-16): persist
-- clarification-selection events -- (org, question hash, offered candidate
-- set, selected candidate, timestamp, selection provenance, pipeline
-- context) -- for every caller-confirmed resolution of a prior
-- clarification_required result. ~85-90% of corpus questions end at
-- clarification_required with candidate options; each selection is a
-- labeled (question phrasing -> correct subject) pair at real production
-- distribution -- the training signal CHAOS-3860's learning-loop epic
-- depends on. Today those selections vanish: PriorSubjectReceipts
-- (migration 0009's investigation_results, read back via
-- Engine.resolvePriorSubjectHints) resolves a receipt back into a
-- re-authorized SubjectHint, but records no fact anywhere that "candidate X
-- of N offered was the one chosen."
--
-- HARD SCOPE BOUNDARY (the ticket's own directive): this migration and the
-- write path it backs are CAPTURE ONLY. No feedback loop, no learned
-- aliases, no threshold consumption -- nothing reads this table back yet.
-- Those are separately chris-ratified follow-on phases.
--
-- No raw question TEXT: only question_hash (contextfabric.QuestionHash,
-- the SAME canonicalizing SHA-256 already used for answer-reuse keys,
-- answer_reuse.go) is stored -- the ticket's own field list says "question
-- hash/features", not text, and this migration follows that literally. (No
-- repo privacy doc mandates hash-only for Context Fabric question text --
-- acr.context_fabric_investigation_results' immutable payload already
-- stores it -- but the ticket's explicit design directive governs this
-- NEW table regardless.)
--
-- No subject LABEL either (sol review F2): offered_candidates carries
-- receipt ids, subject kinds/canonical ids, ranks, and confidences --
-- never SubjectRef.Label, which is caller-facing display TEXT (e.g. an
-- incident title) with no length bound of its own. ids/kinds/ranks/
-- confidences are the full training signal this phase needs; a label is
-- re-derivable from the subject id at consumption time by whichever
-- future phase actually needs display text, which this one is not.
--
-- Shape mirrors 0010's context_fabric_model_execution_receipts precedent
-- exactly: an insert-only sink table with a handful of indexed/queryable
-- columns (org_id, question_hash, captured_at) plus JSONB blobs for the
-- structured detail (offered_candidates, pipeline_context) that doesn't
-- need its own index yet and, per chris's own framing ("the sweep knobs
-- may soon vary"), should stay schema-flexible rather than earning a new
-- migration every time a pipeline dimension is added. The primary key
-- ALSO mirrors that precedent (sol review F1): every other table in this
-- schema uses an application-generated TEXT/UUID key, never a
-- database-generated BIGSERIAL/IDENTITY sequence -- selection_id follows
-- receipt_id's exact idiom (a Go-generated UUIDv4 string), which
-- incidentally means this table needs no SEQUENCE privilege grant at all,
-- only INSERT on the table itself.
--
-- No inline BEGIN/COMMIT, and every constraint is idempotent
-- (DROP CONSTRAINT IF EXISTS + ADD CONSTRAINT) -- CHAOS-3862 migration
-- 0015's sol-review lesson: migrations/postgres/runner.go's applyMigration
-- already wraps this whole file in its own transaction, and an inline
-- COMMIT would land the DDL as its own premature commit (Postgres has no
-- true nested transactions), leaving a failure before the runner's own
-- history INSERT to wedge Runner.Up forever on a non-idempotent ADD
-- CONSTRAINT retry.

CREATE TABLE IF NOT EXISTS acr.context_fabric_clarification_selections (
    selection_id                  TEXT NOT NULL PRIMARY KEY,
    -- org_id is the SAME auth boundary as investigation results
    -- (Principal.OrgID) -- every read of this table MUST filter by it;
    -- see pgclarification's own doc comment for the query-side contract.
    org_id                        TEXT NOT NULL,
    -- captured_at is the APP clock at the moment Engine observed the
    -- selection (Engine's own e.now(), the same injectable clock every
    -- other Engine timestamp uses), NOT the DB clock -- unlike
    -- investigation_results' created_at, nothing here needs to be immune
    -- to app-clock skew (no staleness-window security decision reads this
    -- column), and using the same clock Engine uses everywhere else keeps
    -- this event's timestamp comparable to a same-call InvestigationResult's
    -- own GeneratedAt.
    captured_at                   TIMESTAMPTZ NOT NULL,
    -- question_hash = contextfabric.QuestionHash(priorResult.Question) --
    -- the canonicalized-question SHA-256 hex digest (64 hex chars, always).
    question_hash                 TEXT NOT NULL,
    -- prior_result_id is the clarification_required InvestigationResult
    -- OfferedCandidates was read from -- the join key back to that
    -- immutable row (acr.context_fabric_investigation_results) for
    -- anything this capture-only phase does not itself duplicate (its own
    -- Versions, GeneratedAt, save-time reuse-key columns).
    prior_result_id               TEXT NOT NULL,
    -- selected_receipt_id/selected_subject_kind/selected_subject_canonical_id
    -- are denormalized out of offered_candidates below purely for cheap
    -- querying without JSONB unpacking -- mirroring
    -- investigation_results' own payload-blob-plus-indexed-columns shape
    -- (0009/0011). The full candidate entry, including these same values,
    -- still lives inside offered_candidates too.
    selected_receipt_id           TEXT NOT NULL,
    selected_subject_kind         TEXT NOT NULL,
    selected_subject_canonical_id TEXT NOT NULL,
    -- selection_provenance is a BEST-EFFORT human-vs-agent proxy over a
    -- CLOSED vocabulary (sol review F5 -- was a free-form
    -- "credential_"+ConsumerInfo.Surface concatenation, which both (a)
    -- could silently drop a valid 200-char Surface past this column's
    -- bound, and (b) could carry arbitrary caller-supplied content into
    -- the table). See contextfabric.clarificationSelectionProvenance's
    -- doc comment for exactly what each value derives from and why this
    -- is a proxy, not a classification. CHAOS-3860's stratification
    -- requirement is the reason this column exists at all; do not read
    -- more precision into it than the doc comment claims.
    selection_provenance          TEXT NOT NULL,
    -- offered_candidates is the COMPLETE candidate set the prior result
    -- offered (subject ids/kinds, ranks, confidences, receipt ids -- NOT
    -- labels, see header) -- a training signal needs the negative
    -- examples (candidates NOT chosen) as much as the positive one, so
    -- this is never trimmed to just the selection.
    offered_candidates            JSONB NOT NULL,
    -- pipeline_context is the deployment-CURRENT pipeline/gate config
    -- active at the MOMENT this selection was observed (prompt versions,
    -- retrieval identity, model identities, ...) -- the exact CHAOS-3833/
    -- 3862 reuse-key dimensions Engine already carries as its own fields,
    -- reused here rather than a parallel shape. JSONB, not columns, on
    -- purpose: chris's own framing is that these sweep knobs "may soon
    -- vary," and a JSONB blob absorbs a new dimension without a schema
    -- migration every time CHAOS-3857-style calibration work adds one.
    pipeline_context              JSONB NOT NULL,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE acr.context_fabric_clarification_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_clarification_selections_selection_id_length;
ALTER TABLE acr.context_fabric_clarification_selections
    ADD CONSTRAINT ck_acr_cf_clarification_selections_selection_id_length
        -- Matches 0010's ck_acr_cf_model_receipts_receipt_id_length bound
        -- exactly (8..256): a UUIDv4 string is 36 characters, comfortably
        -- inside it, with room for a future ID-shape change.
        CHECK (char_length(selection_id) BETWEEN 8 AND 256);

ALTER TABLE acr.context_fabric_clarification_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_clarification_selections_org_id_length;
ALTER TABLE acr.context_fabric_clarification_selections
    ADD CONSTRAINT ck_acr_cf_clarification_selections_org_id_length
        CHECK (char_length(org_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_clarification_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_clarification_selections_question_hash_length;
ALTER TABLE acr.context_fabric_clarification_selections
    ADD CONSTRAINT ck_acr_cf_clarification_selections_question_hash_length
        -- Exactly 64: a SHA-256 hex digest's fixed length, not a generic
        -- bound -- a row that fails this is definitionally not a real
        -- contextfabric.QuestionHash output.
        CHECK (char_length(question_hash) = 64);

ALTER TABLE acr.context_fabric_clarification_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_clarification_selections_prior_result_id_length;
ALTER TABLE acr.context_fabric_clarification_selections
    ADD CONSTRAINT ck_acr_cf_clarification_selections_prior_result_id_length
        CHECK (char_length(prior_result_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_clarification_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_clarification_selections_receipt_id_length;
ALTER TABLE acr.context_fabric_clarification_selections
    ADD CONSTRAINT ck_acr_cf_clarification_selections_receipt_id_length
        CHECK (char_length(selected_receipt_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_clarification_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_clarification_selections_subject_kind_length;
ALTER TABLE acr.context_fabric_clarification_selections
    ADD CONSTRAINT ck_acr_cf_clarification_selections_subject_kind_length
        CHECK (char_length(selected_subject_kind) BETWEEN 1 AND 64);

ALTER TABLE acr.context_fabric_clarification_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_clarification_selections_subject_id_length;
ALTER TABLE acr.context_fabric_clarification_selections
    ADD CONSTRAINT ck_acr_cf_clarification_selections_subject_id_length
        CHECK (char_length(selected_subject_canonical_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_clarification_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_clarification_selections_provenance_vocabulary;
ALTER TABLE acr.context_fabric_clarification_selections
    ADD CONSTRAINT ck_acr_cf_clarification_selections_provenance_vocabulary
        -- CLOSED vocabulary (sol review F5), matching
        -- contextfabric.clarificationSelectionProvenance's own exhaustive
        -- switch exactly -- a value this CHECK rejects means the Go and
        -- SQL vocabularies have drifted, not that a real caller supplied
        -- something unexpected (unknown/free-form callers already map to
        -- 'credential_other' in Go before reaching this INSERT).
        CHECK (selection_provenance IN ('web_assertion', 'credential_mcp', 'credential_workbench', 'credential_other'));

-- org_id-first, matching every other org-scoped Context Fabric table's
-- lookup shape: every real read of this table filters by org first.
-- captured_at DESC serves a "most recent selections for this org" scan;
-- question_hash serves a future (not-yet-built, capture-only phase) "what
-- did organizations pick for this question" join -- indexed now because
-- adding an index later is a second migration for free information this
-- one already has.
CREATE INDEX IF NOT EXISTS ix_acr_cf_clarification_selections_org_captured
    ON acr.context_fabric_clarification_selections (org_id, captured_at DESC);

CREATE INDEX IF NOT EXISTS ix_acr_cf_clarification_selections_org_question
    ON acr.context_fabric_clarification_selections (org_id, question_hash);
