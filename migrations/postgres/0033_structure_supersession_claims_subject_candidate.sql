-- CHAOS-4333. CHAOS-4012 (#242, "ranked-candidate-list structure offer
-- axis") added a 5th ContextFabricStructureNeedKind, subject_candidate
-- (internal/contracts/v1/context_fabric_structure_types.go), but never
-- widened this table's CHECK -- migration 0023 pinned the vocabulary at
-- exactly the original four (expected_kind / subject_anchor /
-- subject_handle / window). Every turn where a caller redeems a
-- subject_candidate receipt (Engine's structureSupersessionClaims,
-- pginvestigation/store.go) has been failing the INSERT with
-- ck_acr_cf_structure_supersession_member_vocabulary ever since --
-- confirmed live 2026-08-26 on the kiac acr-pilot cluster: Postgres logged
-- "violates check constraint ck_acr_cf_structure_supersession_member_
-- vocabulary" for a failing row with member=subject_candidate, which
-- pginvestigation.sanitizeError then wrapped into contextfabric.
-- ErrUnavailable (failure_stage=persistence, failure_classification=
-- dependency_unavailable, HTTP 503) -- the Postgres error itself was
-- correct and the only place the real cause was ever recorded; ACR's own
-- wire-facing failure code never carries it (by design, see
-- pginvestigation.sanitizeError's own doc comment), which is why this sat
-- undiagnosed until the DB's own log was read directly.
--
-- Additive only: widens the CHECK to also allow subject_candidate. No
-- existing row is touched, no column changes, no data migrates.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction).

ALTER TABLE acr.context_fabric_structure_supersession_claims
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_supersession_member_vocabulary;
ALTER TABLE acr.context_fabric_structure_supersession_claims
    ADD CONSTRAINT ck_acr_cf_structure_supersession_member_vocabulary
        CHECK (member IN ('expected_kind', 'subject_anchor', 'subject_handle', 'window', 'subject_candidate'));
