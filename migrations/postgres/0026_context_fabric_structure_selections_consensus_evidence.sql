-- CHAOS-3860 P6 precondition fix (pivot-intent design brief, DESIGN-FINAL,
-- §2.4/§B5): the design brief's own sharding table (§4, P6 row) names P6's
-- dependency as "P4 (schema incl. ConsensusEvidence)". P4 (CHAOS-3927,
-- migration 0024) shipped StructureSelectionEvent WITHOUT the
-- ConsensusEvidence field the brief's §2.4 capture-schema contract and
-- §B5 changelog both call for: "events gain ... ConsensusEvidence (3860
-- panel ids + agreement bits)". Discovered as a P4 gap while activating
-- P6 (DP5(b) — chris ratified 2026-08-20); fixed here, additively, as its
-- own migration rather than folded into any P6 harness change.
--
-- ConsensusEvidence is populated ONLY on events captured from a CHAOS-3860
-- agent-user (multi-model panel) acceptance run -- every other
-- SelectionMode (human_panel, agent_receipt, agent_explicit,
-- agent_explicit_echo) leaves this column NULL, mirroring
-- source_watermarks' own additive/nullable precedent (0011). Content
-- discipline matches the brief's own words verbatim: "ids/enums, nothing
-- else" -- panel member model identity ids plus a parallel per-member
-- agreement bit (bool: did that panel member's own independently-derived
-- selection match Selected). No question text, no free-form rationale, no
-- caller-facing labels.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). ADD COLUMN IF NOT
-- EXISTS matches every prior migration's idempotency convention.

ALTER TABLE acr.context_fabric_structure_selections
    ADD COLUMN IF NOT EXISTS consensus_evidence JSONB;

-- consensus_evidence is well-formed JSON only when SelectionMode implies a
-- 3860 panel run captured it (agent_receipt is the vocabulary value that
-- surface uses, per structure_capture.go's own StructureSelectionEvent.
-- SelectionMode doc comment -- P6 runs speak the hosted contract as
-- credentialed non-panel callers, same as any other agent_receipt
-- confirmation); every other mode must leave it NULL, so a future reader
-- can trust presence as the ConsensusEvidence signal itself, never guess
-- from SelectionMode alone.
--
-- IMPORTANT SCOPE NOTE (codex adversarial review, round 1, confirmed):
-- selection_mode = 'agent_receipt' is shared by EVERY credentialed
-- confirmation, panel or not -- this CHECK, like pgstructureselection's
-- Go-side validateEvent, enforces SHAPE (a well-formed, >=2-member payload
-- can only ride an agent_receipt row) and CANNOT by itself prove a given
-- row's consensus_evidence genuinely came from a multi-model panel run
-- rather than a single caller constructing a plausible payload directly
-- against the sink. No production code path populates this column today
-- (Engine.buildStructureSelectionEvent never sets StructureSelectionEvent.
-- Consensus) -- closing that authenticity gap needs request-level
-- provenance this migration deliberately does not add (the P6 activation
-- report's own architectural-fork note: it is either a hosted-contract
-- field addition, full contract-first change, or a harness-side-only
-- consensus computation that never writes this column at all).
ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_consensus_mode;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_consensus_mode
        CHECK (consensus_evidence IS NULL OR selection_mode = 'agent_receipt');

-- A panel is plural by definition (design brief §2.4/§B5/§3.1: "multi-model
-- panel") -- a single-entry payload can never represent agreement OR
-- disagreement between panel members, so the DB itself rejects it, not
-- only the Go-side validateEvent (defense in depth against any future
-- direct-SQL writer that bypasses the sink).
ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_consensus_panel_size;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_consensus_panel_size
        CHECK (consensus_evidence IS NULL OR jsonb_array_length(consensus_evidence -> 'panel_model_identities') >= 2);
