-- CHAOS-3927 P4 (pivot-intent design brief, DESIGN-FINAL, §2.1 offer-
-- supersession rule): "supersession is an ALL-OR-NOTHING CLAIM keyed (org,
-- PriorResultID, member), written in the SAME transaction as R2's result
-- Save -- two concurrent redemptions of the same (org, R1, member) race on
-- the claim row, exactly one transaction wins, and the loser's round
-- terminates stale_superseded_offer... never two results each believing it
-- superseded the other."
--
-- This table IS that claim row. Its primary key is the atomicity
-- mechanism: pginvestigation.Store.Save inserts one row here, in the SAME
-- transaction as the investigation_results INSERT, for every
-- ConfirmedStructure entry (Disposition=applied, Source=receipt) the
-- result carries -- one claim per (org_id, prior_result_id, member). A
-- second Save attempting the identical tuple (a concurrent redemption of
-- the same prior offer) collides on the primary key; Postgres proves
-- exactly one winner, and the loser's whole transaction (claim insert AND
-- result insert together) rolls back -- see Save's own header comment for
-- why both must fail together (design brief §2.5: "A FAILED Save
-- transaction writes no claim... the claim exists iff the result that
-- redeemed it exists").
--
-- claimed_by_result_id additionally lets an operator (or a future
-- backfill/curation job, §3.5) trace which result actually won a given
-- claim without a second table -- it duplicates result_id's own value
-- already present on the winning investigation_results row, but a claim
-- row with no back-reference at all would make claim -> winner ONLY
-- reconstructible by scanning investigation_results.payload for a matching
-- ConfirmedStructure entry, which is exactly the "reconstruct provenance
-- WITHOUT joining a possibly-dropped capture event" property design brief
-- §2.1/B5 already requires of ConfirmedStructureEntry itself.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). CREATE TABLE IF NOT
-- EXISTS and idempotent constraint adds match every prior migration in
-- this directory.

CREATE TABLE IF NOT EXISTS acr.context_fabric_structure_supersession_claims (
    -- org_id is the SAME auth boundary as investigation_results
    -- (Principal.OrgID) -- part of the claim key so two organizations can
    -- never contend over each other's prior_result_id, even in the
    -- (structurally impossible, but never trusted) event of a result_id
    -- collision across organizations.
    org_id                TEXT NOT NULL,
    -- prior_result_id names the clarification_required (or otherwise
    -- structure-offering) InvestigationResult whose StructureNeeds offer
    -- set this claim supersedes one member of -- the same join key
    -- ConfirmedStructureEntry.PriorResultID already carries.
    prior_result_id       TEXT NOT NULL,
    -- member is the closed StructureNeedKind vocabulary (expected_kind /
    -- subject_anchor / subject_handle / window) -- CHECK below matches
    -- contractsv1.ValidContextFabricStructureNeedKind's own exhaustive
    -- switch exactly; a value this CHECK rejects means the Go and SQL
    -- vocabularies have drifted, not that a real caller supplied something
    -- unexpected.
    member                TEXT NOT NULL,
    -- claimed_by_result_id is the WINNING result_id -- the one whose Save
    -- transaction successfully inserted this row. Never the loser: a
    -- losing Save's whole transaction (this insert included) rolls back,
    -- so no row here ever names a result that failed to persist.
    claimed_by_result_id  TEXT NOT NULL,
    claimed_at            TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    -- The PRIMARY KEY *IS* the atomicity mechanism this table exists for
    -- (see header): a second INSERT for the identical (org_id,
    -- prior_result_id, member) triple is rejected by Postgres itself,
    -- before application code has to reason about the race at all.
    PRIMARY KEY (org_id, prior_result_id, member)
);

ALTER TABLE acr.context_fabric_structure_supersession_claims
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_supersession_org_id_length;
ALTER TABLE acr.context_fabric_structure_supersession_claims
    ADD CONSTRAINT ck_acr_cf_structure_supersession_org_id_length
        CHECK (char_length(org_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_supersession_claims
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_supersession_prior_result_id_length;
ALTER TABLE acr.context_fabric_structure_supersession_claims
    ADD CONSTRAINT ck_acr_cf_structure_supersession_prior_result_id_length
        CHECK (char_length(prior_result_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_supersession_claims
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_supersession_claimed_by_result_id_length;
ALTER TABLE acr.context_fabric_structure_supersession_claims
    ADD CONSTRAINT ck_acr_cf_structure_supersession_claimed_by_result_id_length
        CHECK (char_length(claimed_by_result_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_supersession_claims
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_supersession_member_vocabulary;
ALTER TABLE acr.context_fabric_structure_supersession_claims
    ADD CONSTRAINT ck_acr_cf_structure_supersession_member_vocabulary
        CHECK (member IN ('expected_kind', 'subject_anchor', 'subject_handle', 'window'));

-- Serves structure.go's own pre-flight consult
-- (StructureSupersessionChecker.IsStructureSuperseded): "has (org,
-- prior_result_id, member) already been claimed by someone else" is
-- already the primary key's own leading-column order, so no secondary
-- index is needed for that read. This index exists for the OTHER
-- direction -- "what did result X claim" -- which claimed_by_result_id's
-- own doc comment above names as an operator/backfill need the primary
-- key's (org_id, prior_result_id, member) ordering cannot serve.
CREATE INDEX IF NOT EXISTS ix_acr_cf_structure_supersession_claimed_by
    ON acr.context_fabric_structure_supersession_claims (claimed_by_result_id);
