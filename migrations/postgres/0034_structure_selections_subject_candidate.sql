-- CHAOS-4355 (lane-4355-vocab). acr.context_fabric_structure_selections
-- (migration 0024) is a DIFFERENT table from
-- acr.context_fabric_structure_supersession_claims (migration 0023,
-- widened by migration 0033) -- the two tables mirror the same member
-- vocabulary but each was given its own CHECK constraint (this package's
-- own doc comment: pgstructureselection deliberately mirrors, never
-- shares, pgclarification's per-table-independent-vocabulary convention).
-- 0033 widened ONLY the supersession-claims table's CHECK; this table's
-- ck_acr_cf_structure_selections_member_vocabulary was never touched after
-- 0024 pinned it at the original three values. CHAOS-4012 (#242) later
-- added a 5th ContextFabricStructureNeedKind, subject_candidate
-- (internal/contracts/v1/context_fabric_structure_types.go), and
-- canonicalizeStructure's own receipt-member loop (structure.go) has
-- treated subject_candidate as one of its four StructureSelectionEvent
-- members (alongside expected_kind/subject_anchor/subject_handle) since
-- that change -- so every redeemed subject_candidate receipt has been
-- failing pgstructureselection.validateEvent's Go-side vocabulary check
-- with "member \"subject_candidate\" is not in the closed vocabulary",
-- confirmed live on the kiac acr-pilot cluster (structure selection
-- capture failed) on every candidate-offer redemption. This migration
-- closes the gap this table actually has.
--
-- window is deliberately NOT added here. StructureSelectionEvent.Member's
-- own doc comment (structure_capture.go) and canonicalizeStructure's own
-- receipt-member loop (structure.go) agree: window confirmation is
-- canonicalizeEvidenceWindow's own code path, never
-- canonicalizeStructure's, so a window value can never legitimately reach
-- this table -- it rides its own, separately designed WindowSelectionEvent
-- (design brief §2.4), not implemented against this sink. Widening this
-- CHECK to admit "window" would accept a value production code never
-- emits here and silently defeat the closed-vocabulary guard's own
-- purpose; TestInsertContext_RejectsMalformedEventBeforeAnyInsert pins
-- member="window" as the rejected example for exactly this reason and
-- stays unchanged by this migration.
--
-- Additive only: widens the CHECK to also allow subject_candidate. No
-- existing row is touched, no column changes, no data migrates.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction).

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_member_vocabulary;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_member_vocabulary
        CHECK (member IN ('expected_kind', 'subject_anchor', 'subject_handle', 'subject_candidate'));
