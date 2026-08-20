-- CHAOS-3860 P6: codex adversarial review round 1 (session
-- 01a01ee8-a021-7602-b4d8-4218d33f5329) confirmed a real gap in 0026's
-- own CHECK: selection_mode = 'agent_receipt' is shared by EVERY
-- credentialed confirmation, not only genuine multi-model panel runs, so
-- a well-formed single-entry consensus_evidence payload from any ordinary
-- caller passed both the DB CHECK and Go's validateEvent. A panel is
-- plural by definition (design brief §2.4/§3.1's own wording: "multi-model
-- panel") -- this migration adds a SEPARATE constraint requiring
-- panel_model_identities to carry at least two DISTINCT entries, as
-- defense in depth alongside Go's own (already-shipped, separate commit)
-- validateEvent tightening.
--
-- NOT folded into 0026 in place: that migration has already been pushed
-- and reviewed, and migrations/postgres/runner.go's own checksum
-- discipline (validateAppliedHistory) rejects any database that already
-- recorded 0026's checksum the moment its file content changes underneath
-- it (codex round 2, session 01a01ef2-0e7a-7e02-bae7-6cbbc69a3ed3,
-- confirmed finding P1) -- exactly the class of mistake this repo's own
-- migration convention (every prior CHAOS-3927 P4 iteration: 0023, 0024,
-- 0025, each its own file) exists to prevent. A new migration is the only
-- safe way to add a constraint after the fact.
--
-- Plain length + jsonb_typeof cannot express "no duplicates" (codex round
-- 2 finding P2, confirmed: length alone, and a subquery expression
-- inlined directly into ALTER TABLE ... ADD CONSTRAINT, are the two ways
-- to get this wrong -- Postgres CHECK constraint expressions reject bare
-- subqueries outright, and a naive length-only check admits duplicate
-- identities). An IMMUTABLE SQL function sidesteps both problems: the
-- subquery-over-a-set-returning-function lives inside the FUNCTION BODY
-- (fully legal SQL there), and the CHECK constraint expression itself is
-- just a single function call, never a subquery.
--
-- codex round 3 (session 01a01efe-0778-7b11-8725-4e8050c4d7c3) confirmed
-- two more real gaps in the round-2 draft of this same migration, fixed
-- below before this file was ever pushed (so no separate 0028 was needed
-- for THESE two -- the migration itself had not left this branch yet):
--
-- (round-3 P1) `ADD CONSTRAINT` validates every EXISTING row by default.
-- consensus_evidence is a brand-new column this same PR introduces (0026
-- never existed on main before this branch), and no production code path
-- writes StructureSelectionEvent.Consensus (Engine.buildStructureSelectionEvent
-- never sets it) -- so no row this constraint could ever reject can exist
-- through any real product code path. But a database that happened to
-- apply this branch's OWN intermediate history (0026 alone, before this
-- constraint existed) and picked up a hand-inserted or test-only
-- single-member row would otherwise wedge on this ALTER, rolling the
-- whole migration back and leaving that database permanently stuck below
-- head. `NOT VALID` is Postgres's own standard answer: it skips the
-- one-time scan of existing rows (so this ALTER can never fail on legacy
-- data) while still enforcing the CHECK on every INSERT/UPDATE from this
-- point forward -- exactly the semantics wanted here, not a weaker
-- constraint. (No follow-up VALIDATE CONSTRAINT is added: validating would
-- reintroduce the exact same possible failure this migration exists to
-- avoid, for a legacy row this codebase's own write path cannot produce.)
--
-- (round-3 P2) the round-2 predicate counted DISTINCT
-- jsonb_array_elements_text() output, which stringifies non-string JSON
-- values -- [1, 2] or [{"a":1},{"b":2}] both "pass" as two distinct
-- identities even though PanelModelIdentities is documented as []string,
-- and the predicate never looked at agreement_bits at all, so a raw SQL
-- writer bypassing the Go sink could insert mismatched-length or
-- non-boolean agreement_bits with no DB-level objection. Fixed below:
-- every panel_model_identities element must be a genuine JSON string,
-- every agreement_bits element must be a genuine JSON boolean, and the two
-- arrays must be the same length -- THEN the distinct-count check runs,
-- now safe because every element it counts is already proven to be a real
-- string (no stringified non-string value can slip in).
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). CREATE OR REPLACE
-- FUNCTION and ADD CONSTRAINT (after an idempotent DROP IF EXISTS) match
-- every prior migration's re-run convention.

CREATE OR REPLACE FUNCTION acr.context_fabric_structure_selections_consensus_is_valid_panel(payload JSONB)
RETURNS BOOLEAN
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    -- COALESCE(..., FALSE) around the WHOLE inner expression, not just one
    -- clause: a missing panel_model_identities key makes
    -- payload -> 'panel_model_identities' SQL NULL, so jsonb_typeof(NULL)
    -- is NULL (not 'array', and not FALSE either) -- every AND'd clause
    -- downstream of that inherits NULL rather than FALSE, so the
    -- UNWRAPPED conjunction below evaluates to NULL for a missing key, and
    -- an integration test (TestConsensusPanelSizeCheck_
    -- RejectsAtDatabaseLevelDirectly) proved a bare `payload IS NULL OR
    -- (...)` still lets that NULL through: CHECK treats a NULL result as
    -- SATISFIED, exactly the same trap 0026's original one-clause CHECK
    -- fell into (codex round 2 finding P2, part 2) and this migration
    -- exists to close. COALESCE forces the missing-key/malformed-shape
    -- case to a definite FALSE.
    SELECT payload IS NULL
        OR COALESCE(
            jsonb_typeof(payload -> 'panel_model_identities') = 'array'
            AND jsonb_typeof(payload -> 'agreement_bits') = 'array'
            AND jsonb_array_length(payload -> 'panel_model_identities') >= 2
            AND jsonb_array_length(payload -> 'panel_model_identities') = jsonb_array_length(payload -> 'agreement_bits')
            -- every identity must be a genuine JSON string (round-3 P2:
            -- jsonb_array_elements_text would otherwise silently stringify
            -- numbers/objects/booleans into "distinct" text values)
            AND NOT EXISTS (
                SELECT 1 FROM jsonb_array_elements(payload -> 'panel_model_identities') AS identity_element
                WHERE jsonb_typeof(identity_element) <> 'string'
            )
            -- every agreement bit must be a genuine JSON boolean
            AND NOT EXISTS (
                SELECT 1 FROM jsonb_array_elements(payload -> 'agreement_bits') AS bit_element
                WHERE jsonb_typeof(bit_element) <> 'boolean'
            )
            -- distinct-identity check: safe now, because every element
            -- counted here was just proven to be a real JSON string above
            AND (
                SELECT count(*) FROM jsonb_array_elements_text(payload -> 'panel_model_identities')
            ) = (
                SELECT count(DISTINCT element) FROM jsonb_array_elements_text(payload -> 'panel_model_identities') AS element
            ),
            FALSE
        );
$$;

ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_consensus_panel_size;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_consensus_panel_size
        CHECK (acr.context_fabric_structure_selections_consensus_is_valid_panel(consensus_evidence))
        NOT VALID;
