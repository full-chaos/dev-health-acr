-- CHAOS-3927 P4 (pivot-intent design brief, DESIGN-FINAL, §2.4/§3.1):
-- extends the CHAOS-3859 clarification-selection CAPTURE pipeline (0016,
-- shipped 2026-08-17, subjects only) with a SECOND, parallel event table
-- recording structure-offer confirmations -- every kindr_/ancr_/handr_
-- receipt that resolves cleanly against a member of a prior result's own
-- StructureNeeds offer set is a labeled (question shape -> confirmed
-- structure member) pair at real production distribution, the training
-- signal design brief §3 (the Bridge learning loop) depends on.
--
-- Same HARD SCOPE BOUNDARY as 0016: this migration and the write path it
-- backs are CAPTURE ONLY. No feedback loop, no learned priors -- nothing
-- reads this table back yet. Curation/consultation (P5, design brief §4's
-- sharding table) is separately chris-ratified.
--
-- No raw question TEXT: only question_hash, mirroring 0016 exactly (that
-- migration's own header comment covers the reasoning, unchanged here).
--
-- Shape mirrors 0016 closely (a parallel event type per design brief §2.4
-- -- "extend 3859's pattern, parallel event types, never field-grafts" --
-- not a field-graft onto context_fabric_clarification_selections), with
-- one addition 0016 has no analogue for: member, the closed
-- StructureNeedKind vocabulary value this selection confirms
-- (expected_kind/subject_anchor/subject_handle -- window rides its own,
-- separately designed WindowSelectionEvent per §2.4, so it never reaches
-- this table).
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). CREATE TABLE IF NOT
-- EXISTS and idempotent constraint adds match every prior migration.

CREATE TABLE IF NOT EXISTS acr.context_fabric_structure_selections (
    selection_id                TEXT NOT NULL PRIMARY KEY,
    org_id                       TEXT NOT NULL,
    captured_at                  TIMESTAMPTZ NOT NULL,
    question_hash                TEXT NOT NULL,
    prior_result_id               TEXT NOT NULL,
    -- member is the closed StructureNeedKind vocabulary this selection
    -- confirms -- CHECK below matches
    -- contractsv1.ValidContextFabricStructureNeedKind's own switch, minus
    -- "window" (window selections ride their own, separately designed
    -- event table, never this one).
    member                        TEXT NOT NULL,
    -- selected_receipt_id/selected_applied_value are denormalized out of
    -- "offered" below purely for cheap querying without JSONB unpacking,
    -- mirroring 0016's own selected_receipt_id/selected_subject_kind/
    -- selected_subject_canonical_id precedent exactly. selected_applied_value
    -- is the ONE typed id/enum value the redeemed offer represented (a
    -- SubjectKind string, a canonical anchor id, or a handle's literal
    -- value) -- never caller-facing display text, the same sink discipline
    -- every structure-offer echo in this schema already applies.
    selected_receipt_id           TEXT NOT NULL,
    selected_applied_value        TEXT NOT NULL,
    -- accepted reports whether the redeemed offer was the TOP-RANKED
    -- (rank 0) option in "offered" -- design brief §2.4: "Accepted
    -- (selected == engine/prior proposal)".
    accepted                      BOOLEAN NOT NULL,
    -- selection_mode is design brief §2.4's own closed vocabulary:
    -- human_panel/agent_receipt/agent_explicit/agent_explicit_echo. Only
    -- the first two are reachable through this migration's own write path
    -- today (structure.captureStructureSelection's own doc comment covers
    -- why) -- the CHECK below still enumerates all four so a future P3
    -- landing needs no migration to start writing the other two.
    selection_mode                TEXT NOT NULL,
    -- selection_provenance reuses 0016's own best-effort human-vs-agent
    -- proxy verbatim -- SAME closed vocabulary, SAME CHECK.
    selection_provenance          TEXT NOT NULL,
    -- offered is the COMPLETE option set the prior result's StructureNeeds
    -- offered for member (ids/ranks/enums/values only, never a label) --
    -- mirrors 0016's own offered_candidates precedent exactly.
    offered                       JSONB NOT NULL,
    -- pipeline_context mirrors 0016's own column exactly -- the
    -- deployment-CURRENT CHAOS-3833/3862 reuse-key dimensions, JSONB so a
    -- future sweep-knob addition never forces a migration here either.
    pipeline_context              JSONB NOT NULL,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_selection_id_length;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_selection_id_length
        CHECK (char_length(selection_id) BETWEEN 8 AND 256);

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_org_id_length;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_org_id_length
        CHECK (char_length(org_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_question_hash_length;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_question_hash_length
        CHECK (char_length(question_hash) = 64);

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_prior_result_id_length;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_prior_result_id_length
        CHECK (char_length(prior_result_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_member_vocabulary;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_member_vocabulary
        CHECK (member IN ('expected_kind', 'subject_anchor', 'subject_handle'));

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_receipt_id_length;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_receipt_id_length
        CHECK (char_length(selected_receipt_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_applied_value_length;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_applied_value_length
        CHECK (char_length(selected_applied_value) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_mode_vocabulary;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_mode_vocabulary
        CHECK (selection_mode IN ('human_panel', 'agent_receipt', 'agent_explicit', 'agent_explicit_echo'));

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_provenance_vocabulary;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_provenance_vocabulary
        CHECK (selection_provenance IN ('web_assertion', 'credential_mcp', 'credential_workbench', 'credential_other'));

-- org_id-first, matching 0016's own lookup shape exactly: every real read
-- of this table filters by org first.
CREATE INDEX IF NOT EXISTS ix_acr_cf_structure_selections_org_captured
    ON acr.context_fabric_structure_selections (org_id, captured_at DESC);

CREATE INDEX IF NOT EXISTS ix_acr_cf_structure_selections_org_question
    ON acr.context_fabric_structure_selections (org_id, question_hash);
